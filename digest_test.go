package main

import (
	"archive/zip"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestSortEntriesByFeed(t *testing.T) {
	feeds := map[int64]apiFeed{
		1: {ID: 1, Title: "Zeta"},
		2: {ID: 2, Title: "alpha"},
		3: {ID: 3, Title: "Beta"},
	}
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, digestLocation)
	entries := []apiEntry{
		{FeedID: 1, Title: "z-old", PublishedAt: base},
		{FeedID: 2, Title: "a-old", PublishedAt: base},
		{FeedID: 1, Title: "z-new", PublishedAt: base.Add(time.Hour)},
		{FeedID: 3, Title: "b-only", PublishedAt: base},
		{FeedID: 2, Title: "a-new", PublishedAt: base.Add(time.Hour)},
	}

	sortEntriesByFeed(entries, feeds)

	got := make([]string, len(entries))
	for i, e := range entries {
		got[i] = e.Title
	}
	want := []string{"a-new", "a-old", "b-only", "z-new", "z-old"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestBuildDigestEPUBLayout(t *testing.T) {
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, digestLocation)
	entries := []*preparedEntry{
		{Title: "Alpha newer", FeedTitle: "Alpha Feed", Published: base.Add(24 * time.Hour), htmlBody: "<p>alpha new</p>"},
		{Title: "Alpha older", FeedTitle: "Alpha Feed", Published: base, htmlBody: "<p>alpha old</p>"},
		{Title: "Beta only", FeedTitle: "Beta Feed", Published: base.Add(48 * time.Hour), htmlBody: "<p>beta</p>"},
	}
	dst := filepath.Join(t.TempDir(), "digest.epub")
	if _, err := buildDigestEPUB(context.Background(), entries, "Daily Digest - 21 Nov 2026", "21 Nov 2026", dst); err != nil {
		t.Fatalf("buildDigestEPUB: %v", err)
	}

	zr, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatalf("opening EPUB: %v", err)
	}
	defer func() { _ = zr.Close() }()

	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}

	contentsName := "EPUB/xhtml/contents.xhtml"
	contents, ok := files[contentsName]
	if !ok {
		t.Fatal("contents page EPUB/xhtml/contents.xhtml missing")
	}
	contentsXML := readZipFile(t, contents)
	for _, want := range []string{"Alpha Feed — 2 articles", "Beta Feed — 1 article"} {
		if !strings.Contains(contentsXML, want) {
			t.Errorf("contents page missing %q:\n%s", want, contentsXML)
		}
	}
	if strings.Index(contentsXML, "Alpha Feed") > strings.Index(contentsXML, "Beta Feed") {
		t.Error("contents page feeds are not alphabetical")
	}

	linkRe := regexp.MustCompile(`href="([^"]+\.xhtml)"`)
	links := linkRe.FindAllStringSubmatch(contentsXML, -1)
	if len(links) != len(entries)-1 {
		t.Errorf("contents links = %d, want %d", len(links), len(entries)-1)
	}
	for _, m := range links {
		target := path.Join(path.Dir(contentsName), m[1])
		if _, ok := files[target]; !ok {
			t.Errorf("contents link %q resolves to %q, which is missing", m[1], target)
		}
	}

	var separators []string
	for name := range files {
		if strings.HasPrefix(name, "EPUB/xhtml/feed") && strings.HasSuffix(name, ".xhtml") {
			separators = append(separators, name)
		}
	}
	sort.Strings(separators)
	if len(separators) != 2 {
		t.Fatalf("separator pages = %d, want 2: %v", len(separators), separators)
	}
	wantSeparators := []struct{ heading, meta string }{
		{"<h1>Alpha Feed</h1>", "2 articles, 20 Nov 2026 - 21 Nov 2026"},
		{"<h1>Beta Feed</h1>", "1 article, 22 Nov 2026 - 22 Nov 2026"},
	}
	for i, want := range wantSeparators {
		body := readZipFile(t, files[separators[i]])
		if !strings.Contains(body, want.heading) || !strings.Contains(body, want.meta) {
			t.Errorf("separator %s = %q, want heading %q and meta %q",
				separators[i], body, want.heading, want.meta)
		}
		for _, r := range body {
			if r > 127 {
				t.Errorf("separator %s is not ASCII-only: %q", separators[i], body)
				break
			}
		}
	}

	for i := 1; i <= len(entries); i++ {
		name := fmt.Sprintf("EPUB/xhtml/art%04d.xhtml", i)
		if _, ok := files[name]; !ok {
			t.Errorf("article page %s missing", name)
		}
	}

	navXML := readZipFile(t, files["EPUB/nav.xhtml"])
	readingOrder := []string{"xhtml/contents.xhtml", "xhtml/index.xhtml", "xhtml/feed0001.xhtml", "xhtml/art0001.xhtml"}
	for i := 1; i < len(readingOrder); i++ {
		prev, cur := strings.Index(navXML, readingOrder[i-1]), strings.Index(navXML, readingOrder[i])
		if prev < 0 || cur < 0 {
			t.Fatalf("nav.xhtml missing %q or %q", readingOrder[i-1], readingOrder[i])
		}
		if prev >= cur {
			t.Errorf("nav order: %s should come before %s", readingOrder[i-1], readingOrder[i])
		}
	}

	var opfXML string
	for name, f := range files {
		if strings.HasSuffix(name, ".opf") {
			opfXML = readZipFile(t, f)
			break
		}
	}
	if !strings.Contains(opfXML, `properties="cover-image"`) {
		t.Error("OPF does not mark a cover image")
	}
	if !strings.Contains(opfXML, digestAuthor) {
		t.Errorf("OPF does not set dc:creator=%q", digestAuthor)
	}
	for _, name := range []string{contentsName, "EPUB/xhtml/feed0001.xhtml", "EPUB/xhtml/feed0002.xhtml"} {
		if _, ok := files[name]; !ok {
			t.Errorf("%s missing from zip", name)
		}
	}
}

