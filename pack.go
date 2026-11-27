package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// part is one EPUB file plus the entries it contains.
type part struct {
	entries []*preparedEntry
	tmpPath string
	path    string
	size    int64
}

func partEstimate(entries []*preparedEntry) int {
	total := 0
	for _, e := range entries {
		total += e.Estimate()
	}
	return total
}

// feedCutEnd pulls a greedy cut back to the start of the feed it would split,
// but only if the shortened part stays at least half full. A feed that alone
// exceeds the cap still gets cut mid-feed.
func feedCutEnd(entries []*preparedEntry, start, end, maxPart int) int {
	if end >= len(entries) || entries[end].FeedTitle != entries[end-1].FeedTitle {
		return end
	}
	cut := end - 1
	for cut > start && entries[cut].FeedTitle == entries[cut-1].FeedTitle {
		cut--
	}
	if cut > start && partEstimate(entries[start:cut])*2 >= maxPart {
		return cut
	}
	return end
}

// packer greedily groups entries, then verifies each written EPUB against the
// hard size limit and repairs overflowing parts by splitting or shrinking.
type packer struct {
	maxPart  int
	baseName string
	build    func(entries []*preparedEntry, dst string) (int64, error)
	shrink   func(*preparedEntry) bool
}

func (p *packer) pack(entries []*preparedEntry, dir string) (parts []*part, err error) {
	defer func() {
		if err != nil {
			for _, pt := range parts {
				if pt != nil && pt.tmpPath != "" {
					_ = os.Remove(pt.tmpPath)
				}
			}
		}
	}()

	start := 0
	for start < len(entries) {
		end := start
		total := 0
		for end < len(entries) && total+entries[end].Estimate() <= p.maxPart {
			total += entries[end].Estimate()
			end++
		}
		if end == start {
			end = start + 1
		}
		end = feedCutEnd(entries, start, end, p.maxPart)
		parts = append(parts, &part{entries: entries[start:end]})
		start = end
	}

	for i := 0; i < len(parts); i++ {
		for {
			pt := parts[i]
			if pt.tmpPath == "" {
				f, createErr := os.CreateTemp(dir, ".part-*.epub")
				if createErr != nil {
					return nil, fmt.Errorf("creating part file: %w", createErr)
				}
				pt.tmpPath = f.Name()
				if closeErr := f.Close(); closeErr != nil {
					return nil, fmt.Errorf("closing part file: %w", closeErr)
				}
			}

			size, buildErr := p.build(pt.entries, pt.tmpPath)
			if buildErr != nil {
				return nil, fmt.Errorf("building part %d: %w", i+1, buildErr)
			}
			if size <= int64(hardMaxPartBytes) {
				pt.size = size
				break
			}

			if len(pt.entries) == 1 {
				if !p.shrink(pt.entries[0]) {
					return nil, fmt.Errorf("part %d: entry %q cannot be reduced below %d bytes",
						i+1, pt.entries[0].Title, hardMaxPartBytes)
				}
				continue
			}

			last := pt.entries[len(pt.entries)-1]
			pt.entries = pt.entries[:len(pt.entries)-1]
			parts = slices.Insert(parts, i+1, &part{entries: []*preparedEntry{last}})
		}
	}

	for i, pt := range parts {
		pt.path = filepath.Join(dir, fmt.Sprintf("%s, Part %d.epub", p.baseName, i+1))
		if err := os.Rename(pt.tmpPath, pt.path); err != nil {
			return nil, fmt.Errorf("naming part %d: %w", i+1, err)
		}
		pt.tmpPath = ""
	}
	return parts, nil
}
