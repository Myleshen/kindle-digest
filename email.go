package main

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-message"
	"github.com/emersion/go-message/mail"
	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

const (
	imapDialTimeout = 30 * time.Second
	readingWPM      = 220
)

// mailCandidate is a message header, fetched before any body.
type mailCandidate struct {
	UID       imap.UID
	From      string
	FromName  string
	Subject   string
	MessageID string
	Received  time.Time
}

// mailboxConn is one read-only IMAP session. The interface deliberately has no
// STORE, EXPUNGE, MOVE or APPEND: the digest only ever reads mail.
type mailboxConn interface {
	Select(folder string) (uint32, error)
	Candidates(since time.Time) ([]mailCandidate, error)
	Body(uid imap.UID) ([]byte, error)
	Close() error
}

type mailboxOpener func() (mailboxConn, error)

// newMailbox is replaced by tests with a fake; production dials Zoho.
var newMailbox = func(cfg *Config) (mailboxConn, error) {
	return dialIMAP(cfg.IMAPHost, cfg.IMAPPort, cfg.IMAPUsername, cfg.IMAPPassword)
}

// rawEmail is one decoded message ready to become a digest entry.
type rawEmail struct {
	Source    string
	Subject   string
	From      string
	Received  time.Time
	MessageID string
	HTML      string
	Words     int
}

// dialIMAP opens an implicit-TLS session and logs in.
func dialIMAP(host string, port int, username, password string) (mailboxConn, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	c, err := imapclient.DialTLS(addr, &imapclient.Options{Dialer: &net.Dialer{Timeout: imapDialTimeout}})
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", addr, err)
	}
	if err := c.Login(username, password).Wait(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("logging in to %s: %w", addr, err)
	}
	return &imapConn{c: c}, nil
}

type imapConn struct {
	c *imapclient.Client
}

// imapSelectOptions always EXAMINEs: a read-only mailbox cannot be mutated by
// this tool, and the server refuses flag changes.
func imapSelectOptions() *imap.SelectOptions {
	return &imap.SelectOptions{ReadOnly: true}
}

// imapCandidateOptions fetches headers only, never bodies.
func imapCandidateOptions() *imap.FetchOptions {
	return &imap.FetchOptions{Envelope: true, InternalDate: true, UID: true}
}

// imapBodyOptions fetches the whole message with BODY.PEEK[], so reading a
// newsletter never sets \Seen.
func imapBodyOptions() *imap.FetchOptions {
	return &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{{Peek: true}},
	}
}

func (m *imapConn) Select(folder string) (uint32, error) {
	data, err := m.c.Select(folder, imapSelectOptions()).Wait()
	if err != nil {
		return 0, err
	}
	return data.NumMessages, nil
}

func (m *imapConn) Candidates(since time.Time) ([]mailCandidate, error) {
	found, err := m.c.UIDSearch(&imap.SearchCriteria{Since: since}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("searching: %w", err)
	}
	uids, ok := found.All.(imap.UIDSet)
	if !ok {
		return nil, fmt.Errorf("search returned %T, want UIDs", found.All)
	}
	nums, _ := uids.Nums()
	if len(nums) == 0 {
		return nil, nil
	}
	msgs, err := m.c.Fetch(imap.UIDSetNum(nums...), imapCandidateOptions()).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetching headers: %w", err)
	}
	out := make([]mailCandidate, 0, len(msgs))
	for _, msg := range msgs {
		if msg.Envelope == nil {
			continue
		}
		c := mailCandidate{
			UID:       msg.UID,
			Subject:   msg.Envelope.Subject,
			MessageID: strings.Trim(msg.Envelope.MessageID, "<>"),
			Received:  msg.InternalDate,
		}
		if len(msg.Envelope.From) > 0 {
			c.From = msg.Envelope.From[0].Addr()
			c.FromName = msg.Envelope.From[0].Name
		}
		out = append(out, c)
	}
	return out, nil
}

func (m *imapConn) Body(uid imap.UID) ([]byte, error) {
	msgs, err := m.c.Fetch(imap.UIDSetNum(uid), imapBodyOptions()).Collect()
	if err != nil {
		return nil, err
	}
	if len(msgs) != 1 || len(msgs[0].BodySection) != 1 {
		return nil, fmt.Errorf("unexpected fetch response")
	}
	return msgs[0].BodySection[0].Bytes, nil
}

func (m *imapConn) Close() error {
	_ = m.c.Logout().Wait()
	return m.c.Close()
}

