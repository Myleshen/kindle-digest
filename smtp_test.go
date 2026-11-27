package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDigestBodyListsPartsAndCounts(t *testing.T) {
	parts := []*part{
		{entries: make([]*preparedEntry, 2), path: "/out/Daily Digest - 27 Nov 2026, Part 1.epub"},
		{entries: make([]*preparedEntry, 1), path: "/out/Daily Digest - 27 Nov 2026, Part 2.epub"},
	}
	body := digestBody(parts, "27 Nov 2026")
	for _, want := range []string{
		"Daily Digest — 27 Nov 2026",
		"3 entries in 2 parts",
		"Part 1: Daily Digest - 27 Nov 2026, Part 1.epub (2 entries)",
		"Part 2: Daily Digest - 27 Nov 2026, Part 2.epub (1 entry)",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
}

func TestDigestMessageUsesASCIIAttachmentFilename(t *testing.T) {
	dir := t.TempDir()
	name := "Daily Digest - 27 Nov 2026, Part 1.epub"
	epubPath := filepath.Join(dir, name)
	if err := os.WriteFile(epubPath, []byte("epub-bytes"), 0o644); err != nil {
		t.Fatalf("writing attachment: %v", err)
	}

	cfg := &Config{
		KindleEmail: "reader@kindle.com",
		SMTPFrom:    "sender@example.org",
	}
	pt := &part{path: epubPath, entries: []*preparedEntry{{Title: "x"}}}
	msg, err := newDigestMessage(cfg, pt, "Daily Digest - 27 Nov 2026", "body")
	if err != nil {
		t.Fatalf("newDigestMessage: %v", err)
	}

	var buf bytes.Buffer
	if _, err := msg.WriteTo(&buf); err != nil {
		t.Fatalf("rendering message: %v", err)
	}
	raw := buf.String()

	if !strings.Contains(raw, "Content-Type: application/epub+zip") {
		t.Errorf("attachment content type missing:\n%s", firstLines(raw, 40))
	}
	if !strings.Contains(raw, `filename="`+name+`"`) {
		t.Errorf("plain ASCII filename missing from attachment header:\n%s", firstLines(raw, 40))
	}
	if strings.Contains(raw, "filename*=utf-8''") {
		t.Errorf("ASCII filename was RFC 2231 encoded:\n%s", firstLines(raw, 40))
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
