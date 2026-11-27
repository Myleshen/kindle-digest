package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeEntry(title string, htmlBytes int, imageBytes ...int) *preparedEntry {
	e := &preparedEntry{Title: title, htmlBody: strings.Repeat("x", htmlBytes)}
	for i, n := range imageBytes {
		e.images = append(e.images, preparedImage{
			Filename: title + "-" + string(rune('a'+i)) + ".jpg",
			Data:     make([]byte, n),
		})
	}
	return e
}

func exactBuild(scale int64) func([]*preparedEntry, string) (int64, error) {
	return func(entries []*preparedEntry, _ string) (int64, error) {
		return int64(partEstimate(entries)) * scale, nil
	}
}

func TestEstimateIncludesOverheadAndImages(t *testing.T) {
	e := fakeEntry("a", 1000, 500, 250)
	if got, want := e.Estimate(), 1000+750+partOverhead; got != want {
		t.Errorf("Estimate = %d, want %d", got, want)
	}
}

func TestPackGreedyGroupsByEstimate(t *testing.T) {
	var entries []*preparedEntry
	for i := 0; i < 5; i++ {
		entries = append(entries, fakeEntry(string(rune('a'+i)), 4000))
	}
	// Each estimate is 24000; a part holds three.
	p := &packer{
		maxPart:   72000,
		dateLabel: "27 Nov 2026",
		build:     exactBuild(1),
		shrink:    func(*preparedEntry) bool { return false },
	}
	parts, err := p.pack(entries, t.TempDir())
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if len(parts[0].entries) != 3 || len(parts[1].entries) != 2 {
		t.Errorf("group sizes = %d,%d, want 3,2", len(parts[0].entries), len(parts[1].entries))
	}
	if parts[0].size != 72000 || parts[1].size != 48000 {
		t.Errorf("sizes = %d,%d, want 72000,48000", parts[0].size, parts[1].size)
	}
	want := "Daily Digest - 27 Nov 2026, Part 1.epub"
	if parts[0].path != filepath.Join(filepath.Dir(parts[0].path), want) {
		t.Errorf("part 1 path = %q, want filename %q", parts[0].path, want)
	}
	if _, err := os.Stat(parts[0].path); err != nil {
		t.Errorf("part 1 file missing: %v", err)
	}
}

func TestPackRepairsOverflowBySplitting(t *testing.T) {
	entries := []*preparedEntry{fakeEntry("a", 0), fakeEntry("b", 0)}
	// Two entries estimate 40000; scale 200 writes 8 MB, over the hard limit.
	// Splitting leaves two 4 MB parts.
	buildCalls := 0
	p := &packer{
		maxPart:   10_000_000,
		dateLabel: "27 Nov 2026",
		build: func(es []*preparedEntry, _ string) (int64, error) {
			buildCalls++
			return exactBuild(200)(es, "")
		},
		shrink: func(*preparedEntry) bool {
			t.Error("shrink called for a multi-entry part")
			return false
		},
	}
	parts, err := p.pack(entries, t.TempDir())
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2 after split", len(parts))
	}
	if buildCalls != 3 {
		t.Errorf("build calls = %d, want 3 (oversized, rewritten first part, new part)", buildCalls)
	}
	for i, pt := range parts {
		if len(pt.entries) != 1 {
			t.Errorf("part %d has %d entries, want 1", i+1, len(pt.entries))
		}
		if pt.size != 4_000_000 {
			t.Errorf("part %d size = %d, want 4000000", i+1, pt.size)
		}
		if pt.size > hardMaxPartBytes {
			t.Errorf("part %d exceeds hard limit", i+1)
		}
	}
}

func TestPackShrinksSingleOversizedEntry(t *testing.T) {
	entry := fakeEntry("huge", 2_500_000, 3_000_000)
	shrinkCalls := 0
	p := &packer{
		maxPart:   defaultMaxPartBytes,
		dateLabel: "27 Nov 2026",
		build:     exactBuild(1),
		shrink: func(e *preparedEntry) bool {
			shrinkCalls++
			if len(e.images) == 0 {
				return false
			}
			e.images = nil
			return true
		},
	}
	parts, err := p.pack([]*preparedEntry{entry}, t.TempDir())
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(parts))
	}
	if shrinkCalls != 1 {
		t.Errorf("shrink calls = %d, want 1", shrinkCalls)
	}
	if parts[0].size != int64(2_500_000+partOverhead) {
		t.Errorf("size = %d, want %d", parts[0].size, 2_500_000+partOverhead)
	}
	if parts[0].size > hardMaxPartBytes {
		t.Error("part still exceeds hard limit")
	}
}

func TestPackFailsWhenEntryCannotShrink(t *testing.T) {
	entry := fakeEntry("huge", 6_000_000)
	p := &packer{
		maxPart:   defaultMaxPartBytes,
		dateLabel: "27 Nov 2026",
		build:     exactBuild(1),
		shrink:    func(*preparedEntry) bool { return false },
	}
	if _, err := p.pack([]*preparedEntry{entry}, t.TempDir()); err == nil {
		t.Fatal("pack succeeded for an unshrinkable oversized entry")
	}
}
