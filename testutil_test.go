package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// makeTestPNG returns a PNG where (when alpha is true) the left half is fully
// transparent and the right half is red.
func makeTestPNG(w, h int, alpha bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{R: 200, G: 30, B: 30, A: 255}
			if alpha && x < w/2 {
				c = color.NRGBA{}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func makeTestEPUBEntry(t *testing.T, title string, idx int) *preparedEntry {
	t.Helper()
	data, err := processImage(makeTestPNG(120, 80, false))
	if err != nil {
		t.Fatalf("processImage: %v", err)
	}
	filename := "e" + string(rune('1'+idx)) + "-i1.jpg"
	return &preparedEntry{
		Title:     title,
		FeedTitle: "Test Feed",
		Author:    "Tester",
		URL:       "https://example.org/" + title,
		htmlBody:  `<p>Body of ` + title + `.</p><img src="../images/` + filename + `"/>`,
		images: []preparedImage{
			{Filename: filename, Data: data},
		},
	}
}
