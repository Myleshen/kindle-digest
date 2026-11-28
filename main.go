package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
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

// options holds the command-line flags that select a run mode.
type options struct {
	feed         string
	limit        int
	send         bool
	generateOnly bool
	listFeeds    bool
	serve        string
	since        string
	until        string
	sinceTime    time.Time
	untilTime    time.Time
}

func main() {
	configPath := flag.String("config", "config.json", "path to the JSON config file")
	feed := flag.String("feed", "", "export one feed's latest entries: feed id or title substring")
	limit := flag.Int("limit", defaultCollectionLimit, "number of entries to export with -feed (1-500)")
	send := flag.Bool("send", false, "email an on-demand run; without it on-demand runs are generate-only")
	listFeeds := flag.Bool("list-feeds", false, "print feed ids and titles, then exit")
	serve := flag.String("serve", "", "serve the web UI on this address (e.g. 0.0.0.0:8090)")
	since := flag.String("since", "", "window start: RFC3339, 2006-01-02T15:04 or 2006-01-02 (Asia/Kolkata)")
	until := flag.String("until", "", "window end, exclusive; same formats as -since (default now)")
	flag.Parse()

	opts := options{
		feed:      *feed,
		limit:     *limit,
		send:      *send,
		listFeeds: *listFeeds,
		serve:     *serve,
		since:     *since,
		until:     *until,
	}
	if err := run(*configPath, opts); err != nil {
		fmt.Fprintf(os.Stderr, "kindle-digest: %v\n", err)
		os.Exit(1)
	}
}

func run(configPath string, opts options) error {
	if opts.until != "" && opts.since == "" {
		return fmt.Errorf("-until requires -since")
	}
	var err error
	if opts.since != "" {
		if opts.sinceTime, err = parseWindowTime(opts.since); err != nil {
			return err
		}
	}
	if opts.until != "" {
		if opts.untilTime, err = parseWindowTime(opts.until); err != nil {
			return err
		}
	}
	if !opts.untilTime.IsZero() && opts.untilTime.Before(opts.sinceTime) {
		return fmt.Errorf("-until %s is before -since %s", opts.until, opts.since)
	}
	if opts.send && opts.feed == "" && opts.since == "" {
		return fmt.Errorf("-send requires -feed or -since (daily mode sends automatically from the config)")
	}

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

	switch {
	case opts.listFeeds:
		return runListFeeds(cfg, os.Stdout)
	case opts.serve != "":
		if cfg.WebToken == "" {
			return fmt.Errorf("web_token is required in the config when -serve is set")
		}
		return serveWeb(cfg, log, opts.serve)
	}
	unlock, err := acquireRunLock(cfg.OutputDir)
	if err != nil {
		return err
	}
	defer unlock()

	var result *runResult
	switch {
	case opts.feed != "":
		result, err = runCollection(cfg, log, started, opts)
	default:
		result, err = runDigest(cfg, log, started, opts)
	}
	if err != nil {
		log.fileOnly("fatal: %v", err)
		notifyRun(cfg, log, result, err)
		return err
	}
	notifyRun(cfg, log, result, nil)
	return nil
}

// sortEntriesByFeed orders a digest by feed title, case-insensitively, then
// newest first within each feed.
func sortEntriesByFeed(entries []apiEntry, feeds map[int64]apiFeed) {
	title := func(e apiEntry) string { return displayFeedTitle(feeds[e.FeedID]) }
	sort.SliceStable(entries, func(i, j int) bool {
		ai, aj := title(entries[i]), title(entries[j])
		li, lj := strings.ToLower(ai), strings.ToLower(aj)
		if li != lj {
			return li < lj
		}
		if ai != aj {
			return ai < aj
		}
		return entries[i].PublishedAt.After(entries[j].PublishedAt)
	})
}

// displayFeedTitle is the feed name used in sections and separators.
func displayFeedTitle(feed apiFeed) string {
	if feed.Title == "" {
		return "Unknown feed"
	}
	return feed.Title
}

// runResult is what a digest or collection run produced, for logging and the
// web UI's status page.
type runResult struct {
	Entries    int
	Parts      []*part
	TotalBytes int64
	Mode       string
	Duration   time.Duration
	Windowed   bool
	// Title is the notification title: the run's base, or "Daily Digest
	// ready" for a plain daily run.
	Title string
	// Emails is how many email newsletters made it into the digest.
	Emails int
}

