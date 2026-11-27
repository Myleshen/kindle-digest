package main

import (
	"context"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmaupin/go-epub"
)

const (
	digestAuthor = "Daily Digest"
	bylineLayout = "2 Jan 2006 15:04 MST"
)

const digestCSS = `body { line-height: 1.5; }
img { max-width: 100%; height: auto; page-break-inside: avoid; }
figure { page-break-inside: avoid; }
p { orphans: 2; widows: 2; }
a { color: #000; text-decoration: underline; }
pre { white-space: pre-wrap; }
`

// buildEPUB writes one digest part and returns its byte size.
func buildEPUB(ctx context.Context, entries []*preparedEntry, dateLabel, dst string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	tmpDir, err := os.MkdirTemp("", "kindle-digest-epub-")
	if err != nil {
		return 0, fmt.Errorf("creating temp directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	book := epub.NewEpub("Daily Digest - " + dateLabel)
	book.SetAuthor(digestAuthor)

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

	for i, e := range entries {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		for _, img := range e.images {
			imgPath := filepath.Join(tmpDir, img.Filename)
			if err := os.WriteFile(imgPath, img.Data, 0o644); err != nil {
				return 0, fmt.Errorf("writing image %s: %w", img.Filename, err)
			}
			href, err := book.AddImage(imgPath, img.Filename)
			if err != nil {
				return 0, fmt.Errorf("adding image %s: %w", img.Filename, err)
			}
			if want := "../images/" + img.Filename; href != want {
				return 0, fmt.Errorf("image %s has href %q, want %q", img.Filename, href, want)
			}
		}

		title := e.Title
		if title == "" {
			title = "(untitled)"
		}
		if _, err := book.AddSection(sectionHTML(e), e.FeedTitle+": "+title, "", cssHref); err != nil {
			return 0, fmt.Errorf("adding section %d: %w", i+1, err)
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

func sectionHTML(e *preparedEntry) string {
	title := e.Title
	if title == "" {
		title = "(untitled)"
	}
	return "<h1>" + html.EscapeString(title) + "</h1>\n" + bylineHTML(e) + "\n" + e.HTML()
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
