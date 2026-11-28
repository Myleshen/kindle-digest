package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const (
	perEntryImageBudget = 2_500_000
	imageTimeout        = 10 * time.Second
	maxConcurrentImages = 4
	maxImageBytes       = 8 << 20
	partOverhead        = 20_000
	truncationReserve   = 50_000
	minTruncatedHTML    = 10_000

	// Chrome desktop user agent; some image hosts reject unknown clients.
	chromeUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

var dropElements = map[string]bool{
	"script":   true,
	"style":    true,
	"iframe":   true,
	"form":     true,
	"nav":      true,
	"button":   true,
	"svg":      true,
	"ins":      true,
	"video":    true,
	"audio":    true,
	"object":   true,
	"embed":    true,
	"canvas":   true,
	"frame":    true,
	"frameset": true,
	"applet":   true,
}

type preparedImage struct {
	Filename string
	Data     []byte
}

type preparedEntry struct {
	Title     string
	FeedTitle string
	Author    string
	URL       string
	Published time.Time

	dom      *html.Node
	htmlBody string
	images   []preparedImage
}

// HTML returns the sanitized content with image sources rewritten to the
// EPUB-internal image paths.
func (e *preparedEntry) HTML() string { return e.htmlBody }

// Estimate is the packing cost: text, images and per-entry overhead.
func (e *preparedEntry) Estimate() int {
	n := len(e.htmlBody) + partOverhead
	for _, img := range e.images {
		n += len(img.Data)
	}
	return n
}

// imageDownloader fetches images with a global in-flight cap.
type imageDownloader struct {
	hc  *http.Client
	sem chan struct{}
}

func newImageDownloader() *imageDownloader {
	return &imageDownloader{
		hc:  &http.Client{Timeout: imageTimeout},
		sem: make(chan struct{}, maxConcurrentImages),
	}
}

func (d *imageDownloader) fetch(ctx context.Context, src, referer string) ([]byte, error) {
	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-d.sem }()

	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, fmt.Errorf("building image request: %w", err)
	}
	req.Header.Set("User-Agent", chromeUserAgent)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}

	resp, err := d.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading image: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading image: server returned %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return nil, fmt.Errorf("reading image body: %w", err)
	}
	return data, nil
}

// prepareEntry sanitizes one entry's HTML, downloads and normalizes its
// images, enforces the per-entry image budget and truncates oversized text.
func prepareEntry(ctx context.Context, idx int, entry apiEntry, feed apiFeed, maxPart int, downloads *imageDownloader) (*preparedEntry, error) {
	doc, err := html.Parse(strings.NewReader(entry.Content))
	if err != nil {
		return nil, fmt.Errorf("parsing HTML: %w", err)
	}
	body := findElement(doc, "body")
	if body == nil {
		return nil, fmt.Errorf("parsing HTML: no body element")
	}

	stripElements(body)
	imgNodes := collectImages(body)
	referer := siteOrigin(feed.SiteURL)

	type outcome struct {
		data   []byte
		err    error
		remove bool
	}
	outcomes := make([]outcome, len(imgNodes))

	var wg sync.WaitGroup
	for i, node := range imgNodes {
		src := strings.TrimSpace(elementAttr(node, "src"))
		switch {
		case src == "" || strings.HasPrefix(strings.ToLower(src), "data:"):
			outcomes[i] = outcome{remove: true}
		case !isHTTPURL(src):
			outcomes[i] = outcome{err: fmt.Errorf("unsupported image URL %q", src)}
		default:
			wg.Add(1)
			go func(i int, src string) {
				defer wg.Done()
				raw, err := downloads.fetch(ctx, src, referer)
				if err == nil {
					raw, err = processImage(raw)
				}
				outcomes[i] = outcome{data: raw, err: err}
			}(i, src)
		}
	}
	wg.Wait()

	feedTitle := displayFeedTitle(feed)
	e := &preparedEntry{
		Title:     entry.Title,
		FeedTitle: feedTitle,
		Author:    entry.Author,
		URL:       entry.URL,
		Published: entry.PublishedAt,
		dom:       body,
	}

	for i, node := range imgNodes {
		switch {
		case outcomes[i].remove:
			node.Parent.RemoveChild(node)
		case outcomes[i].err != nil:
			replaceNode(node, unavailableNode())
		default:
			name := fmt.Sprintf("e%d-i%d.jpg", idx+1, i+1)
			setElementAttr(node, "src", "../images/"+name)
			e.images = append(e.images, preparedImage{Filename: name, Data: outcomes[i].data})
		}
	}

	// Per-entry image budget: drop the largest images until under the cap.
	var dropped []string
	e.images, dropped = dropOversizedImages(e.images, perEntryImageBudget)
	for _, name := range dropped {
		dropImageNode(body, name)
	}

	if e.htmlBody, err = renderChildren(body); err != nil {
		return nil, fmt.Errorf("rendering HTML: %w", err)
	}

	if len(e.htmlBody) > maxPart {
		e.truncate(maxPart - truncationReserve)
	}
	return e, nil
}