// filterCandidates keeps messages inside [since, until) whose From address,
// optional display name and optional subject substring match the source.
func filterCandidates(cands []mailCandidate, src EmailSource, since, until time.Time) []mailCandidate {
	var kept []mailCandidate
	for _, c := range cands {
		if !withinWindow(c.Received, since, until) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(c.From), strings.TrimSpace(src.From)) {
			continue
		}
		if src.FromName != "" && !strings.EqualFold(strings.TrimSpace(c.FromName), strings.TrimSpace(src.FromName)) {
			continue
		}
		if src.SubjectMatch != "" && !strings.Contains(strings.ToLower(c.Subject), strings.ToLower(src.SubjectMatch)) {
			continue
		}
		kept = append(kept, c)
	}
	return kept
}

// capCandidates keeps the newest limit messages and returns the rest.
func capCandidates(cands []mailCandidate, limit int) (kept, dropped []mailCandidate) {
	sorted := make([]mailCandidate, len(cands))
	copy(sorted, cands)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Received.After(sorted[j].Received) })
	if limit < 0 {
		limit = 0
	}
	if len(sorted) <= limit {
		return sorted, nil
	}
	return sorted[:limit], sorted[limit:]
}

// collectEmails reads every configured source and applies the per-source and
// global caps. failures counts source-level and message-level errors so the
// caller can exit non-zero without losing the digest.
func collectEmails(open mailboxOpener, sources []EmailSource, since, until time.Time, maxTotal int, log *logger) ([]rawEmail, int) {
	var all []rawEmail
	failures, dropped := 0, 0
	for _, src := range sources {
		emails, msgFailures, drops, err := readEmailSource(open, src, since, until, log)
		failures += msgFailures
		dropped += len(drops)
		for _, c := range drops {
			log.info("email dropped: source=%q subject=%q received=%s reason=%q",
				src.Name, c.Subject, c.Received.In(digestLocation).Format(time.RFC3339),
				fmt.Sprintf("per-source cap (max_per_day=%d)", src.MaxPerDay))
		}
		if err != nil {
			log.error("email source %q (folder %q) failed: %v", src.Name, src.Folder, err)
			failures++
			continue
		}
		all = append(all, emails...)
	}

	sort.SliceStable(all, func(i, j int) bool { return all[i].Received.After(all[j].Received) })
	var selected []rawEmail
	seen := make(map[string]bool)
	for _, em := range all {
		if em.MessageID != "" && seen[em.MessageID] {
			log.info("email dropped: source=%q subject=%q reason=%q", em.Source, em.Subject, "duplicate Message-ID")
			dropped++
			continue
		}
		if len(selected) >= maxTotal {
			log.info("email dropped: source=%q subject=%q received=%s reason=%q",
				em.Source, em.Subject, em.Received.In(digestLocation).Format(time.RFC3339),
				fmt.Sprintf("global cap (max_emails_total=%d)", maxTotal))
			dropped++
			continue
		}
		if em.MessageID != "" {
			seen[em.MessageID] = true
		}
		selected = append(selected, em)
		log.info("email accepted: source=%q subject=%q received=%s words=%d minutes=%d",
			em.Source, em.Subject, em.Received.In(digestLocation).Format(time.RFC3339),
			em.Words, readingMinutes(em.Words))
	}
	log.info("email sources: accepted=%d dropped=%d failed=%d", len(selected), dropped, failures)
	return selected, failures
}

// readEmailSource reads one folder: headers, filters, per-source cap, then
// BODY.PEEK[] only for the few messages that survive.
func readEmailSource(open mailboxOpener, src EmailSource, since, until time.Time, log *logger) ([]rawEmail, int, []mailCandidate, error) {
	conn, err := open()
	if err != nil {
		return nil, 0, nil, err
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Select(src.Folder); err != nil {
		return nil, 0, nil, fmt.Errorf("selecting folder %q: %w", src.Folder, err)
	}
	cands, err := conn.Candidates(since)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("listing folder %q: %w", src.Folder, err)
	}
	kept, dropped := capCandidates(filterCandidates(cands, src, since, until), src.MaxPerDay)

	var out []rawEmail
	failures := 0
	for _, c := range kept {
		body, err := conn.Body(c.UID)
		if err != nil {
			log.error("email source %q (folder %q) message %d failed: %v", src.Name, src.Folder, c.UID, err)
			failures++
			continue
		}
		bodyHTML, words, err := decodeEmail(body)
		if err != nil {
			log.error("email source %q (folder %q) message %d undecodable: %v", src.Name, src.Folder, c.UID, err)
			failures++
			continue
		}
		out = append(out, rawEmail{
			Source:    src.Name,
			Subject:   c.Subject,
			From:      c.From,
			Received:  c.Received,
			MessageID: c.MessageID,
			HTML:      bodyHTML,
			Words:     words,
		})
	}
	return out, failures, dropped, nil
}

func readingMinutes(words int) int {
	if words <= 0 {
		return 0
	}
	return (words + readingWPM - 1) / readingWPM
}

