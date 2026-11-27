package main

import (
	"archive/zip"
	"bytes"
	"context"
	"image"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestBuildEPUBStructure(t *testing.T) {
	entries := []*preparedEntry{
		makeTestEPUBEntry(t, "First Post", 0),
		makeTestEPUBEntry(t, "Second Post", 1),
	}
	dst := filepath.Join(t.TempDir(), "digest.epub")
	size, err := buildEPUB(context.Background(), entries, "27 Nov 2026", dst)
	if err != nil {
		t.Fatalf("buildEPUB: %v", err)
	}
	if size <= 0 {
		t.Fatalf("size = %d, want > 0", size)
	}

	zr, err := zip.OpenReader(dst)
	if err != nil {
		t.Fatalf("opening EPUB: %v", err)
	}
	defer func() { _ = zr.Close() }()

	if len(zr.File) == 0 {
		t.Fatal("EPUB is empty")
	}
	first := zr.File[0]
	if first.Name != "mimetype" {
		t.Errorf("first zip entry = %q, want mimetype", first.Name)
	}
	if first.Method != zip.Store {
		t.Errorf("mimetype method = %d, want zip.Store (%d)", first.Method, zip.Store)
	}
	if first.CompressedSize64 != first.UncompressedSize64 {
		t.Errorf("mimetype compressed size %d != uncompressed size %d", first.CompressedSize64, first.UncompressedSize64)
	}
	if got := readZipFile(t, first); got != "application/epub+zip" {
		t.Errorf("mimetype contents = %q", got)
	}

	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	if _, ok := files["META-INF/container.xml"]; !ok {
		t.Error("META-INF/container.xml missing")
	}

	var opf *zip.File
	for name, f := range files {
		if strings.HasSuffix(name, ".opf") {
			opf = f
			break
		}
	}
	if opf == nil {
		t.Fatal("package .opf missing")
	}
	opfXML := readZipFile(t, opf)
	if !strings.Contains(opfXML, `properties="cover-image"`) {
		t.Errorf("OPF does not mark a cover image:\n%s", opfXML)
	}
	if !strings.Contains(opfXML, "<dc:creator") || !strings.Contains(opfXML, digestAuthor) {
		t.Errorf("OPF does not set dc:creator=%q", digestAuthor)
	}

	nav, ok := files["EPUB/nav.xhtml"]
	if !ok {
		t.Fatal("EPUB3 nav document EPUB/nav.xhtml missing")
	}
	navXML := readZipFile(t, nav)
	for _, title := range []string{"Test Feed: First Post", "Test Feed: Second Post"} {
		if !strings.Contains(navXML, title) {
			t.Errorf("nav document missing section title %q", title)
		}
	}

	// Every image referenced by section XHTML must exist in the archive.
	srcRe := regexp.MustCompile(`src="(\.\./images/[^"]+)"`)
	refs := 0
	for name, f := range files {
		if !strings.HasPrefix(name, "EPUB/xhtml/") || !strings.HasSuffix(name, ".xhtml") || name == "EPUB/xhtml/cover.xhtml" {
			continue
		}
		for _, m := range srcRe.FindAllStringSubmatch(readZipFile(t, f), -1) {
			refs++
			resolved := path.Join(path.Dir(name), m[1])
			if _, ok := files[resolved]; !ok {
				t.Errorf("%s references %s, which is missing from the EPUB", name, resolved)
			}
		}
	}
	if refs != len(entries) {
		t.Errorf("image references = %d, want %d", refs, len(entries))
	}

	cover, ok := files["EPUB/images/cover.jpg"]
	if !ok {
		t.Fatal("cover image EPUB/images/cover.jpg missing")
	}
	img, format, err := image.Decode(bytes.NewReader([]byte(readZipFile(t, cover))))
	if err != nil {
		t.Fatalf("decoding cover: %v", err)
	}
	if format != "jpeg" || img.Bounds().Dx() != coverWidth || img.Bounds().Dy() != coverHeight {
		t.Errorf("cover = %s %v, want jpeg %dx%d", format, img.Bounds(), coverWidth, coverHeight)
	}

	if size > hardMaxPartBytes {
		t.Errorf("EPUB size = %d, exceeds hard limit %d", size, hardMaxPartBytes)
	}
}

func readZipFile(t *testing.T, f *zip.File) string {
	t.Helper()
	rc, err := f.Open()
	if err != nil {
		t.Fatalf("opening %s: %v", f.Name, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading %s: %v", f.Name, err)
	}
	return string(data)
}
