package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"
	_ "time/tzdata"
)

var digestLocation = func() *time.Location {
	loc, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		// Asia/Kolkata has no DST; this keeps the tool working if tzdata is
		// somehow unavailable.
		return time.FixedZone("IST", 5*60*60+30*60)
	}
	return loc
}()

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON config file")
	flag.Parse()

	if err := run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "kindle-digest: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string) error {
	started := time.Now()

	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	log, err := newLogger(cfg.LogFile)
	if err != nil {
		return err
	}
	defer log.close()

	if err := runDigest(cfg, log, started); err != nil {
		log.fileOnly("fatal: %v", err)
		return err
	}
	return nil
}

func runDigest(cfg *Config, log *logger, started time.Time) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	now := time.Now().In(digestLocation)
	dateLabel := now.Format("2 Jan 2006")
	windowStart := now.Add(-24 * time.Hour)

	client := newMinifluxClient(cfg.MinifluxURL, cfg.MinifluxAPIKey)
	feeds, err := client.fetchFeeds(ctx)
	if err != nil {
		return err
	}
	entries, err := client.fetchEntries(ctx, windowStart)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		log.info("no entries in window (published after %s)", windowStart.Format(time.RFC3339))
		return nil
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].PublishedAt.Before(entries[j].PublishedAt)
	})
	log.info("fetched %d entries from %d feeds (published after %s)",
		len(entries), len(feeds), windowStart.Format(time.RFC3339))

	downloads := newImageDownloader()
	prepared := make([]*preparedEntry, 0, len(entries))
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		e, err := prepareEntry(ctx, i, entry, feeds[entry.FeedID], cfg.MaxPartBytes, downloads)
		if err != nil {
			log.error("skipping entry %d (%q): %v", entry.ID, entry.Title, err)
			continue
		}
		prepared = append(prepared, e)
	}
	if len(prepared) == 0 {
		return fmt.Errorf("no entries could be prepared")
	}

	outDir := filepath.Join(cfg.OutputDir, now.Format("2006-01-02"))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("creating output directory %s: %w", outDir, err)
	}

	p := &packer{
		maxPart:   cfg.MaxPartBytes,
		dateLabel: dateLabel,
		build: func(entries []*preparedEntry, dst string) (int64, error) {
			return buildEPUB(ctx, entries, dateLabel, dst)
		},
		shrink: func(e *preparedEntry) bool {
			return e.shrinkToFit(cfg.MaxPartBytes)
		},
	}
	parts, err := p.pack(prepared, outDir)
	if err != nil {
		return fmt.Errorf("packing digest: %w", err)
	}

	var totalBytes int64
	for i, pt := range parts {
		totalBytes += pt.size
		log.info("wrote part %d/%d: %s (%d bytes, %d entries)",
			i+1, len(parts), filepath.Base(pt.path), pt.size, len(pt.entries))
	}

	mode := "generate-only"
	if cfg.sendMode() {
		mode = "send"
		m := &mailer{cfg: cfg, log: log, interval: sendInterval}
		if err := m.send(ctx, parts, dateLabel); err != nil {
			return err
		}
	} else if cfg.KindleEmail == "" {
		log.info("send skipped: kindle_email is empty (generate-only mode)")
	} else {
		log.info("send skipped: SMTP settings are incomplete (generate-only mode)")
	}

	log.info("summary: entries=%d parts=%d bytes=%d mode=%s duration=%s",
		len(prepared), len(parts), totalBytes, mode, time.Since(started).Round(time.Millisecond))
	return nil
}
