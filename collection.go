package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultCollectionLimit = 50
	maxCollectionLimit     = 500
)

// clampLimit keeps -limit inside the documented 1..500 range.
func clampLimit(n int) int {
	if n < 1 {
		return 1
	}
	if n > maxCollectionLimit {
		return maxCollectionLimit
	}
	return n
}

// resolveFeed maps -feed to exactly one feed: a numeric id, an exact
// case-insensitive title, or a unique title substring.
func resolveFeed(feeds map[int64]apiFeed, query string) (apiFeed, error) {
	q := strings.TrimSpace(query)
	if id, err := strconv.ParseInt(q, 10, 64); err == nil {
		feed, ok := feeds[id]
		if !ok {
			return apiFeed{}, fmt.Errorf("feed id %d not found; available feeds: %s",
				id, joinFeedTitles(feedsSortedByTitle(feeds)))
		}
		return feed, nil
	}

	needle := strings.ToLower(q)
	var exact, partial []apiFeed
	for _, feed := range feeds {
		title := strings.ToLower(feed.Title)
		switch {
		case title == needle:
			exact = append(exact, feed)
		case strings.Contains(title, needle):
			partial = append(partial, feed)
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Title < matches[j].Title })

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return apiFeed{}, fmt.Errorf("no feed matches %q; available feeds: %s",
			q, joinFeedTitles(feedsSortedByTitle(feeds)))
	default:
		return apiFeed{}, fmt.Errorf("%q matches %d feeds: %s; refine the query",
			q, len(matches), joinFeedTitles(matches))
	}
}

func feedsSortedByTitle(feeds map[int64]apiFeed) []apiFeed {
	list := make([]apiFeed, 0, len(feeds))
	for _, feed := range feeds {
		list = append(list, feed)
	}
	sort.Slice(list, func(i, j int) bool { return feedTitlesLower(list[i], list[j]) })
	return list
}

func joinFeedTitles(feeds []apiFeed) string {
	titles := make([]string, len(feeds))
	for i, feed := range feeds {
		titles[i] = strconv.Quote(feed.Title)
	}
	return strings.Join(titles, ", ")
}

// asciiFilename keeps only [A-Za-z0-9 ._-], turning every other run of
// characters into a single space. Attachment filenames become Kindle library
// titles, and this project deliberately keeps those pure ASCII.
func asciiFilename(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		allowed := r == ' ' || r == '.' || r == '_' || r == '-' ||
			('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
		if !allowed {
			if !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		}
		if r == ' ' && space {
			continue
		}
		b.WriteRune(r)
		space = r == ' '
	}
	return strings.TrimSpace(b.String())
}

// collectionBaseName is both the EPUB dc:title and the filename stem.
func collectionBaseName(feedTitle string, articles int, date string) string {
	title := asciiFilename(feedTitle)
	if title == "" {
		title = "Feed"
	}
	return fmt.Sprintf("%s - %d articles - %s", title, articles, date)
}

// runCollection exports one feed, either its newest entries or those inside an
// explicit window. It sends only when opts.send is set.
func runCollection(cfg *Config, log *logger, started time.Time, opts options) (*runResult, error) {
	if opts.send && !cfg.sendMode() {
		return nil, fmt.Errorf("-send requires kindle_email and a complete SMTP block in the config")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	limit := clampLimit(opts.limit)
	now := time.Now().In(digestLocation)
	date := now.Format("2006-01-02")
	windowed := !opts.sinceTime.IsZero()
	since, until := opts.sinceTime, opts.untilTime
	if windowed && until.IsZero() {
		until = now
	}

	client := newMinifluxClient(cfg.MinifluxURL, cfg.MinifluxAPIKey)
	feeds, err := client.fetchFeeds(ctx)
	if err != nil {
		return nil, err
	}
	feed, err := resolveFeed(feeds, opts.feed)
	if err != nil {
		return nil, err
	}

	var entries []apiEntry
	if windowed {
		entries, err = client.fetchFeedEntriesWindow(ctx, feed.ID, limit, since, until)
	} else {
		entries, err = client.fetchFeedEntries(ctx, feed.ID, limit)
	}
	if err != nil {
		return nil, err
	}
	if windowed {
		filtered := entries[:0]
		for _, e := range entries {
			if withinWindow(e.PublishedAt, since, until) {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	if len(entries) == 0 {
		if windowed {
			return nil, fmt.Errorf("feed %q has no entries in the window", feed.Title)
		}
		return nil, fmt.Errorf("feed %q has no entries", feed.Title)
	}
	log.info("collection: feed %q (id %d), limit=%d, fetched=%d entries",
		feed.Title, feed.ID, limit, len(entries))

	// The API returns newest first; pack oldest first so the book reads in order.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].PublishedAt.Before(entries[j].PublishedAt)
	})

	downloads := newImageDownloader()
	prepared := make([]*preparedEntry, 0, len(entries))
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e, err := prepareEntry(ctx, i, entry, feeds[entry.FeedID], cfg.MaxPartBytes, downloads)
		if err != nil {
			log.error("skipping entry %d (%q): %v", entry.ID, entry.Title, err)
			continue
		}
		prepared = append(prepared, e)
	}
	if len(prepared) == 0 {
		return nil, fmt.Errorf("no entries could be prepared")
	}

	base := collectionBaseName(feed.Title, len(prepared), date)
	if windowed {
		base = windowedCollectionBase(feed.Title, len(prepared), since, until)
	}
	outDir := filepath.Join(cfg.OutputDir, date)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating output directory %s: %w", outDir, err)
	}

	p := &packer{
		maxPart:  cfg.MaxPartBytes,
		baseName: base,
		build: func(entries []*preparedEntry, dst string) (int64, error) {
			return buildEPUB(ctx, entries, base, date, dst)
		},
		shrink: func(e *preparedEntry) bool {
			return e.shrinkToFit(cfg.MaxPartBytes)
		},
	}
	parts, err := p.pack(prepared, outDir)
	if err != nil {
		return nil, fmt.Errorf("packing collection: %w", err)
	}

	var totalBytes int64
	for i, pt := range parts {
		totalBytes += pt.size
		log.info("wrote part %d/%d: %s (%d bytes, %d entries)",
			i+1, len(parts), filepath.Base(pt.path), pt.size, len(pt.entries))
	}

	result := &runResult{
		Entries:    len(prepared),
		Parts:      parts,
		TotalBytes: totalBytes,
		Mode:       "generate-only",
		Duration:   time.Since(started).Round(time.Millisecond),
		Windowed:   windowed,
		Title:      base,
	}
	if opts.send && !opts.generateOnly {
		result.Mode = "send"
		m := &mailer{cfg: cfg, log: log, interval: sendInterval}
		if err := m.send(ctx, parts, base); err != nil {
			return result, err
		}
	} else {
		log.info("send skipped: collection mode is generate-only (pass -send to email)")
	}

	log.info("summary: feed=%q limit=%d entries=%d parts=%d bytes=%d mode=%s duration=%s",
		feed.Title, limit, len(prepared), len(parts), totalBytes, result.Mode, result.Duration)
	return result, nil
}