// decodeEmail turns a raw RFC 5322 message into sanitized HTML, preferring
// text/html over text/plain. Charset labels name the source encoding.
func decodeEmail(data []byte) (string, int, error) {
	r, err := mail.CreateReader(bytes.NewReader(data))
	if err != nil && !message.IsUnknownCharset(err) {
		return "", 0, fmt.Errorf("parsing message: %w", err)
	}

	var htmlBody, plainBody string
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) {
			return "", 0, fmt.Errorf("reading part: %w", err)
		}
		mediaType, params, _ := partContentType(part)
		switch mediaType {
		case "text/html":
			if htmlBody == "" {
				htmlBody, err = decodePartBody(part.Body, params["charset"])
			}
		case "text/plain":
			if plainBody == "" {
				plainBody, err = decodePartBody(part.Body, params["charset"])
			}
		}
		if err != nil {
			return "", 0, err
		}
	}

	if htmlBody != "" {
		body, err := sanitizeEmailHTML(htmlBody)
		if err != nil {
			return "", 0, err
		}
		return body, countWordsHTML(body), nil
	}
	if plainBody != "" {
		return plainEmailHTML(plainBody), len(strings.Fields(plainBody)), nil
	}
	return "", 0, fmt.Errorf("no text/html or text/plain part")
}

type contentTyper interface {
	ContentType() (string, map[string]string, error)
}

func partContentType(part *mail.Part) (string, map[string]string, error) {
	if ct, ok := part.Header.(contentTyper); ok {
		return ct.ContentType()
	}
	return "text/plain", nil, nil
}

func decodePartBody(r io.Reader, label string) (string, error) {
	if label == "" || strings.EqualFold(label, "utf-8") || strings.EqualFold(label, "us-ascii") {
		b, err := io.ReadAll(r)
		return string(b), err
	}
	decoded, err := charset.NewReaderLabel(label, r)
	if err != nil {
		return "", err
	}
	b, err := io.ReadAll(decoded)
	return string(b), err
}

// sanitizeEmailHTML strips images (tracking pixels and logos), scripts, styles
// and inline CSS, and keeps text and links.
func sanitizeEmailHTML(src string) (string, error) {
	doc, err := xhtml.Parse(strings.NewReader(src))
	if err != nil {
		return "", fmt.Errorf("parsing email HTML: %w", err)
	}
	body := findElement(doc, "body")
	if body == nil {
		body = doc
	}
	dropEmailElements(body)
	removeStyleAttrs(body)
	return renderChildren(body)
}

func dropEmailElements(n *xhtml.Node) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == xhtml.CommentNode {
			// Conditional comments hold MSO-only <style> blocks.
			n.RemoveChild(c)
		} else if c.Type == xhtml.ElementNode && (c.Data == "img" || dropElements[c.Data]) {
			n.RemoveChild(c)
		} else {
			dropEmailElements(c)
		}
		c = next
	}
}

func removeStyleAttrs(n *xhtml.Node) {
	if n.Type == xhtml.ElementNode {
		attrs := n.Attr[:0]
		for _, a := range n.Attr {
			if !strings.EqualFold(a.Key, "style") {
				attrs = append(attrs, a)
			}
		}
		n.Attr = attrs
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		removeStyleAttrs(c)
	}
}

// plainEmailHTML is the text/plain fallback: blank-line separated paragraphs.
func plainEmailHTML(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var b strings.Builder
	for _, para := range strings.Split(text, "\n\n") {
		para = strings.TrimSpace(para)
		if para == "" {
			continue
		}
		b.WriteString("<p>" + strings.ReplaceAll(html.EscapeString(para), "\n", "<br/>") + "</p>\n")
	}
	return b.String()
}

func countWordsHTML(fragment string) int {
	doc, err := xhtml.Parse(strings.NewReader(fragment))
	if err != nil {
		return 0
	}
	words := 0
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.TextNode {
			words += len(strings.Fields(n.Data))
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return words
}

// emailEntries turns accepted emails into digest entries, one synthetic feed
// per source so the existing grouping, Contents and Index pages apply.
func emailEntries(emails []rawEmail, feeds map[int64]apiFeed) []apiEntry {
	entries := make([]apiEntry, 0, len(emails))
	for i, em := range emails {
		id := emailFeedIDBase - int64(i)
		feeds[id] = apiFeed{ID: id, Title: em.Source}
		entries = append(entries, apiEntry{
			ID:          id,
			FeedID:      id,
			Title:       em.Subject,
			Content:     em.HTML,
			PublishedAt: em.Received,
		})
	}
	return entries
}

// emailFeedIDBase keeps synthetic email feeds clear of Miniflux's positive ids.
const emailFeedIDBase = -1_000_000