func runDigest(cfg *Config, log *logger, started time.Time, opts options) (*runResult, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	now := time.Now().In(digestLocation)
	dateLabel := now.Format("2 Jan 2006")
	windowed := !opts.sinceTime.IsZero()
	since, until := now.Add(-24*time.Hour), now
	if windowed {
		since = opts.sinceTime
		until = opts.untilTime
		if until.IsZero() {
			until = now
		}
	}

	client := newMinifluxClient(cfg.MinifluxURL, cfg.MinifluxAPIKey)
	feeds, err := client.fetchFeeds(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := client.fetchEntriesWindow(ctx, since, until)
	if err != nil {
		return nil, err
	}
	filtered := entries[:0]
	for _, e := range entries {
		if withinWindow(e.PublishedAt, since, until) {
			filtered = append(filtered, e)
		}
	}
	entries = filtered

	var emailFailures, emailCount int
	if len(cfg.EmailSources) > 0 {
		emails, failures := collectEmails(func() (mailboxConn, error) {
			return newMailbox(cfg)
		}, cfg.EmailSources, since, until, cfg.MaxEmailsTotal, log)
		emailFailures = failures
		emailCount = len(emails)
		entries = append(entries, emailEntries(emails, feeds)...)
	}

	if len(entries) == 0 {
		log.info("no entries in window (published in [%s, %s))",
			since.Format(time.RFC3339), until.Format(time.RFC3339))
		return &runResult{Windowed: windowed}, emailFailureError(emailFailures)
	}

	sortEntriesByFeed(entries, feeds)
	log.info("fetched %d entries from %d feeds (published in [%s, %s))",
		len(entries), len(feeds), since.Format(time.RFC3339), until.Format(time.RFC3339))

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

	outDir := filepath.Join(cfg.OutputDir, now.Format("2006-01-02"))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating output directory %s: %w", outDir, err)
	}

	base := "Daily Digest - " + dateLabel
	subject := "Daily Digest — " + dateLabel
	if windowed {
		base = windowedDigestBase(since, until)
		subject = base
	}

	p := &packer{
		maxPart:  cfg.MaxPartBytes,
		baseName: base,
		build: func(entries []*preparedEntry, dst string) (int64, error) {
			return buildDigestEPUB(ctx, entries, base, dateLabel, dst)
		},
		shrink: func(e *preparedEntry) bool {
			return e.shrinkToFit(cfg.MaxPartBytes)
		},
	}
	parts, err := p.pack(prepared, outDir)
	if err != nil {
		return nil, fmt.Errorf("packing digest: %w", err)
	}

	var totalBytes int64
	for i, pt := range parts {
		totalBytes += pt.size
		log.info("wrote part %d/%d: %s (%d bytes, %d entries)",
			i+1, len(parts), filepath.Base(pt.path), pt.size, len(pt.entries))
	}

	notifyTitle := base
	if !windowed {
		notifyTitle = "Daily Digest ready"
	}
	result := &runResult{
		Entries:    len(prepared),
		Parts:      parts,
		TotalBytes: totalBytes,
		Mode:       "generate-only",
		Duration:   time.Since(started).Round(time.Millisecond),
		Windowed:   windowed,
		Title:      notifyTitle,
		Emails:     emailCount,
	}
	if !opts.generateOnly && digestSendEnabled(cfg, windowed, opts.send) {
		result.Mode = "send"
		m := &mailer{cfg: cfg, log: log, interval: sendInterval}
		if err := m.send(ctx, parts, subject); err != nil {
			return result, err
		}
	} else if windowed && cfg.sendMode() {
		log.info("send skipped: on-demand runs are generate-only without -send")
	} else if cfg.KindleEmail == "" {
		log.info("send skipped: kindle_email is empty (generate-only mode)")
	} else {
		log.info("send skipped: SMTP settings are incomplete (generate-only mode)")
	}

	log.info("summary: entries=%d parts=%d bytes=%d mode=%s duration=%s",
		len(prepared), len(parts), totalBytes, result.Mode, result.Duration)
	if err := emailFailureError(emailFailures); err != nil {
		log.error("email sources: %d error(s); the digest above was built without the failed emails", emailFailures)
		return result, err
	}
	return result, nil
}

// emailFailureError makes IMAP trouble exit non-zero without blocking the RSS
// digest or the send.
func emailFailureError(failures int) error {
	if failures == 0 {
		return nil
	}
	return fmt.Errorf("email collection had %d error(s); see the log", failures)
}
