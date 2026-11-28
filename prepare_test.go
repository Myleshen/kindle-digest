package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
)

func TestPrepareEntrySanitizesAndRewritesImages(t *testing.T) {
	var gotUA, gotReferer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/img.png" {
			http.NotFound(w, r)
			return
		}
		gotUA = r.Header.Get("User-Agent")
		gotReferer = r.Header.Get("Referer")
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(makeTestPNG(40, 20, true))
	}))
	defer srv.Close()

	content := `<div>
		<script>alert("xss")</script>
		<style>body{display:none}</style>
		<iframe src="https://evil.example"></iframe>
		<form><input name="q"></form>
		<nav>navigation</nav>
		<button>click</button>
		<svg><circle r="1"></circle></svg>
		<ins>inserted</ins>
		<p>hello <em>world</em></p>
		<img src="data:image/png;base64,AAAA">
		<img src="/relative.png">
		<img src="` + srv.URL + `/img.png">
	</div>`

	entry := apiEntry{
		ID:          9,
		FeedID:      42,
		Title:       "Sanitize me",
		URL:         "https://example.org/post",
		Author:      "Alice",
		Content:     content,
		PublishedAt: time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC),
	}
	feed := apiFeed{ID: 42, Title: "Example Feed", SiteURL: "https://example.org/feed"}

	e, err := prepareEntry(context.Background(), 0, entry, feed, defaultMaxPartBytes, newImageDownloader())
	if err != nil {
		t.Fatalf("prepareEntry: %v", err)
	}

	body := e.HTML()
	for _, banned := range []string{"<script", "alert(", "<iframe", "<nav", "<button", "<svg", "<ins", "<form", "data:image"} {
		if strings.Contains(body, banned) {
			t.Errorf("output still contains %q:\n%s", banned, body)
		}
	}
	if !strings.Contains(body, "hello") || !strings.Contains(body, "<em>world</em>") {
		t.Errorf("sanitizer dropped wanted markup:\n%s", body)
	}
	if got := strings.Count(body, "[image unavailable]"); got != 1 {
		t.Errorf("unavailable placeholders = %d, want 1:\n%s", got, body)
	}
	if len(e.images) != 1 {
		t.Fatalf("images = %d, want 1", len(e.images))
	}
	if e.images[0].Filename != "e1-i3.jpg" {
		t.Errorf("image filename = %q, want e1-i3.jpg", e.images[0].Filename)
	}
	if !strings.Contains(body, `src="../images/e1-i3.jpg"`) {
		t.Errorf("image src not rewritten:\n%s", body)
	}

	if gotReferer != "https://example.org" {
		t.Errorf("Referer = %q, want https://example.org", gotReferer)
	}
	if !strings.Contains(gotUA, "Chrome/") {
		t.Errorf("User-Agent = %q, want a Chrome UA", gotUA)
	}

	// The alpha PNG must have been flattened onto white and encoded as JPEG.
	if _, format, err := image.Decode(bytes.NewReader(e.images[0].Data)); err != nil {
		t.Fatalf("decoding processed image: %v", err)
	} else if format != "jpeg" {
		t.Errorf("processed image format = %q, want jpeg", format)
	}
}