func TestPackFeedAwareCut(t *testing.T) {
	pack := func(t *testing.T, maxPart int, entries []*preparedEntry) [][]*preparedEntry {
		t.Helper()
		p := &packer{
			maxPart:  maxPart,
			baseName: "Daily Digest - 27 Nov 2026",
			build:    exactBuild(1),
			shrink:   func(*preparedEntry) bool { return false },
		}
		parts, err := p.pack(entries, t.TempDir())
		if err != nil {
			t.Fatalf("pack: %v", err)
		}
		got := make([][]*preparedEntry, len(parts))
		for i, pt := range parts {
			got[i] = pt.entries
		}
		return got
	}

	t.Run("backs off to the feed boundary", func(t *testing.T) {
		entries := []*preparedEntry{
			feedFake("a1", "A", 10_000),
			feedFake("a2", "A", 10_000),
			feedFake("b1", "B", 10_000),
			feedFake("b2", "B", 10_000),
		}
		parts := pack(t, 100_000, entries)
		if len(parts) != 2 {
			t.Fatalf("parts = %d, want 2", len(parts))
		}
		if len(parts[0]) != 2 || parts[0][0].FeedTitle != "A" || parts[0][1].FeedTitle != "A" {
			t.Errorf("part 1 should hold both A articles, got %d entries", len(parts[0]))
		}
		if len(parts[1]) != 2 || parts[1][0].FeedTitle != "B" {
			t.Errorf("part 2 should start at feed B, got %d entries", len(parts[1]))
		}
	})

	t.Run("keeps the greedy cut when the boundary is under half full", func(t *testing.T) {
		entries := []*preparedEntry{
			feedFake("a1", "A", 10_000),
			feedFake("b1", "B", 10_000),
			feedFake("b2", "B", 10_000),
			feedFake("b3", "B", 10_000),
		}
		parts := pack(t, 100_000, entries)
		if len(parts) != 2 {
			t.Fatalf("parts = %d, want 2", len(parts))
		}
		if len(parts[0]) != 3 {
			t.Errorf("part 1 = %d entries, want 3 (greedy cut kept)", len(parts[0]))
		}
	})

	t.Run("single feed over the cap still cuts mid-feed", func(t *testing.T) {
		entries := []*preparedEntry{
			feedFake("a1", "A", 10_000),
			feedFake("a2", "A", 10_000),
			feedFake("a3", "A", 10_000),
			feedFake("a4", "A", 10_000),
		}
		parts := pack(t, 100_000, entries)
		if len(parts) != 2 {
			t.Fatalf("parts = %d, want 2", len(parts))
		}
		if len(parts[0]) != 3 || len(parts[1]) != 1 {
			t.Errorf("part sizes = %d,%d, want 3,1", len(parts[0]), len(parts[1]))
		}
	})

	t.Run("cut already on a boundary is unchanged", func(t *testing.T) {
		entries := []*preparedEntry{
			feedFake("a1", "A", 30_000),
			feedFake("a2", "A", 30_000),
			feedFake("b1", "B", 30_000),
			feedFake("b2", "B", 30_000),
		}
		parts := pack(t, 100_000, entries)
		if len(parts) != 2 {
			t.Fatalf("parts = %d, want 2", len(parts))
		}
		if len(parts[0]) != 2 || len(parts[1]) != 2 {
			t.Errorf("part sizes = %d,%d, want 2,2", len(parts[0]), len(parts[1]))
		}
	})
}

