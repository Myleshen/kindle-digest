package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseWindowTime(t *testing.T) {
	tests := []struct {
		in   string
		want time.Time
	}{
		{"2026-11-26T10:30:00+05:30", time.Date(2026, 11, 26, 10, 30, 0, 0, digestLocation)},
		{"2026-11-26T10:30", time.Date(2026, 11, 26, 10, 30, 0, 0, digestLocation)},
		{"2026-11-26", time.Date(2026, 11, 26, 0, 0, 0, 0, digestLocation)},
	}
	for _, tt := range tests {
		got, err := parseWindowTime(tt.in)
		if err != nil {
			t.Errorf("parseWindowTime(%q): %v", tt.in, err)
			continue
		}
		if !got.Equal(tt.want) {
			t.Errorf("parseWindowTime(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
	for _, in := range []string{"", "26 Nov 2026", "2026-13-01", "2026-11-26T25:00"} {
		if _, err := parseWindowTime(in); err == nil {
			t.Errorf("parseWindowTime(%q) succeeded, want error", in)
		}
	}
}

func TestWindowLabelAndTitles(t *testing.T) {
	since := time.Date(2026, 11, 26, 6, 0, 0, 0, digestLocation)
	sameDay := time.Date(2026, 11, 26, 20, 0, 0, 0, digestLocation)
	nextDay := time.Date(2026, 11, 27, 0, 0, 0, 0, digestLocation)

	if got, want := windowLabel(since, sameDay), "26 Nov 2026"; got != want {
		t.Errorf("same-day label = %q, want %q", got, want)
	}
	if got, want := windowedDigestBase(since, nextDay), "Daily Digest - 26 Nov 2026 to 27 Nov 2026"; got != want {
		t.Errorf("digest base = %q, want %q", got, want)
	}
	if got, want := windowedCollectionBase("Café — Tech", 3, since, nextDay),
		"Caf Tech - 3 articles - 26 Nov 2026 to 27 Nov 2026"; got != want {
		t.Errorf("collection base = %q, want %q", got, want)
	}
	if !asciiFilenameRE.MatchString(windowedCollectionBase("Café — Tech", 3, since, nextDay)) {
		t.Error("windowed collection base is not pure ASCII")
	}
}

func TestWithinWindowBoundaries(t *testing.T) {
	since := time.Date(2026, 11, 26, 0, 0, 0, 0, digestLocation)
	until := time.Date(2026, 11, 27, 0, 0, 0, 0, digestLocation)

	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		{"before", since.Add(-time.Second), false},
		{"at since", since, true},
		{"inside", since.Add(12 * time.Hour), true},
		{"just before until", until.Add(-time.Nanosecond), true},
		{"at until", until, false},
	}
	for _, tt := range tests {
		if got := withinWindow(tt.at, since, until); got != tt.want {
			t.Errorf("%s: withinWindow = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestSendGating(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.json")
	tests := []struct {
		name    string
		opts    options
		wantErr string
	}{
		{"send without feed or since", options{send: true}, "-send requires -feed"},
		{"until without since", options{until: "2026-11-26"}, "-until requires -since"},
		{"invalid since", options{since: "26 Nov 2026"}, "invalid time"},
		{"until before since", options{since: "2026-11-27", until: "2026-11-26"}, "is before -since"},
		{"send with feed passes gating", options{send: true, feed: "13"}, "reading config"},
		{"send with since passes gating", options{send: true, since: "2026-11-26"}, "reading config"},
		{"feed alone passes gating", options{feed: "13"}, "reading config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(missing, tt.opts)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("run error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}

	complete := &Config{
		KindleEmail: "reader@kindle.com", SMTPHost: "smtp.example.com", SMTPPort: 587,
		SMTPUsername: "u", SMTPPassword: "p", SMTPFrom: "f@example.com",
	}
	if !digestSendEnabled(complete, false, false) {
		t.Error("daily run should auto-send")
	}
	if digestSendEnabled(complete, true, false) {
		t.Error("windowed run must not send without -send")
	}
	if !digestSendEnabled(complete, true, true) {
		t.Error("windowed run should send with -send")
	}
	if digestSendEnabled(&Config{}, false, false) {
		t.Error("generate-only config must never send")
	}
}

func TestWindowedDigestRun(t *testing.T) {
	since := time.Date(2026, 11, 26, 0, 0, 0, 0, digestLocation)
	until := time.Date(2026, 11, 27, 0, 0, 0, 0, digestLocation)
	feedID := int64(42)

	entries := []apiEntry{
		{ID: 1, FeedID: feedID, Title: "Before", Content: "<p>before</p>", PublishedAt: since.Add(-time.Hour)},
		{ID: 2, FeedID: feedID, Title: "At since", Content: "<p>one</p>", PublishedAt: since},
		{ID: 3, FeedID: feedID, Title: "Inside", Content: "<p>two</p>", PublishedAt: since.Add(12 * time.Hour)},
		{ID: 4, FeedID: feedID, Title: "At until", Content: "<p>three</p>", PublishedAt: until},
		{ID: 5, FeedID: feedID, Title: "After", Content: "<p>after</p>", PublishedAt: until.Add(time.Hour)},
	}

	var gotAfter, gotBefore string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/feeds":
			_ = json.NewEncoder(w).Encode([]apiFeed{{ID: feedID, Title: "Example Feed"}})
		case "/v1/entries":
			gotAfter = r.URL.Query().Get("published_after")
			gotBefore = r.URL.Query().Get("published_before")
			_ = json.NewEncoder(w).Encode(entriesPage{Total: len(entries), Entries: entries})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	cfg := &Config{MinifluxURL: srv.URL, MinifluxAPIKey: "secret", OutputDir: t.TempDir(), MaxPartBytes: defaultMaxPartBytes}
	result, err := runDigest(cfg, &logger{}, time.Now(), options{sinceTime: since, untilTime: until})
	if err != nil {
		t.Fatalf("runDigest: %v", err)
	}
	if result.Entries != 2 {
		t.Errorf("entries = %d, want 2 (since inclusive, until exclusive)", result.Entries)
	}
	if result.Mode != "generate-only" {
		t.Errorf("mode = %q, want generate-only", result.Mode)
	}
	if gotAfter != strconv.FormatInt(since.Unix(), 10) || gotBefore != strconv.FormatInt(until.Unix(), 10) {
		t.Errorf("API window = [%s, %s), want [%d, %d)", gotAfter, gotBefore, since.Unix(), until.Unix())
	}

	date := time.Now().In(digestLocation).Format("2006-01-02")
	want := "Daily Digest - 26 Nov 2026 to 27 Nov 2026, Part 1.epub"
	files, _ := filepath.Glob(filepath.Join(cfg.OutputDir, date, "*.epub"))
	if len(files) != 1 {
		t.Fatalf("epub files = %v, want one", files)
	}
	if got := filepath.Base(files[0]); got != want {
		t.Errorf("filename = %q, want %q", got, want)
	}
	if got := epubTitle(t, files[0]); got != strings.TrimSuffix(want, ", Part 1.epub") {
		t.Errorf("dc:title = %q, want windowed title", got)
	}
}

func TestWindowedFeedRun(t *testing.T) {
	since := time.Date(2026, 11, 26, 0, 0, 0, 0, digestLocation)
	until := time.Date(2026, 11, 27, 0, 0, 0, 0, digestLocation)
	feedID := int64(13)

	entries := []apiEntry{
		{ID: 1, FeedID: feedID, Title: "Newest", Content: "<p>new</p>", PublishedAt: since.Add(2 * time.Hour)},
		{ID: 2, FeedID: feedID, Title: "Older", Content: "<p>old</p>", PublishedAt: since.Add(-2 * time.Hour)},
	}
	var gotAfter, gotBefore string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/feeds":
			_ = json.NewEncoder(w).Encode([]apiFeed{{ID: feedID, Title: "Rust Blog"}})
		case fmt.Sprintf("/v1/feeds/%d/entries", feedID):
			gotAfter = r.URL.Query().Get("published_after")
			gotBefore = r.URL.Query().Get("published_before")
			_ = json.NewEncoder(w).Encode(entriesPage{Total: len(entries), Entries: entries})
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	cfg := &Config{MinifluxURL: srv.URL, MinifluxAPIKey: "secret", OutputDir: t.TempDir(), MaxPartBytes: defaultMaxPartBytes}
	result, err := runCollection(cfg, &logger{}, time.Now(), options{
		feed: "13", limit: 5, sinceTime: since, untilTime: until,
	})
	if err != nil {
		t.Fatalf("runCollection: %v", err)
	}
	if result.Entries != 1 || result.Mode != "generate-only" {
		t.Errorf("result = %d entries, mode %q; want 1, generate-only", result.Entries, result.Mode)
	}
	if gotAfter != strconv.FormatInt(since.Unix(), 10) || gotBefore != strconv.FormatInt(until.Unix(), 10) {
		t.Errorf("API window = [%s, %s), want [%d, %d)", gotAfter, gotBefore, since.Unix(), until.Unix())
	}

	date := time.Now().In(digestLocation).Format("2006-01-02")
	want := "Rust Blog - 1 articles - 26 Nov 2026 to 27 Nov 2026, Part 1.epub"
	files, _ := filepath.Glob(filepath.Join(cfg.OutputDir, date, "*.epub"))
	if len(files) != 1 || filepath.Base(files[0]) != want {
		t.Errorf("epub files = %v, want %q", files, want)
	}
}

func TestRunListFeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/feeds" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]apiFeed{
			{ID: 1, Title: "Zeta"},
			{ID: 2, Title: "alpha"},
			{ID: 3, Title: "Beta"},
		})
	}))
	defer srv.Close()

	cfg := &Config{MinifluxURL: srv.URL, MinifluxAPIKey: "secret"}
	var buf bytes.Buffer
	if err := runListFeeds(cfg, &buf); err != nil {
		t.Fatalf("runListFeeds: %v", err)
	}
	want := "2\talpha\n3\tBeta\n1\tZeta\n"
	if buf.String() != want {
		t.Errorf("list-feeds output = %q, want %q", buf.String(), want)
	}
}

