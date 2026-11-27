package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	minifluxTimeout  = 30 * time.Second
	minifluxPageSize = 100
	maxResponseBytes = 64 << 20
)

type apiFeed struct {
	ID      int64  `json:"id"`
	Title   string `json:"title"`
	SiteURL string `json:"site_url"`
}

type apiEntry struct {
	ID          int64     `json:"id"`
	FeedID      int64     `json:"feed_id"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Author      string    `json:"author"`
	Content     string    `json:"content"`
	PublishedAt time.Time `json:"published_at"`
}

type entriesPage struct {
	Total   int        `json:"total"`
	Entries []apiEntry `json:"entries"`
}

type minifluxClient struct {
	baseURL string
	token   string
	hc      *http.Client
}

func newMinifluxClient(baseURL, token string) *minifluxClient {
	return &minifluxClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		hc:      &http.Client{Timeout: minifluxTimeout},
	}
}

// fetchEntries returns every entry published after the given time, following
// Miniflux's offset pagination until total is covered. No status filter is
// sent, so both read and unread entries are included.
func (c *minifluxClient) fetchEntries(ctx context.Context, after time.Time) ([]apiEntry, error) {
	return c.fetchEntriesWindow(ctx, after, time.Time{})
}

// fetchEntriesWindow is fetchEntries with an optional exclusive upper bound.
func (c *minifluxClient) fetchEntriesWindow(ctx context.Context, after, before time.Time) ([]apiEntry, error) {
	var all []apiEntry
	for offset := 0; ; {
		q := url.Values{
			// Miniflux parses published_after as a Unix timestamp in seconds.
			"published_after": {strconv.FormatInt(after.Unix(), 10)},
			"limit":           {strconv.Itoa(minifluxPageSize)},
			"offset":          {strconv.Itoa(offset)},
		}
		if !before.IsZero() {
			q.Set("published_before", strconv.FormatInt(before.Unix(), 10))
		}
		var page entriesPage
		if err := c.getJSON(ctx, "/v1/entries", q, &page); err != nil {
			return nil, fmt.Errorf("fetching entries at offset %d: %w", offset, err)
		}
		all = append(all, page.Entries...)
		offset += len(page.Entries)
		if len(page.Entries) == 0 || offset >= page.Total {
			break
		}
	}
	return all, nil
}

// fetchFeedEntries returns the newest limit entries of one feed, newest first,
// paging through the feed's entries in 100-entry batches.
func (c *minifluxClient) fetchFeedEntries(ctx context.Context, feedID int64, limit int) ([]apiEntry, error) {
	return c.fetchFeedEntriesWindow(ctx, feedID, limit, time.Time{}, time.Time{})
}

// fetchFeedEntriesWindow is fetchFeedEntries restricted to [since, until).
func (c *minifluxClient) fetchFeedEntriesWindow(ctx context.Context, feedID int64, limit int, since, until time.Time) ([]apiEntry, error) {
	var all []apiEntry
	for offset := 0; offset < limit; {
		size := min(limit-offset, minifluxPageSize)
		q := url.Values{
			"limit":     {strconv.Itoa(size)},
			"offset":    {strconv.Itoa(offset)},
			"order":     {"published_at"},
			"direction": {"desc"},
		}
		if !since.IsZero() {
			q.Set("published_after", strconv.FormatInt(since.Unix(), 10))
		}
		if !until.IsZero() {
			q.Set("published_before", strconv.FormatInt(until.Unix(), 10))
		}
		var page entriesPage
		path := fmt.Sprintf("/v1/feeds/%d/entries", feedID)
		if err := c.getJSON(ctx, path, q, &page); err != nil {
			return nil, fmt.Errorf("fetching entries for feed %d at offset %d: %w", feedID, offset, err)
		}
		all = append(all, page.Entries...)
		offset += len(page.Entries)
		if len(page.Entries) == 0 || len(page.Entries) < size || offset >= page.Total {
			break
		}
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func (c *minifluxClient) fetchFeeds(ctx context.Context) (map[int64]apiFeed, error) {
	var feeds []apiFeed
	if err := c.getJSON(ctx, "/v1/feeds", nil, &feeds); err != nil {
		return nil, fmt.Errorf("fetching feeds: %w", err)
	}
	byID := make(map[int64]apiFeed, len(feeds))
	for _, f := range feeds {
		byID[f.ID] = f
	}
	return byID, nil
}

// getJSON performs a GET with the API token and retries once on a network
// error or a 5xx response.
func (c *minifluxClient) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("X-Auth-Token", c.token)
		req.Header.Set("Accept", "application/json")

		resp, err := c.hc.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("GET %s: %w", path, err)
			continue
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("GET %s: reading response: %w", path, readErr)
			continue
		}
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("GET %s: server returned %d: %s", path, resp.StatusCode, snippet(body))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("GET %s: server returned %d: %s", path, resp.StatusCode, snippet(body))
		}
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("GET %s: decoding response: %w", path, err)
		}
		return nil
	}
	return fmt.Errorf("%w (after 1 retry)", lastErr)
}

func snippet(body []byte) string {
	const max = 200
	s := strings.TrimSpace(string(body))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
