package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

const entriesPageOne = `{
  "total": 3,
  "entries": [
    {
      "id": 1,
      "user_id": 7,
      "feed_id": 42,
      "title": "First",
      "url": "https://example.org/first",
      "comments_url": "",
      "author": "Alice",
      "content": "<p>first</p>",
      "hash": "abc",
      "published_at": "2026-11-26T20:00:00Z",
      "created_at": "2026-11-26T20:01:00Z",
      "status": "unread",
      "starred": false,
      "reading_time": 1,
      "enclosures": null,
      "feed": {
        "id": 42,
        "user_id": 7,
        "title": "Example Feed",
        "site_url": "https://example.org",
        "feed_url": "https://example.org/feed.xml",
        "category": {"id": 1, "title": "Tech", "user_id": 7},
        "icon": {"feed_id": 42, "icon_id": 5}
      }
    },
    {
      "id": 2,
      "feed_id": 43,
      "title": "Second",
      "url": "https://other.example/second",
      "author": "",
      "content": "<p>second</p>",
      "published_at": "2026-11-26T21:30:00Z",
      "status": "read"
    }
  ]
}`

const entriesPageTwo = `{
  "total": 3,
  "entries": [
    {
      "id": 3,
      "feed_id": 42,
      "title": "Third",
      "url": "https://example.org/third",
      "author": "Bob",
      "content": "<p>third</p>",
      "published_at": "2026-11-27T01:00:00Z",
      "status": "unread"
    }
  ]
}`

const feedsJSON = `[
  {
    "id": 42,
    "user_id": 7,
    "title": "Example Feed",
    "site_url": "https://example.org",
    "feed_url": "https://example.org/feed.xml",
    "category": {"id": 1, "title": "Tech", "user_id": 7},
    "icon": {"feed_id": 42, "icon_id": 5}
  },
  {
    "id": 43,
    "user_id": 7,
    "title": "Other Feed",
    "site_url": "https://other.example",
    "feed_url": "https://other.example/feed.xml"
  }
]`

func TestFetchEntriesPagination(t *testing.T) {
	after := time.Date(2026, 11, 26, 6, 45, 0, 0, time.UTC)
	wantAfter := strconv.FormatInt(after.Unix(), 10)

	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Token") != "secret" {
			t.Errorf("missing or wrong X-Auth-Token: %q", r.Header.Get("X-Auth-Token"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/entries" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q.Get("published_after"); got != wantAfter {
			t.Errorf("published_after = %q, want %q", got, wantAfter)
		}
		if got := q.Get("limit"); got != "100" {
			t.Errorf("limit = %q, want 100", got)
		}
		offsets = append(offsets, q.Get("offset"))

		w.Header().Set("Content-Type", "application/json")
		switch q.Get("offset") {
		case "0":
			_, _ = fmt.Fprint(w, entriesPageOne)
		case "2":
			_, _ = fmt.Fprint(w, entriesPageTwo)
		default:
			t.Errorf("unexpected offset %q", q.Get("offset"))
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	client := newMinifluxClient(srv.URL, "secret")
	entries, err := client.fetchEntries(context.Background(), after)
	if err != nil {
		t.Fatalf("fetchEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[2].Title != "Third" || entries[2].FeedID != 42 {
		t.Errorf("unexpected third entry: %+v", entries[2])
	}
	if got, want := entries[0].PublishedAt.UTC(), time.Date(2026, 11, 26, 20, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("published_at = %s, want %s", got, want)
	}
	if len(offsets) != 2 || offsets[0] != "0" || offsets[1] != "2" {
		t.Errorf("offsets = %v, want [0 2]", offsets)
	}
}

func TestFetchFeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Auth-Token") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/v1/feeds" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, feedsJSON)
	}))
	defer srv.Close()

	feeds, err := newMinifluxClient(srv.URL, "secret").fetchFeeds(context.Background())
	if err != nil {
		t.Fatalf("fetchFeeds: %v", err)
	}
	if len(feeds) != 2 {
		t.Fatalf("got %d feeds, want 2", len(feeds))
	}
	if feeds[42].Title != "Example Feed" || feeds[42].SiteURL != "https://example.org" {
		t.Errorf("unexpected feed 42: %+v", feeds[42])
	}
}

func TestRetryOnServerError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, "boom")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	if _, err := newMinifluxClient(srv.URL, "secret").fetchFeeds(context.Background()); err != nil {
		t.Fatalf("fetchFeeds: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", calls)
	}
}

func TestNoRetryOnClientError(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error_message":"bad token"}`)
	}))
	defer srv.Close()

	if _, err := newMinifluxClient(srv.URL, "secret").fetchFeeds(context.Background()); err == nil {
		t.Fatal("fetchFeeds succeeded, want error")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 4xx)", calls)
	}
}

func TestEntriesJSONShapeMatchesMiniflux(t *testing.T) {
	// Guards the struct tags against drift from the documented API shape.
	var page entriesPage
	if err := json.Unmarshal([]byte(entriesPageOne), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if page.Total != 3 || len(page.Entries) != 2 {
		t.Fatalf("unexpected page: %+v", page)
	}
}