func TestSanitizeDropsEmbeddedMedia(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		absent  []string
		present []string
	}{
		{
			name: "embedded media elements are removed with their subtrees",
			input: `<div>
				<video controls=""><source src="https://example.org/2.webm" type="video/webm"/>video fallback</video>
				<audio controls=""><source src="https://example.org/a.mp3" type="audio/mpeg"/>audio fallback</audio>
				<iframe src="https://evil.example">iframe fallback</iframe>
				<object data="https://example.org/x.swf">object fallback</object>
				<embed src="https://example.org/y.swf"/>
				<canvas>canvas fallback</canvas>
				<p>keep this</p>
			</div>`,
			absent: []string{
				"<video", "2.webm", "video fallback",
				"<audio", "a.mp3", "audio fallback",
				"<iframe", "iframe fallback",
				"<object", "object fallback",
				"<embed", "y.swf",
				"<canvas", "canvas fallback",
			},
			present: []string{"keep this", "<p>"},
		},
		{
			name:  "picture with source and img is kept",
			input: `<figure><picture><source srcset="https://example.org/p.webp" type="image/webp"><img src="https://example.org/p.png" alt="pic"></picture><figcaption>caption</figcaption></figure>`,
			present: []string{
				"<figure", "<picture", "<source", "p.webp", "<img", "p.png", "<figcaption", "caption",
			},
		},
		{
			name:    "video inside a paragraph leaves the paragraph and text intact",
			input:   `<p>before the clip <video controls=""><source src="https://example.org/v.webm" type="video/webm"/></video> after the clip</p>`,
			absent:  []string{"<video", "v.webm"},
			present: []string{"<p>", "before the clip", "after the clip"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := sanitizeFragment(t, tt.input)
			for _, banned := range tt.absent {
				if strings.Contains(body, banned) {
					t.Errorf("output still contains %q:\n%s", banned, body)
				}
			}
			for _, want := range tt.present {
				if !strings.Contains(body, want) {
					t.Errorf("output is missing %q:\n%s", want, body)
				}
			}
		})
	}
}

// sanitizeFragment runs the same parse-and-strip path prepareEntry uses.
func sanitizeFragment(t *testing.T, content string) string {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(content))
	if err != nil {
		t.Fatalf("parsing HTML: %v", err)
	}
	body := findElement(doc, "body")
	if body == nil {
		t.Fatal("parsed HTML has no body element")
	}
	stripElements(body)
	out, err := renderChildren(body)
	if err != nil {
		t.Fatalf("rendering HTML: %v", err)
	}
	return out
}

func TestProcessImageFlattensAlpha(t *testing.T) {
	out, err := processImage(makeTestPNG(100, 40, true))
	if err != nil {
		t.Fatalf("processImage: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decoding output: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("format = %q, want jpeg", format)
	}
	if got := img.Bounds().Dx(); got != 100 {
		t.Errorf("width = %d, want 100 (no upscale)", got)
	}

	r, g, b, _ := img.At(5, 20).RGBA()
	if r>>8 < 235 || g>>8 < 235 || b>>8 < 235 {
		t.Errorf("transparent area = (%d,%d,%d), want near white", r>>8, g>>8, b>>8)
	}
	r, g, b, _ = img.At(90, 20).RGBA()
	if r>>8 < 150 || g>>8 > 120 || b>>8 > 120 {
		t.Errorf("opaque area = (%d,%d,%d), want red", r>>8, g>>8, b>>8)
	}
}

func TestProcessImageDownscalesWebP(t *testing.T) {
	raw, err := os.ReadFile("testdata/alpha.webp")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	out, err := processImage(raw)
	if err != nil {
		t.Fatalf("processImage(webp): %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decoding output: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("format = %q, want jpeg", format)
	}
	if img.Bounds().Dx() != 64 || img.Bounds().Dy() != 48 {
		t.Errorf("bounds = %v, want 64x48", img.Bounds())
	}
	// Left half was transparent; it must be white now.
	r, g, b, _ := img.At(2, 2).RGBA()
	if r>>8 < 235 || g>>8 < 235 || b>>8 < 235 {
		t.Errorf("transparent area = (%d,%d,%d), want near white", r>>8, g>>8, b>>8)
	}
}

func TestProcessImageDownscalesTo800(t *testing.T) {
	out, err := processImage(makeTestPNG(1600, 800, false))
	if err != nil {
		t.Fatalf("processImage: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decoding output: %v", err)
	}
	if img.Bounds().Dx() != maxImageWidth || img.Bounds().Dy() != 400 {
		t.Errorf("bounds = %v, want 800x400", img.Bounds())
	}
}

func TestDropOversizedImages(t *testing.T) {
	images := []preparedImage{
		{Filename: "small.jpg", Data: make([]byte, 300_000)},
		{Filename: "large.jpg", Data: make([]byte, 1_500_000)},
		{Filename: "medium.jpg", Data: make([]byte, 1_000_000)},
	}
	kept, dropped := dropOversizedImages(images, 2_000_000)
	total := 0
	for _, img := range kept {
		total += len(img.Data)
	}
	if total > 2_000_000 {
		t.Errorf("kept %d bytes, want <= 2000000", total)
	}
	if len(dropped) != 1 || dropped[0] != "large.jpg" {
		t.Errorf("dropped = %v, want [large.jpg]", dropped)
	}
}

func TestTruncateHTML(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&b, "<p>paragraph %d with some text</p>", i)
	}
	long := b.String()
	limit := 50_000
	out := truncateHTML(long, limit)
	if len(out) > limit {
		t.Errorf("truncated length = %d, want <= %d", len(out), limit)
	}
	if !strings.HasSuffix(out, "<p><em>[content truncated]</em></p>") {
		t.Errorf("missing truncation note: %q", out[len(out)-100:])
	}
	if _, err := html.Parse(strings.NewReader(out)); err != nil {
		t.Errorf("truncated HTML does not parse: %v", err)
	}
	if got := truncateHTML("short", 100); got != "short" {
		t.Errorf("short HTML changed to %q", got)
	}
}

