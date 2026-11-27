package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// parseWindowTime accepts RFC3339, 2006-01-02T15:04 and 2006-01-02, with the
// latter two read in Asia/Kolkata; a bare date means midnight.
func parseWindowTime(value string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t.In(digestLocation), nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, digestLocation); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid time %q: use RFC3339, 2006-01-02T15:04 or 2006-01-02", value)
}

// withinWindow reports whether t is in [since, until).
func withinWindow(t, since, until time.Time) bool {
	return !t.Before(since) && t.Before(until)
}

// windowLabel is the ASCII date span used in on-demand titles, collapsed to
// one date when the window sits inside one day.
func windowLabel(since, until time.Time) string {
	a := since.In(digestLocation).Format("2 Jan 2006")
	b := until.In(digestLocation).Format("2 Jan 2006")
	if a == b {
		return a
	}
	return a + " to " + b
}

// windowedDigestBase is the dc:title and filename stem for an all-feeds run.
func windowedDigestBase(since, until time.Time) string {
	return "Daily Digest - " + windowLabel(since, until)
}

// windowedCollectionBase is the dc:title and filename stem for a feed run.
func windowedCollectionBase(feedTitle string, articles int, since, until time.Time) string {
	title := asciiFilename(feedTitle)
	if title == "" {
		title = "Feed"
	}
	return fmt.Sprintf("%s - %d articles - %s", title, articles, windowLabel(since, until))
}

// digestSendEnabled gates sends: daily runs keep auto-send, on-demand runs
// need an explicit -send.
func digestSendEnabled(cfg *Config, windowed, explicitSend bool) bool {
	return cfg.sendMode() && (!windowed || explicitSend)
}

// runListFeeds prints "<id>\t<title>" for every feed, newest config first, and
// does no EPUB work.
func runListFeeds(cfg *Config, w io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	feeds, err := newMinifluxClient(cfg.MinifluxURL, cfg.MinifluxAPIKey).fetchFeeds(ctx)
	if err != nil {
		return err
	}
	for _, f := range feedsSortedByTitle(feeds) {
		if _, err := fmt.Fprintf(w, "%d\t%s\n", f.ID, f.Title); err != nil {
			return fmt.Errorf("writing feed list: %w", err)
		}
	}
	return nil
}

// feedTitlesLower orders feed titles case-insensitively for menus and errors.
func feedTitlesLower(a, b apiFeed) bool {
	la, lb := strings.ToLower(a.Title), strings.ToLower(b.Title)
	if la != lb {
		return la < lb
	}
	return a.Title < b.Title
}
