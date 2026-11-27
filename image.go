package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"

	_ "image/gif"
	_ "image/png"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"
)

const (
	maxImageWidth = 800
	jpegQuality   = 75
	coverWidth    = 1200
	coverHeight   = 1600
)

// processImage decodes any supported image, flattens it onto white (which also
// removes any alpha channel and normalizes WebP), downscales it to at most
// maxImageWidth pixels wide and re-encodes it as a JPEG.
func processImage(data []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("decoding image: empty bounds %v", bounds)
	}

	nw, nh := w, h
	if w > maxImageWidth {
		nw = maxImageWidth
		nh = int(math.Round(float64(h) * float64(nw) / float64(w)))
		if nh < 1 {
			nh = 1
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, fmt.Errorf("encoding JPEG: %w", err)
	}
	return buf.Bytes(), nil
}

// generateCover renders a 1200x1600 near-black cover with light-grey text.
func generateCover(dateLabel string) ([]byte, error) {
	dst := image.NewRGBA(image.Rect(0, 0, coverWidth, coverHeight))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.RGBA{R: 18, G: 18, B: 18, A: 255}), image.Point{}, draw.Src)

	text := color.RGBA{R: 215, G: 215, B: 215, A: 255}
	drawCenteredText(dst, "DAILY DIGEST", 10, coverWidth/2, 680, text)
	drawCenteredText(dst, dateLabel, 6, coverWidth/2, 840, text)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("encoding cover JPEG: %w", err)
	}
	return buf.Bytes(), nil
}

// drawCenteredText draws text with the built-in bitmap font, scaled up by an
// integer factor and centered on (cx, cy).
func drawCenteredText(dst *image.RGBA, text string, scale, cx, cy int, c color.Color) {
	face := basicfont.Face7x13
	textWidth := font.MeasureString(face, text).Ceil()
	if textWidth == 0 {
		return
	}
	metrics := face.Metrics()
	textHeight := metrics.Height.Ceil()

	src := image.NewRGBA(image.Rect(0, 0, textWidth, textHeight))
	drawer := &font.Drawer{
		Dst:  src,
		Src:  image.NewUniform(c),
		Face: face,
		Dot:  fixed.P(0, metrics.Ascent.Ceil()),
	}
	drawer.DrawString(text)

	scaled := image.NewRGBA(image.Rect(0, 0, textWidth*scale, textHeight*scale))
	xdraw.NearestNeighbor.Scale(scaled, scaled.Bounds(), src, src.Bounds(), draw.Src, nil)

	at := image.Pt(cx-scaled.Bounds().Dx()/2, cy-scaled.Bounds().Dy()/2)
	draw.Draw(dst, image.Rectangle{Min: at, Max: at.Add(scaled.Bounds().Size())}, scaled, image.Point{}, draw.Over)
}