func TestShrinkToFitSingleEntry(t *testing.T) {
	html := strings.Repeat("<p>lots of text</p>", 400_000) // ~6.4 MB
	e := &preparedEntry{htmlBody: html}
	if !e.shrinkToFit(defaultMaxPartBytes) {
		t.Fatal("shrinkToFit = false, want true (text truncation)")
	}
	if len(e.htmlBody) > defaultMaxPartBytes {
		t.Errorf("html length = %d, want <= %d", len(e.htmlBody), defaultMaxPartBytes)
	}

	tiny := &preparedEntry{htmlBody: "<p>tiny</p>"}
	if tiny.shrinkToFit(defaultMaxPartBytes) {
		t.Error("shrinkToFit on tiny entry = true, want false")
	}
}

func TestShrinkToFitDropsImagesFirst(t *testing.T) {
	e := &preparedEntry{
		htmlBody: "<p>body</p>",
		images: []preparedImage{
			{Filename: "big.jpg", Data: make([]byte, 2_000_000)},
			{Filename: "small.jpg", Data: make([]byte, 1_000_000)},
		},
	}
	if !e.shrinkToFit(defaultMaxPartBytes) {
		t.Fatal("shrinkToFit = false, want true")
	}
	if len(e.images) != 1 || e.images[0].Filename != "small.jpg" {
		t.Errorf("images after shrink = %+v, want only small.jpg", e.images)
	}
}

func TestProcessImageRejectsGarbage(t *testing.T) {
	if _, err := processImage([]byte("not an image")); err == nil {
		t.Fatal("processImage accepted garbage")
	}
}

func TestGenerateCover(t *testing.T) {
	data, err := generateCover("27 Nov 2026")
	if err != nil {
		t.Fatalf("generateCover: %v", err)
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decoding cover: %v", err)
	}
	if format != "jpeg" {
		t.Errorf("format = %q, want jpeg", format)
	}
	if img.Bounds().Dx() != coverWidth || img.Bounds().Dy() != coverHeight {
		t.Errorf("cover bounds = %v, want %dx%d", img.Bounds(), coverWidth, coverHeight)
	}
	// Near-black background.
	r, g, b, _ := img.At(10, 10).RGBA()
	if r>>8 > 40 || g>>8 > 40 || b>>8 > 40 {
		t.Errorf("background = (%d,%d,%d), want near black", r>>8, g>>8, b>>8)
	}
}
