package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

// fakeMailbox is an in-memory read-only mailbox.
type fakeMailbox struct {
	folders      map[string][]mailCandidate
	bodies       map[string]map[imap.UID][]byte
	selectErrors map[string]error
	current      string
}

func (f *fakeMailbox) Select(folder string) (uint32, error) {
	if err := f.selectErrors[folder]; err != nil {
		return 0, err
	}
	if _, ok := f.folders[folder]; !ok {
		return 0, fmt.Errorf("no such mailbox")
	}
	f.current = folder
	return uint32(len(f.folders[folder])), nil
}

func (f *fakeMailbox) Candidates(time.Time) ([]mailCandidate, error) {
	return f.folders[f.current], nil
}

func (f *fakeMailbox) Body(uid imap.UID) ([]byte, error) {
	body, ok := f.bodies[f.current][uid]
	if !ok {
		return nil, fmt.Errorf("no body for uid %d", uid)
	}
	return body, nil
}

func (f *fakeMailbox) Close() error { return nil }

func fakeOpener(f *fakeMailbox) mailboxOpener {
	return func() (mailboxConn, error) { return f, nil }
}

func htmlMessage(body string) []byte {
	return []byte("From: \"Sender\" <s@example.org>\r\n" +
		"To: me@example.org\r\n" +
		"Subject: test\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		body)
}

func candidate(uid int, from, fromName, subject, messageID string, received time.Time) mailCandidate {
	return mailCandidate{
		UID:       imap.UID(uid),
		From:      from,
		FromName:  fromName,
		Subject:   subject,
		MessageID: messageID,
		Received:  received,
	}
}

func TestFilterCandidatesByInternalDate(t *testing.T) {
	since := time.Date(2026, 11, 26, 0, 0, 0, 0, digestLocation)
	until := time.Date(2026, 11, 27, 0, 0, 0, 0, digestLocation)
	src := EmailSource{Name: "S", From: "s@example.org"}
	cands := []mailCandidate{
		candidate(1, "s@example.org", "", "before", "", since.Add(-time.Second)),
		candidate(2, "s@example.org", "", "at since", "", since),
		candidate(3, "s@example.org", "", "inside", "", since.Add(12*time.Hour)),
		candidate(4, "s@example.org", "", "at until", "", until),
	}
	got := filterCandidates(cands, src, since, until)
	if len(got) != 2 || got[0].Subject != "at since" || got[1].Subject != "inside" {
		t.Fatalf("kept %v, want the [since, until) messages", got)
	}
}

func TestFilterCandidatesFromNameAndSubject(t *testing.T) {
	now := time.Now()
	since, until := now.Add(-time.Hour), now.Add(time.Hour)
	cands := []mailCandidate{
		candidate(1, "News@Example.org", "TLDR AI", "The AI Weekly", "", now),
		candidate(2, "news@example.org.evil", "TLDR AI", "The AI Weekly", "", now),
		candidate(3, "news@example.org", "TLDR Dev", "The AI Weekly", "", now),
		candidate(4, "news@example.org", "TLDR AI", "Something else", "", now),
	}
	src := EmailSource{From: "news@example.org", FromName: "tldr ai", SubjectMatch: "ai weekly"}
	got := filterCandidates(cands, src, since, until)
	if len(got) != 1 || got[0].UID != 1 {
		t.Fatalf("kept %v, want only the exact From + display name + subject match", got)
	}
}

func TestCapCandidatesKeepsNewest(t *testing.T) {
	base := time.Now()
	cands := []mailCandidate{
		{UID: 1, Subject: "oldest", Received: base.Add(-3 * time.Hour)},
		{UID: 2, Subject: "newest", Received: base},
		{UID: 3, Subject: "middle", Received: base.Add(-time.Hour)},
	}
	kept, dropped := capCandidates(cands, 1)
	if len(kept) != 1 || kept[0].Subject != "newest" {
		t.Fatalf("kept %v, want the newest only", kept)
	}
	if len(dropped) != 2 || dropped[0].Subject != "middle" || dropped[1].Subject != "oldest" {
		t.Fatalf("dropped %v, want middle then oldest", dropped)
	}
}

func TestCollectEmailsGlobalCapAndDropLog(t *testing.T) {
	base := time.Now()
	f := &fakeMailbox{
		folders: map[string][]mailCandidate{
			"A": {
				candidate(1, "a@example.org", "", "A newest", "m1", base),
				candidate(2, "a@example.org", "", "A older", "m2", base.Add(-2*time.Hour)),
			},
			"B": {
				candidate(3, "b@example.org", "", "B newest", "m3", base.Add(-time.Hour)),
			},
		},
		bodies: map[string]map[imap.UID][]byte{
			"A": {1: htmlMessage("<p>a1</p>"), 2: htmlMessage("<p>a2</p>")},
			"B": {3: htmlMessage("<p>b3</p>")},
		},
	}
	var logBuf bytes.Buffer
	log := &logger{buf: &logBuf}
	sources := []EmailSource{
		{Name: "A", Folder: "A", From: "a@example.org", MaxPerDay: 2},
		{Name: "B", Folder: "B", From: "b@example.org", MaxPerDay: 1},
	}
	emails, failures := collectEmails(fakeOpener(f), sources, base.Add(-24*time.Hour), base.Add(time.Hour), 2, log)
	if failures != 0 {
		t.Fatalf("failures = %d, want 0", failures)
	}
	if len(emails) != 2 || emails[0].Subject != "A newest" || emails[1].Subject != "B newest" {
		t.Fatalf("accepted %v, want the two newest across sources", emails)
	}
	if !strings.Contains(logBuf.String(), "global cap (max_emails_total=2)") || !strings.Contains(logBuf.String(), "A older") {
		t.Errorf("log does not record the global-cap drop:\n%s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "email accepted:") || !strings.Contains(logBuf.String(), "words=") {
		t.Errorf("log does not record accepted emails with word counts:\n%s", logBuf.String())
	}
}

func TestCollectEmailsDedupeByMessageID(t *testing.T) {
	now := time.Now()
	f := &fakeMailbox{
		folders: map[string][]mailCandidate{
			"One": {candidate(7, "x@example.org", "", "same message", "abc@example.org", now)},
			"Two": {candidate(8, "x@example.org", "", "same message", "abc@example.org", now.Add(-time.Minute))},
		},
		bodies: map[string]map[imap.UID][]byte{
			"One": {7: htmlMessage("<p>one</p>")},
			"Two": {8: htmlMessage("<p>two</p>")},
		},
	}
	var logBuf bytes.Buffer
	sources := []EmailSource{
		{Name: "One", Folder: "One", From: "x@example.org", MaxPerDay: 1},
		{Name: "Two", Folder: "Two", From: "x@example.org", MaxPerDay: 1},
	}
	emails, failures := collectEmails(fakeOpener(f), sources, now.Add(-24*time.Hour), now.Add(time.Hour), 5, &logger{buf: &logBuf})
	if failures != 0 || len(emails) != 1 || emails[0].Source != "One" {
		t.Fatalf("emails=%v failures=%d, want one message from the first source", emails, failures)
	}
	if !strings.Contains(logBuf.String(), "duplicate Message-ID") {
		t.Errorf("log does not record the duplicate drop:\n%s", logBuf.String())
	}
}

func TestDecodeEmailPrefersHTMLOverPlain(t *testing.T) {
	raw := []byte("From: a@example.org\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Subject: multi\r\n" +
		"Content-Type: multipart/alternative; boundary=\"BOUND\"\r\n\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"plain body wins nothing\r\n" +
		"--BOUND\r\n" +
		"Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><body><p>html body wins</p></body></html>\r\n" +
		"--BOUND--\r\n")
	body, words, err := decodeEmail(raw)
	if err != nil {
		t.Fatalf("decodeEmail: %v", err)
	}
	if !strings.Contains(body, "html body wins") || strings.Contains(body, "plain body") {
		t.Errorf("body = %q, want the HTML part", body)
	}
	if words != 3 {
		t.Errorf("words = %d, want 3", words)
	}
}

func TestDecodeEmailPlainFallback(t *testing.T) {
	raw := []byte("From: a@example.org\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\n" +
		"first paragraph line one\nline two\n\nsecond paragraph\n")
	body, words, err := decodeEmail(raw)
	if err != nil {
		t.Fatalf("decodeEmail: %v", err)
	}
	if !strings.Contains(body, "<p>first paragraph") || !strings.Contains(body, "<br/>line two") {
		t.Errorf("body = %q, want escaped paragraphs", body)
	}
	if !strings.Contains(body, "<p>second paragraph</p>") {
		t.Errorf("body = %q, want the second paragraph", body)
	}
	if words != 8 {
		t.Errorf("words = %d, want 8", words)
	}
}

func TestDecodeEmailSanitizesImagesScriptsAndStyles(t *testing.T) {
	body, _, err := decodeEmail(htmlMessage(`<html><head><style>p{color:red}</style></head><body>
<!--[if mso]><style>.button-table { padding: 12px 0 }</style><![endif]-->
<script>alert(1)</script>
<p style="color:red">Keep <a href="https://example.org/x">this link</a></p>
<img src="https://tracker.example.org/pixel.gif" width="1"/>
</body></html>`))
	if err != nil {
		t.Fatalf("decodeEmail: %v", err)
	}
	for _, gone := range []string{"<img", "<script", "<style", "style=", "button-table", "tracker.example.org"} {
		if strings.Contains(body, gone) {
			t.Errorf("body still contains %q:\n%s", gone, body)
		}
	}
	if !strings.Contains(body, `href="https://example.org/x"`) || !strings.Contains(body, "this link") {
		t.Errorf("body lost the link:\n%s", body)
	}
}

func TestDecodeEmailISO88591(t *testing.T) {
	raw := []byte("From: a@example.org\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: text/html; charset=iso-8859-1\r\n\r\n" +
		"<p>caf\xe9</p>")
	body, _, err := decodeEmail(raw)
	if err != nil {
		t.Fatalf("decodeEmail: %v", err)
	}
	if !strings.Contains(body, "café") {
		t.Errorf("body = %q, want UTF-8 café", body)
	}
}

func TestEmailSectionsMergeAlphabeticallyWithFeeds(t *testing.T) {
	base := time.Date(2026, 11, 26, 9, 0, 0, 0, digestLocation)
	feeds := map[int64]apiFeed{
		1: {ID: 1, Title: "Zeta Feed"},
		2: {ID: 2, Title: "Alpha Feed"},
	}
	emails := []rawEmail{
		{Source: "TLDR AI", Subject: "ai one", Received: base},
		{Source: "Atomic Habits", Subject: "habits one", Received: base},
	}
	entries := emailEntries(emails, feeds)
	entries = append(entries, apiEntry{FeedID: 1, Title: "rss zeta", PublishedAt: base})
	entries = append(entries, apiEntry{FeedID: 2, Title: "rss alpha", PublishedAt: base})
	sortEntriesByFeed(entries, feeds)

	var prepared []*preparedEntry
	for _, e := range entries {
		prepared = append(prepared, &preparedEntry{
			Title: e.Title, FeedTitle: displayFeedTitle(feeds[e.FeedID]),
			Published: e.PublishedAt, htmlBody: e.Content,
		})
	}
	groups := groupByFeed(prepared)
	var titles []string
	for _, g := range groups {
		titles = append(titles, g.Title)
	}
	want := "Alpha Feed,Atomic Habits,TLDR AI,Zeta Feed"
	if strings.Join(titles, ",") != want {
		t.Fatalf("groups = %v, want %s", titles, want)
	}
	if !strings.Contains(contentsHTML(groups), "TLDR AI — 1 article") {
		t.Errorf("contents page missing the email section:\n%s", contentsHTML(groups))
	}
}

func TestMailboxSurfaceIsReadOnly(t *testing.T) {
	typ := reflect.TypeOf((*mailboxConn)(nil)).Elem()
	forbidden := map[string]bool{
		"Store": true, "Expunge": true, "Move": true, "Copy": true,
		"Append": true, "Delete": true, "Create": true, "Rename": true,
	}
	for i := 0; i < typ.NumMethod(); i++ {
		if forbidden[typ.Method(i).Name] {
			t.Errorf("mailboxConn exposes mutating method %s", typ.Method(i).Name)
		}
	}
	if opts := imapSelectOptions(); !opts.ReadOnly {
		t.Error("imapSelectOptions must EXAMINE (ReadOnly)")
	}
	if opts := imapBodyOptions(); len(opts.BodySection) != 1 || !opts.BodySection[0].Peek {
		t.Error("body fetch must use BODY.PEEK[]")
	}
	if opts := imapCandidateOptions(); len(opts.BodySection) != 0 {
		t.Error("header fetch must not request bodies")
	}
}

// newDigestRSSServer serves one feed with one entry.
func newDigestRSSServer(t *testing.T) (*httptest.Server, time.Time) {
	t.Helper()
	published := time.Now().Add(-time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/feeds":
			_ = json.NewEncoder(w).Encode([]apiFeed{{ID: 1, Title: "RSS Feed"}})
		case "/v1/entries":
			_ = json.NewEncoder(w).Encode(entriesPage{Total: 1, Entries: []apiEntry{{
				ID: 1, FeedID: 1, Title: "RSS entry", Content: "<p>rss body</p>", PublishedAt: published,
			}}})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, published
}

func withFakeMailbox(t *testing.T, f *fakeMailbox) {
	t.Helper()
	old := newMailbox
	newMailbox = func(*Config) (mailboxConn, error) { return f, nil }
	t.Cleanup(func() { newMailbox = old })
}

func TestRunDigestPerSourceFailureStillBuilds(t *testing.T) {
	srv, published := newDigestRSSServer(t)
	f := &fakeMailbox{
		folders: map[string][]mailCandidate{
			"Good": {candidate(1, "good@example.org", "", "Good email", "g1", published)},
		},
		bodies:       map[string]map[imap.UID][]byte{"Good": {1: htmlMessage("<p>good body</p>")}},
		selectErrors: map[string]error{"Broken": fmt.Errorf("mailbox does not exist")},
	}
	withFakeMailbox(t, f)

	cfg := &Config{
		MinifluxURL: srv.URL, MinifluxAPIKey: "secret", OutputDir: t.TempDir(), MaxPartBytes: defaultMaxPartBytes,
		MaxEmailsTotal: 5,
		EmailSources: []EmailSource{
			{Name: "Broken", Folder: "Broken", From: "bad@example.org", MaxPerDay: 1},
			{Name: "Good", Folder: "Good", From: "good@example.org", MaxPerDay: 1},
		},
	}
	var logBuf bytes.Buffer
	result, err := runDigest(cfg, &logger{buf: &logBuf}, time.Now(), options{})
	if err == nil {
		t.Fatal("runDigest succeeded, want a non-zero-exit error for the failed source")
	}
	if result == nil || result.Entries != 2 {
		t.Fatalf("result = %+v, want RSS entry plus good email", result)
	}
	if result.Emails != 1 {
		t.Errorf("result.Emails = %d, want 1", result.Emails)
	}
	if !strings.Contains(logBuf.String(), `email source "Broken" (folder "Broken") failed`) {
		t.Errorf("log missing the source ERROR line:\n%s", logBuf.String())
	}
	files, _ := filepath.Glob(filepath.Join(cfg.OutputDir, "*", "*.epub"))
	if len(files) != 1 {
		t.Fatalf("epub files = %v, want one", files)
	}
	z := zipEntries(t, files[0])
	contents := readZipFile(t, z["EPUB/xhtml/contents.xhtml"])
	if !strings.Contains(contents, "Good — 1 article") || !strings.Contains(contents, "RSS Feed — 1 article") {
		t.Errorf("contents page = %s, want both RSS and email sections", contents)
	}
}

func TestRunDigestAllEmailSourcesFailStillBuildsRSS(t *testing.T) {
	srv, _ := newDigestRSSServer(t)
	f := &fakeMailbox{selectErrors: map[string]error{"TLDR": fmt.Errorf("connection reset")}}
	withFakeMailbox(t, f)

	cfg := &Config{
		MinifluxURL: srv.URL, MinifluxAPIKey: "secret", OutputDir: t.TempDir(), MaxPartBytes: defaultMaxPartBytes,
		MaxEmailsTotal: 5,
		EmailSources: []EmailSource{
			{Name: "TLDR AI", Folder: "TLDR", From: "dan@example.org", MaxPerDay: 1},
		},
	}
	result, err := runDigest(cfg, &logger{}, time.Now(), options{})
	if err == nil {
		t.Fatal("runDigest succeeded, want a non-zero-exit error")
	}
	if result == nil || result.Entries != 1 {
		t.Fatalf("result = %+v, want the RSS-only digest", result)
	}
}

func TestLoadConfigEmailSources(t *testing.T) {
	raw := validConfig()
	raw["email_sources"] = []map[string]any{
		{"name": "TLDR AI", "folder": "TLDR", "from": "dan@example.org"},
	}
	raw["imap_username"] = "user@example.org"
	raw["imap_password"] = "app-password"
	cfg, err := loadConfig(writeConfigFile(t, raw))
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.IMAPHost != defaultIMAPHost || cfg.IMAPPort != defaultIMAPPort {
		t.Errorf("imap defaults = %s:%d", cfg.IMAPHost, cfg.IMAPPort)
	}
	if cfg.MaxEmailsTotal != defaultMaxEmailsTotal {
		t.Errorf("MaxEmailsTotal = %d, want %d", cfg.MaxEmailsTotal, defaultMaxEmailsTotal)
	}
	if cfg.EmailSources[0].MaxPerDay != 1 {
		t.Errorf("MaxPerDay = %d, want default 1", cfg.EmailSources[0].MaxPerDay)
	}

	// Empty sources turn the whole feature off, even with broken IMAP fields.
	off := validConfig()
	off["imap_password"] = ""
	off["imap_port"] = -1
	if _, err := loadConfig(writeConfigFile(t, off)); err != nil {
		t.Fatalf("empty email_sources should ignore IMAP settings: %v", err)
	}
}

func TestLoadConfigEmailSourceValidation(t *testing.T) {
	base := func() map[string]any {
		raw := validConfig()
		raw["imap_username"] = "user@example.org"
		raw["imap_password"] = "hunter2-secret"
		raw["email_sources"] = []map[string]any{
			{"name": "TLDR AI", "folder": "TLDR", "from": "dan@example.org"},
		}
		return raw
	}

	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{"missing username", func(c map[string]any) { delete(c, "imap_username") }, "missing imap_username"},
		{"missing password", func(c map[string]any) { delete(c, "imap_password") }, "missing imap_password"},
		{"bad port", func(c map[string]any) { c["imap_port"] = 70000 }, "imap_port must be between"},
		{"zero total cap", func(c map[string]any) { c["max_emails_total"] = -1 }, "max_emails_total must be at least 1"},
		{"missing name", func(c map[string]any) {
			c["email_sources"] = []map[string]any{{"folder": "TLDR", "from": "dan@example.org"}}
		}, "email_sources[0] is missing name"},
		{"missing folder", func(c map[string]any) {
			c["email_sources"] = []map[string]any{{"name": "TLDR AI", "from": "dan@example.org"}}
		}, "email_sources[0] is missing folder"},
		{"missing from", func(c map[string]any) {
			c["email_sources"] = []map[string]any{{"name": "TLDR AI", "folder": "TLDR"}}
		}, "email_sources[0] is missing from"},
		{"negative per-day cap", func(c map[string]any) {
			c["email_sources"] = []map[string]any{
				{"name": "TLDR AI", "folder": "TLDR", "from": "dan@example.org", "max_per_day": -1},
			}
		}, "max_per_day must not be negative"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := base()
			tt.mutate(raw)
			_, err := loadConfig(writeConfigFile(t, raw))
			if err == nil {
				t.Fatal("loadConfig succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "hunter2-secret") {
				t.Errorf("error leaked the IMAP password: %v", err)
			}
		})
	}
}
