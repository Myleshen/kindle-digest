package main

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmaupin/go-epub"
)

const (
	digestAuthor = "Daily Digest"
	bylineLayout = "2 Jan 2006 15:04 MST"
)

const digestCSS = `body { font-family: "Bookerly", "Caecilia", Georgia, serif; font-size: 1em; line-height: 1.5; text-align: left; }
h1, h2, h3, h4, h5, h6 { font-family: "Amazon Ember", "Helvetica Neue", Arial, sans-serif; }
h1 { font-size: 1.6em; }
h2 { font-size: 1.3em; }
h3 { font-size: 1.1em; }
img { max-width: 100%; height: auto; page-break-inside: avoid; }
figure { page-break-inside: avoid; }
p { orphans: 2; widows: 2; text-align: left; }
a { color: #000; text-decoration: underline; }
pre, code { font-family: "Courier Prime", "Courier New", monospace; font-size: 0.85em; white-space: pre-wrap; word-break: break-word; }
table { max-width: 100%; font-size: 0.9em; }
.index-feed { color: #555; font-size: 0.85em; }
`

// buildEPUB writes one flat EPUB (collection mode) and returns its byte size.
// title becomes dc:title and dateLabel is drawn on the generated cover.
func buildEPUB(ctx context.Context, entries []*preparedEntry, title, dateLabel, dst string) (int64, error) {
	return buildBook(ctx, entries, title, dateLabel, dst, false)
}

// buildDigestEPUB writes one daily-digest part: a Contents page, a separator
// page per feed and articles grouped under those separators.
func buildDigestEPUB(ctx context.Context, entries []*preparedEntry, title, dateLabel, dst string) (int64, error) {
	return buildBook(ctx, entries, title, dateLabel, dst, true)
}

func buildBook(ctx context.Context, entries []*preparedEntry, title, dateLabel, dst string, grouped bool) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	tmpDir, err := os.MkdirTemp("", "kindle-digest-epub-")
	if err != nil {
		return 0, fmt.Errorf("creating temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	book := epub.NewEpub(title)
	book.SetAuthor(digestAuthor)
	book.SetLang("en")

	cssPath := filepath.Join(tmpDir, "digest.css")
	if err := os.WriteFile(cssPath, []byte(digestCSS), 0o644); err != nil {
		return 0, fmt.Errorf("writing temp CSS: %w", err)
	}
	cssHref, err := book.AddCSS(cssPath, "digest.css")
	if err != nil {
		return 0, fmt.Errorf("adding CSS: %w", err)
	}

	coverData, err := generateCover(dateLabel)
	if err != nil {
		return 0, err
	}
	coverPath := filepath.Join(tmpDir, "cover.jpg")
	if err := os.WriteFile(coverPath, coverData, 0o644); err != nil {
		return 0, fmt.Errorf("writing cover image: %w", err)
	}
	coverHref, err := book.AddImage(coverPath, "cover.jpg")
	if err != nil {
		return 0, fmt.Errorf("adding cover image: %w", err)
	}
	book.SetCover(coverHref, "")

	groups := []feedGroup{{Entries: entries}}
	if grouped {
		groups = groupByFeed(entries)
	}
	refs := assignArticles(groups)

	for i, ref := range refs {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := addEntryImages(tmpDir, book, ref.entry); err != nil {
			return 0, fmt.Errorf("adding images for entry %d: %w", i+1, err)
		}
	}

	if grouped {
		if _, err := book.AddSection(contentsHTML(groups), "Contents", "contents.xhtml", cssHref); err != nil {
			return 0, fmt.Errorf("adding contents section: %w", err)
		}
	}
	if _, err := book.AddSection(indexHTML(refs), "Index", "index.xhtml", cssHref); err != nil {
		return 0, fmt.Errorf("adding index section: %w", err)
	}

	next := 0
	for i, g := range groups {
		if grouped {
			separator := fmt.Sprintf("feed%04d.xhtml", i+1)
			if _, err := book.AddSection(separatorHTML(g), g.Title, separator, cssHref); err != nil {
				return 0, fmt.Errorf("adding feed separator %d: %w", i+1, err)
			}
		}
		for range g.Entries {
			ref := refs[next]
			next++
			if _, err := book.AddSection(sectionHTML(ref.entry), ref.entry.FeedTitle+": "+entryTitle(ref.entry), ref.filename, cssHref); err != nil {
				return 0, fmt.Errorf("adding section %d: %w", next, err)
			}
		}
	}

	if err := book.Write(dst); err != nil {
		return 0, fmt.Errorf("writing EPUB: %w", err)
	}
	info, err := os.Stat(dst)
	if err != nil {
		return 0, fmt.Errorf("statting EPUB: %w", err)
	}
	return info.Size(), nil
}

// articleRef pairs an entry with the section filename every front page links
// to. Filenames are assigned in reading order, before the pages are written.
type articleRef struct {
	entry    *preparedEntry
	filename string
}

func assignArticles(groups []feedGroup) []articleRef {
	var refs []articleRef
	for _, g := range groups {
		for _, e := range g.Entries {
			refs = append(refs, articleRef{entry: e, filename: fmt.Sprintf("art%04d.xhtml", len(refs)+1)})
		}
	}
	return refs
}

// addEntryImages writes one entry's images next to the book's other temp files
// and registers them under their stable eN-iM.jpg names.
func addEntryImages(tmpDir string, book *epub.Epub, e *preparedEntry) error {
	for _, img := range e.images {
		imgPath := filepath.Join(tmpDir, img.Filename)
		if err := os.WriteFile(imgPath, img.Data, 0o644); err != nil {
			return fmt.Errorf("writing image %s: %w", img.Filename, err)
		}
		href, err := book.AddImage(imgPath, img.Filename)
		if err != nil {
			return fmt.Errorf("adding image %s: %w", img.Filename, err)
		}
		if want := "../images/" + img.Filename; href != want {
			return fmt.Errorf("image %s has href %q, want %q", img.Filename, href, want)
		}
	}
	return nil
}

// feedGroup is a contiguous run of entries from one feed.
type feedGroup struct {
	Title   string
	Entries []*preparedEntry
}

func groupByFeed(entries []*preparedEntry) []feedGroup {
	var groups []feedGroup
	for _, e := range entries {
		if len(groups) == 0 || groups[len(groups)-1].Title != e.FeedTitle {
			groups = append(groups, feedGroup{Title: e.FeedTitle})
		}
		groups[len(groups)-1].Entries = append(groups[len(groups)-1].Entries, e)
	}
	return groups
}

func contentsHTML(groups []feedGroup) string {
	var b strings.Builder
	b.WriteString("<h1>Contents</h1>\n<ul>\n")
	for i, g := range groups {
		fmt.Fprintf(&b, `<li><a href="feed%04d.xhtml">%s — %s</a></li>`+"\n",
			i+1, html.EscapeString(g.Title), articleCount(len(g.Entries)))
	}
	b.WriteString("</ul>")
	return b.String()
}

// indexHTML lists every article in the part by title under A-Z headings, with
// non-alphabetic starts under '#'. Links reuse the assigned section filenames.
func indexHTML(refs []articleRef) string {
	sorted := make([]articleRef, len(refs))
	copy(sorted, refs)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti := strings.ToLower(entryTitle(sorted[i].entry))
		tj := strings.ToLower(entryTitle(sorted[j].entry))
		if ti != tj {
			return ti < tj
		}
		fi := strings.ToLower(sorted[i].entry.FeedTitle)
		fj := strings.ToLower(sorted[j].entry.FeedTitle)
		if fi != fj {
			return fi < fj
		}
		return sorted[i].entry.Published.After(sorted[j].entry.Published)
	})

	byHeading := make(map[string][]articleRef)
	for _, r := range sorted {
		h := indexHeading(entryTitle(r.entry))
		byHeading[h] = append(byHeading[h], r)
	}

	var b strings.Builder
	b.WriteString("<h1>Index</h1>\n")
	for _, h := range []string{"#", "A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K", "L", "M",
		"N", "O", "P", "Q", "R", "S", "T", "U", "V", "W", "X", "Y", "Z"} {
		items := byHeading[h]
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "<h2>%s</h2>\n<ul>\n", h)
		for _, r := range items {
			fmt.Fprintf(&b, `<li><a href="%s">%s</a> <span class="index-feed">— %s</span></li>`+"\n",
				r.filename, html.EscapeString(entryTitle(r.entry)), html.EscapeString(r.entry.FeedTitle))
		}
		b.WriteString("</ul>\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// indexHeading is the letter a title files under: A-Z for an ASCII letter
// start, '#' for digits, symbols and non-ASCII starts.
func indexHeading(title string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return "#"
	}
	r := []rune(t)[0]
	switch {
	case r >= 'a' && r <= 'z':
		return string(r - 'a' + 'A')
	case r >= 'A' && r <= 'Z':
		return string(r)
	default:
		return "#"
	}
}

