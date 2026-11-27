package main

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

var asciiFilenameRE = regexp.MustCompile(`^[A-Za-z0-9 ._-]+$`)

func TestResolveFeed(t *testing.T) {
	feeds := map[int64]apiFeed{
		13: {ID: 13, Title: "Rust Blog"},
		42: {ID: 42, Title: "Example Feed"},
		43: {ID: 43, Title: "Other Feed"},
		44: {ID: 44, Title: "Rust Blog Extra"},
	}

	tests := []struct {
		name    string
		query   string
		wantID  int64
		wantErr string
	}{
		{"id", "13", 13, ""},
		{"exact title", "rust blog", 13, ""},
		{"substring", "example", 42, ""},
		{"ambiguous substring", "rust", 0, `matches 2 feeds`},
		{"none", "python", 0, `no feed matches`},
		{"missing id", "99", 0, `feed id 99 not found`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			feed, err := resolveFeed(feeds, tt.query)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveFeed error = %v, want it to contain %q", err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), `"Rust Blog"`) {
					t.Errorf("error does not list candidate titles: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveFeed: %v", err)
			}
			if feed.ID != tt.wantID {
				t.Errorf("feed = %+v, want id %d", feed, tt.wantID)
			}
		})
	}
}

func TestClampLimit(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{-5, 1},
		{0, 1},
		{1, 1},
		{50, 50},
		{500, 500},
		{501, 500},
		{10000, 500},
	}
	for _, tt := range tests {
		if got := clampLimit(tt.in); got != tt.want {
			t.Errorf("clampLimit(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestASCIISanitizer(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Rust Blog", "Rust Blog"},
		{"Café — Tech", "Caf Tech"},
		{"Rust 🦀 Weekly", "Rust Weekly"},
		{"Daily_Digest-2.0", "Daily_Digest-2.0"},
		{"  a   b  ", "a b"},
		{"Ünïcode Feed", "n code Feed"},
	}
	for _, tt := range tests {
		got := asciiFilename(tt.in)
		if got != tt.want {
			t.Errorf("asciiFilename(%q) = %q, want %q", tt.in, got, tt.want)
		}
		if !asciiFilenameRE.MatchString(got) {
			t.Errorf("asciiFilename(%q) = %q, does not match %s", tt.in, got, asciiFilenameRE)
		}
	}
}

func TestCollectionBaseName(t *testing.T) {
	tests := []struct {
		feed  string
		count int
		want  string
	}{
		{"Rust Blog", 30, "Rust Blog - 30 articles - 2026-11-27"},
		{"Café — Tech", 3, "Caf Tech - 3 articles - 2026-11-27"},
		{"日本語", 1, "Feed - 1 articles - 2026-11-27"},
	}
	for _, tt := range tests {
		got := collectionBaseName(tt.feed, tt.count, "2026-11-27")
		if got != tt.want {
			t.Errorf("collectionBaseName(%q, %d) = %q, want %q", tt.feed, tt.count, got, tt.want)
		}
		if !asciiFilenameRE.MatchString(got) {
			t.Errorf("collectionBaseName(%q, %d) = %q, is not pure ASCII", tt.feed, tt.count, got)
		}
	}
}