func TestBuildIndexPage(t *testing.T) {
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, digestLocation)
	entries := []*preparedEntry{
		{Title: "beta", FeedTitle: "Zeta Feed", Published: base, htmlBody: "<p>beta</p>"},
		{Title: "Apple", FeedTitle: "The Verge", Published: base, htmlBody: "<p>apple</p>"},
		{Title: "123 tips", FeedTitle: "Feed 3", Published: base, htmlBody: "<p>digits</p>"},
		{Title: "alpha", FeedTitle: "The Verge", Published: base, htmlBody: "<p>alpha</p>"},
	}
	dst := filepath.Join(t.TempDir(), "digest.epub")
	if _, err := buildDigestEPUB(context.Background(), entries, "Daily Digest - 21 Nov 2026", "21 Nov 2026", dst); err != nil {
		t.Fatalf("buildDigestEPUB: %v", err)
	}

	files := zipEntries(t, dst)
	index, ok := files["EPUB/xhtml/index.xhtml"]
	if !ok {
		t.Fatal("index page EPUB/xhtml/index.xhtml missing")
	}
	indexXML := readZipFile(t, index)

	if got := strings.Count(indexXML, "<li>"); got != len(entries) {
		t.Errorf("index entries = %d, want %d", got, len(entries))
	}
	positions := make([]int, len(entries))
	for i, title := range []string{"123 tips", "alpha", "Apple", "beta"} {
		positions[i] = strings.Index(indexXML, ">"+title+"</a>")
		if positions[i] < 0 {
			t.Fatalf("index missing title %q:\n%s", title, indexXML)
		}
	}
	for i := 1; i < len(positions); i++ {
		if positions[i-1] >= positions[i] {
			t.Errorf("index order is not case-insensitive alphabetical:\n%s", indexXML)
			break
		}
	}
	if strings.Index(indexXML, "<h2>#</h2>") > strings.Index(indexXML, "<h2>A</h2>") {
		t.Error("'#' group should come before the letter groups")
	}
	for _, h := range []string{"#", "A", "B"} {
		if !strings.Contains(indexXML, "<h2>"+h+"</h2>") {
			t.Errorf("index is missing heading %q", h)
		}
	}
	for _, h := range []string{"C", "D", "Z"} {
		if strings.Contains(indexXML, "<h2>"+h+"</h2>") {
			t.Errorf("index has empty heading %q", h)
		}
	}
	for _, attribution := range []string{"— The Verge", "— Zeta Feed", "— Feed 3"} {
		if !strings.Contains(indexXML, attribution) {
			t.Errorf("index missing attribution %q", attribution)
		}
	}
	if !strings.Contains(indexXML, `class="index-feed"`) {
		t.Error("index attribution is not styled")
	}

	linkRe := regexp.MustCompile(`href="([^"]+\.xhtml)"`)
	links := linkRe.FindAllStringSubmatch(indexXML, -1)
	if len(links) != len(entries) {
		t.Errorf("index links = %d, want %d", len(links), len(entries))
	}
	for _, m := range links {
		target := path.Join("EPUB/xhtml", m[1])
		if _, ok := files[target]; !ok {
			t.Errorf("index link %q resolves to %q, which is missing", m[1], target)
		}
	}
}

func TestCollectionEPUBHasIndexOnly(t *testing.T) {
	base := time.Date(2026, 11, 20, 9, 0, 0, 0, digestLocation)
	entries := []*preparedEntry{
		{Title: "Zebra", FeedTitle: "One Feed", Published: base, htmlBody: "<p>z</p>"},
		{Title: "apple", FeedTitle: "One Feed", Published: base, htmlBody: "<p>a</p>"},
	}
	dst := filepath.Join(t.TempDir(), "collection.epub")
	if _, err := buildEPUB(context.Background(), entries, "One Feed - 2 articles - 2026-11-20", "20 Nov 2026", dst); err != nil {
		t.Fatalf("buildEPUB: %v", err)
	}

	files := zipEntries(t, dst)
	index, ok := files["EPUB/xhtml/index.xhtml"]
	if !ok {
		t.Fatal("collection EPUB is missing the index page")
	}
	if _, ok := files["EPUB/xhtml/contents.xhtml"]; ok {
		t.Error("collection EPUB should not have a contents page")
	}
	for name := range files {
		if strings.HasPrefix(name, "EPUB/xhtml/feed") && strings.HasSuffix(name, ".xhtml") {
			t.Errorf("collection EPUB should not have separator page %s", name)
		}
	}
	indexXML := readZipFile(t, index)
	if strings.Index(indexXML, ">apple</a>") > strings.Index(indexXML, ">Zebra</a>") {
		t.Error("collection index is not title-sorted")
	}
	linkRe := regexp.MustCompile(`href="([^"]+\.xhtml)"`)
	for _, m := range linkRe.FindAllStringSubmatch(indexXML, -1) {
		if _, ok := files[path.Join("EPUB/xhtml", m[1])]; !ok {
			t.Errorf("collection index link %q is missing from the zip", m[1])
		}
	}
	for i := 1; i <= len(entries); i++ {
		if _, ok := files[fmt.Sprintf("EPUB/xhtml/art%04d.xhtml", i)]; !ok {
			t.Errorf("collection article page art%04d.xhtml missing", i)
		}
	}
}

// zipEntries opens an EPUB and returns its files by name.
func zipEntries(t *testing.T, dst string) map[string]*zip.File {
	t.Helper()
	zr, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatalf("opening EPUB: %v", err)
	}
	t.Cleanup(func() { _ = zr.Close() })
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	return files
}

func feedFake(title, feed string, htmlBytes int) *preparedEntry {
	e := fakeEntry(title, htmlBytes)
	e.FeedTitle = feed
	return e
}