// separatorHTML opens a feed's section: its title and the article count plus
// the publication date span. Kept ASCII-only.
func separatorHTML(g feedGroup) string {
	oldest, newest := g.Entries[0].Published, g.Entries[0].Published
	for _, e := range g.Entries[1:] {
		if e.Published.Before(oldest) {
			oldest = e.Published
		}
		if e.Published.After(newest) {
			newest = e.Published
		}
	}
	span := oldest.In(digestLocation).Format("2 Jan 2006") + " - " + newest.In(digestLocation).Format("2 Jan 2006")
	return fmt.Sprintf("<h1>%s</h1>\n<p class=\"meta\">%s, %s</p>",
		html.EscapeString(g.Title), articleCount(len(g.Entries)), span)
}

func articleCount(n int) string {
	if n == 1 {
		return "1 article"
	}
	return fmt.Sprintf("%d articles", n)
}

func entryTitle(e *preparedEntry) string {
	if e.Title == "" {
		return "(untitled)"
	}
	return e.Title
}

func sectionHTML(e *preparedEntry) string {
	return "<h1>" + html.EscapeString(entryTitle(e)) + "</h1>\n" + bylineHTML(e) + "\n" + e.HTML()
}

func bylineHTML(e *preparedEntry) string {
	var meta []string
	if e.Author != "" {
		meta = append(meta, html.EscapeString(e.Author))
	}
	meta = append(meta, html.EscapeString(e.FeedTitle))
	line := strings.Join(meta, " — ")
	line += ", " + e.Published.In(digestLocation).Format(bylineLayout)
	if e.URL != "" {
		line += fmt.Sprintf(` — <a href="%s">original</a>`, html.EscapeString(e.URL))
	}
	return `<p class="byline">` + line + `</p>`
}
