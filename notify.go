package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	notifyTimeout    = 10 * time.Second
	notifyErrorLimit = 200
)

// notifyEnabled reports whether the ntfy block is complete enough to publish.
// The config validator guarantees it is all-or-nothing.
func (c *Config) notifyEnabled() bool {
	return c.NtfyURL != "" && c.NtfyTopic != "" && c.NtfyToken != ""
}

// notify publishes one ntfy message with the header API. Callers treat it as
// best-effort: a failed notification never fails the run.
func notify(cfg *Config, title, message string, priority int, tags string) error {
	endpoint := strings.TrimRight(cfg.NtfyURL, "/") + "/" + url.PathEscape(cfg.NtfyTopic)
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(message))
	if err != nil {
		return fmt.Errorf("building ntfy request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+cfg.NtfyToken)
	req.Header.Set("X-Title", title)
	req.Header.Set("X-Priority", strconv.Itoa(priority))
	req.Header.Set("X-Tags", tags)
	if cfg.NtfyClick != "" {
		req.Header.Set("X-Click", cfg.NtfyClick)
	}

	client := &http.Client{Timeout: notifyTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("publishing ntfy message: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ntfy returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// notifyRun reports a finished run: failures and actual sends only. A
// generate-only run released nothing, so it stays quiet.
func notifyRun(cfg *Config, log *logger, res *runResult, runErr error) {
	if !cfg.notifyEnabled() {
		return
	}
	if runErr != nil {
		title := "Digest failed"
		msg := []rune(runErr.Error())
		if len(msg) > notifyErrorLimit {
			msg = msg[:notifyErrorLimit]
		}
		if err := notify(cfg, title, string(msg), 4, "warning"); err != nil {
			log.error("ntfy notification failed: %v", err)
		}
		return
	}
	if res == nil || res.Mode != "send" {
		return
	}
	if err := notify(cfg, res.Title, notifyMessage(res), 3, "books"); err != nil {
		log.error("ntfy notification failed: %v", err)
	}
}

// notifyMessage summarises what reached the Kindle.
func notifyMessage(res *runResult) string {
	msg := fmt.Sprintf("%d %s", res.Entries, plural(res.Entries, "article", "articles"))
	if res.Emails > 0 {
		msg += fmt.Sprintf(" (%d from email)", res.Emails)
	}
	return fmt.Sprintf("%s, %d %s, %.1f MB sent to Kindle",
		msg, len(res.Parts), plural(len(res.Parts), "part", "parts"),
		float64(res.TotalBytes)/1_000_000)
}