// shrinkToFit removes bytes from a single entry that alone overflows a part.
// It returns false when nothing else can be removed.
func (e *preparedEntry) shrinkToFit(maxPart int) bool {
	if len(e.images) > 0 {
		largest := 0
		for i := range e.images {
			if len(e.images[i].Data) > len(e.images[largest].Data) {
				largest = i
			}
		}
		dropImageNode(e.dom, e.images[largest].Filename)
		e.images = append(e.images[:largest], e.images[largest+1:]...)
		e.rerender()
		return true
	}

	if len(e.htmlBody) > maxPart-truncationReserve {
		e.truncate(maxPart - truncationReserve)
		return true
	}
	if len(e.htmlBody) > minTruncatedHTML {
		limit := len(e.htmlBody) / 2
		if limit < minTruncatedHTML {
			limit = minTruncatedHTML
		}
		e.truncate(limit)
		return true
	}
	return false
}

func (e *preparedEntry) rerender() {
	if e.dom == nil {
		return
	}
	if rendered, err := renderChildren(e.dom); err == nil {
		e.htmlBody = rendered
	}
}

// truncate cuts the rendered HTML at limit bytes, rebalances the tags and
// appends a visible note. Images whose nodes were cut away are discarded.
func (e *preparedEntry) truncate(limit int) {
	e.htmlBody = truncateHTML(e.htmlBody, limit)
	// Rebuild the DOM so later image drops operate on the truncated document.
	if doc, err := html.Parse(strings.NewReader(e.htmlBody)); err == nil {
		if body := findElement(doc, "body"); body != nil {
			e.dom = body
		}
	}
	kept := e.images[:0]
	for _, img := range e.images {
		if strings.Contains(e.htmlBody, "../images/"+img.Filename) {
			kept = append(kept, img)
		}
	}
	e.images = kept
}

func truncateHTML(s string, limit int) string {
	const note = "<p><em>[content truncated]</em></p>"
	if len(s) <= limit {
		return s
	}

	budget := limit - len(note)
	if budget < 0 {
		budget = 0
	}
	for {
		cut := s
		if len(cut) > budget {
			cut = cut[:budget]
		}
		if i := strings.LastIndex(cut, ">"); i >= 0 {
			cut = cut[:i+1]
		}
		balanced := balanceFragment(cut)
		if len(balanced) <= budget || budget < 100 {
			return balanced + note
		}
		// Closing tags pushed the result over budget; shave a little more.
		budget -= 100
	}
}

func balanceFragment(s string) string {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	body := findElement(doc, "body")
	if body == nil {
		return s
	}
	rendered, err := renderChildren(body)
	if err != nil {
		return s
	}
	return rendered
}

func stripElements(n *html.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.ElementNode && dropElements[c.Data] {
			n.RemoveChild(c)
		} else {
			stripElements(c)
		}
		c = next
	}
}

func collectImages(n *html.Node) []*html.Node {
	var imgs []*html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode && node.Data == "img" {
			imgs = append(imgs, node)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return imgs
}

func findElement(n *html.Node, name string) *html.Node {
	if n.Type == html.ElementNode && n.Data == name {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findElement(c, name); found != nil {
			return found
		}
	}
	return nil
}

func elementAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setElementAttr(n *html.Node, key, value string) {
	for i := range n.Attr {
		if n.Attr[i].Key == key {
			n.Attr[i].Val = value
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: value})
}

func renderChildren(n *html.Node) (string, error) {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&b, c); err != nil {
			return "", err
		}
	}
	return b.String(), nil
}

func replaceNode(old, replacement *html.Node) {
	if old.Parent == nil {
		return
	}
	old.Parent.InsertBefore(replacement, old)
	old.Parent.RemoveChild(old)
}

func unavailableNode() *html.Node {
	em := &html.Node{Type: html.ElementNode, Data: "em"}
	em.AppendChild(&html.Node{Type: html.TextNode, Data: "[image unavailable]"})
	return em
}

func dropImageNode(root *html.Node, filename string) {
	if root == nil {
		return
	}
	src := "../images/" + filename
	var walk func(*html.Node) bool
	walk = func(n *html.Node) bool {
		if n.Type == html.ElementNode && n.Data == "img" && elementAttr(n, "src") == src {
			if n.Parent != nil {
				n.Parent.RemoveChild(n)
			}
			return true
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if walk(c) {
				return true
			}
		}
		return false
	}
	walk(root)
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func siteOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// dropOversizedImages removes the largest images until the total fits the
// budget. It returns the kept images and the filenames of the dropped ones.
func dropOversizedImages(images []preparedImage, budget int) ([]preparedImage, []string) {
	total := 0
	for _, img := range images {
		total += len(img.Data)
	}
	var dropped []string
	for total > budget && len(images) > 0 {
		largest := 0
		for i := range images {
			if len(images[i].Data) > len(images[largest].Data) {
				largest = i
			}
		}
		total -= len(images[largest].Data)
		dropped = append(dropped, images[largest].Filename)
		images = append(images[:largest], images[largest+1:]...)
	}
	return images, dropped
}