func TestDigestCSSTypography(t *testing.T) {
	for _, want := range []string{
		`"Bookerly", "Caecilia", Georgia, serif`,
		`"Amazon Ember", "Helvetica Neue", Arial, sans-serif`,
		`"Courier Prime", "Courier New", monospace`,
		"line-height: 1.5",
		"text-align: left",
	} {
		if !strings.Contains(digestCSS, want) {
			t.Errorf("CSS missing %q", want)
		}
	}
	if strings.Contains(digestCSS, "px") || strings.Contains(digestCSS, "pt") {
		// No absolute font sizes allowed; em is the only size unit used.
		for _, decl := range strings.Split(digestCSS, ";") {
			if !strings.Contains(decl, "font-size") {
				continue
			}
			value := strings.TrimSpace(strings.SplitN(decl, ":", 2)[1])
			if strings.HasSuffix(value, "px") || strings.HasSuffix(value, "pt") {
				t.Errorf("absolute font size: %q", strings.TrimSpace(decl))
			}
		}
	}
	if !strings.Contains(digestCSS, "font-size: 1em") {
		t.Error("body font-size must be 1em")
	}
}

func TestEPUBLanguageMetadata(t *testing.T) {
	entries := []*preparedEntry{makeTestEPUBEntry(t, "Lang", 0)}
	dst := filepath.Join(t.TempDir(), "lang.epub")
	if _, err := buildEPUB(context.Background(), entries, "Lang", "27 Nov 2026", dst); err != nil {
		t.Fatalf("buildEPUB: %v", err)
	}
	files := zipEntries(t, dst)
	for name, f := range files {
		if !strings.HasSuffix(name, ".opf") {
			continue
		}
		opf := readZipFile(t, f)
		if !strings.Contains(opf, "<dc:language>en</dc:language>") {
			t.Errorf("OPF does not set dc:language en:\n%s", opf)
		}
		return
	}
	t.Fatal("OPF missing from EPUB")
}