func TestFetchFeedEntriesPagination(t *testing.T) {
	makeEntries := func(from, n int) []apiEntry {
		entries := make([]apiEntry, n)
		for i := range entries {
			entries[i] = apiEntry{
				ID:          int64(from + i),
				FeedID:      13,
				Title:       fmt.Sprintf("Entry %d", from+i),
				Content:     "<p>x</p>",
				PublishedAt: time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC).Add(-time.Duration(from+i) * time.Hour),
			}
		}
		return entries
	}

	var limits, offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/feeds/13/entries" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Has("published_after") {
			t.Errorf("collection request must not send published_after: %s", r.URL.RawQuery)
		}
		if got := q.Get("order"); got != "published_at" {
			t.Errorf("order = %q, want published_at", got)
		}
		if got := q.Get("direction"); got != "desc" {
			t.Errorf("direction = %q, want desc", got)
		}
		limits = append(limits, q.Get("limit"))
		offsets = append(offsets, q.Get("offset"))

		page := entriesPage{Total: 150}
		switch q.Get("offset") {
		case "0":
			page.Entries = makeEntries(0, 100)
		case "100":
			page.Entries = makeEntries(100, 50)
		default:
			t.Errorf("unexpected offset %q", q.Get("offset"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer srv.Close()

	entries, err := newMinifluxClient(srv.URL, "secret").fetchFeedEntries(t.Context(), 13, 150)
	if err != nil {
		t.Fatalf("fetchFeedEntries: %v", err)
	}
	if len(entries) != 150 {
		t.Fatalf("entries = %d, want 150", len(entries))
	}
	if got, want := strings.Join(limits, ","), "100,50"; got != want {
		t.Errorf("page limits = %s, want %s", got, want)
	}
	if got, want := strings.Join(offsets, ","), "0,100"; got != want {
		t.Errorf("offsets = %s, want %s", got, want)
	}
}

func TestCollectionRunIgnoresWindowAndSkipsSend(t *testing.T) {
	feedID := int64(13)
	feedTitle := "Rust Blog"
	entries := []apiEntry{
		{ID: 1, FeedID: feedID, Title: "First", Content: "<p>first article</p>", PublishedAt: time.Now().Add(-48 * time.Hour)},
		{ID: 2, FeedID: feedID, Title: "Second", Content: "<p>second article</p>", PublishedAt: time.Now().Add(-72 * time.Hour)},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/feeds":
			_ = json.NewEncoder(w).Encode([]apiFeed{{ID: feedID, Title: feedTitle, SiteURL: "https://example.org"}})
		case fmt.Sprintf("/v1/feeds/%d/entries", feedID):
			q := r.URL.Query()
			if q.Has("published_after") {
				t.Errorf("collection fetch must ignore the 24h window: %s", r.URL.RawQuery)
			}
			if got := q.Get("limit"); got != "30" {
				t.Errorf("limit = %q, want 30", got)
			}
			_ = json.NewEncoder(w).Encode(entriesPage{Total: len(entries), Entries: entries})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Complete SMTP block, but the run must not touch it without -send.
	cfg := &Config{
		MinifluxURL:    srv.URL,
		MinifluxAPIKey: "secret",
		KindleEmail:    "reader@kindle.com",
		SMTPHost:       "127.0.0.1",
		SMTPPort:       1,
		SMTPUsername:   "user@example.org",
		SMTPPassword:   "pass",
		SMTPFrom:       "user@example.org",
		OutputDir:      t.TempDir(),
		MaxPartBytes:   defaultMaxPartBytes,
	}
	if !cfg.sendMode() {
		t.Fatal("test config should be in send mode")
	}

	date := time.Now().In(digestLocation).Format("2006-01-02")
	if _, err := runCollection(cfg, &logger{}, time.Now(), options{feed: "13", limit: 30}); err != nil {
		t.Fatalf("runCollection: %v", err)
	}

	files, err := filepath.Glob(filepath.Join(cfg.OutputDir, date, "*.epub"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("epub files = %v, want exactly one", files)
	}
	base := filepath.Base(files[0])
	want := fmt.Sprintf("%s - 2 articles - %s, Part 1.epub", feedTitle, date)
	if base != want {
		t.Errorf("filename = %q, want %q", base, want)
	}
	for _, r := range base {
		if r > 127 {
			t.Errorf("filename %q contains non-ASCII rune %q", base, r)
			break
		}
	}

	if title := epubTitle(t, files[0]); title != strings.TrimSuffix(want, ", Part 1.epub") {
		t.Errorf("dc:title = %q, want %q", title, strings.TrimSuffix(want, ", Part 1.epub"))
	}
}

func TestCollectionSendRequiresFlag(t *testing.T) {
	// -send without -feed is rejected before any config or network access.
	err := run(filepath.Join(t.TempDir(), "missing.json"), options{send: true})
	if err == nil || !strings.Contains(err.Error(), "-send requires -feed") {
		t.Fatalf("run error = %v, want -send requires -feed", err)
	}
}

func TestPackCollectionBaseNameSplitsSmallParts(t *testing.T) {
	entries := []*preparedEntry{
		fakeEntry("a", 4000),
		fakeEntry("b", 4000),
		fakeEntry("c", 4000),
	}
	base := "Rust Blog - 3 articles - 2026-11-27"
	p := &packer{
		maxPart:  24_000, // one 24 KB estimate per part
		baseName: base,
		build:    exactBuild(1),
		shrink:   func(*preparedEntry) bool { return false },
	}
	parts, err := p.pack(entries, t.TempDir())
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if len(parts) != 3 {
		t.Fatalf("parts = %d, want 3", len(parts))
	}
	for i, pt := range parts {
		want := fmt.Sprintf("%s, Part %d.epub", base, i+1)
		if got := filepath.Base(pt.path); got != want {
			t.Errorf("part %d filename = %q, want %q", i+1, got, want)
		}
		if _, err := os.Stat(pt.path); err != nil {
			t.Errorf("part %d file missing: %v", i+1, err)
		}
	}
}

// epubTitle returns dc:title from the first OPF entry in an EPUB.
func epubTitle(t *testing.T, path string) string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("opening EPUB: %v", err)
	}
	defer func() { _ = zr.Close() }()

	for _, f := range zr.File {
		if !strings.HasSuffix(f.Name, ".opf") {
			continue
		}
		opf := readZipFile(t, f)
		const open, close = "<dc:title>", "</dc:title>"
		start := strings.Index(opf, open)
		end := strings.Index(opf, close)
		if start < 0 || end < start {
			t.Fatalf("dc:title missing from OPF:\n%s", opf)
		}
		return opf[start+len(open) : end]
	}
	t.Fatal("OPF missing from EPUB")
	return ""
}
