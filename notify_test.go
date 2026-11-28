package main

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ntfyMessage is one request the fake ntfy server received.
type ntfyMessage struct {
	Method   string
	Path     string
	Auth     string
	Title    string
	Priority string
	Tags     string
	Click    string
	Body     string
}

// ntfyRecorder is a fake ntfy server that records publishes. status 0 means
// 200; any other status is answered with body.
type ntfyRecorder struct {
	mu     sync.Mutex
	msgs   []ntfyMessage
	status int
	body   string
}

func newNtfyServer(t *testing.T, rec *ntfyRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.msgs = append(rec.msgs, ntfyMessage{
			Method:   r.Method,
			Path:     r.URL.Path,
			Auth:     r.Header.Get("Authorization"),
			Title:    r.Header.Get("X-Title"),
			Priority: r.Header.Get("X-Priority"),
			Tags:     r.Header.Get("X-Tags"),
			Click:    r.Header.Get("X-Click"),
			Body:     string(body),
		})
		status, msg := rec.status, rec.body
		rec.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, msg)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (r *ntfyRecorder) received() []ntfyMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ntfyMessage(nil), r.msgs...)
}

func ntfyConfig(url string) *Config {
	return &Config{
		NtfyURL:   url,
		NtfyTopic: "kindle-digest",
		NtfyToken: "write-only-token",
	}
}

func TestNotifyPublishesPayload(t *testing.T) {
	rec := &ntfyRecorder{}
	srv := newNtfyServer(t, rec)
	cfg := ntfyConfig(srv.URL + "/")
	cfg.NtfyClick = "https://example.org/digest/"

	if err := notify(cfg, "Daily Digest ready", "82 articles, 1 part, 6.9 MB sent to Kindle", 3, "books"); err != nil {
		t.Fatalf("notify: %v", err)
	}

	msgs := rec.received()
	if len(msgs) != 1 {
		t.Fatalf("requests = %d, want 1", len(msgs))
	}
	m := msgs[0]
	if m.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", m.Method)
	}
	if m.Path != "/kindle-digest" {
		t.Errorf("path = %q, want /kindle-digest", m.Path)
	}
	if m.Auth != "Bearer write-only-token" {
		t.Errorf("Authorization = %q, want the bearer token", m.Auth)
	}
	if m.Title != "Daily Digest ready" {
		t.Errorf("X-Title = %q", m.Title)
	}
	if m.Priority != "3" {
		t.Errorf("X-Priority = %q, want 3", m.Priority)
	}
	if m.Tags != "books" {
		t.Errorf("X-Tags = %q, want books", m.Tags)
	}
	if m.Click != "https://example.org/digest/" {
		t.Errorf("X-Click = %q", m.Click)
	}
	if m.Body != "82 articles, 1 part, 6.9 MB sent to Kindle" {
		t.Errorf("body = %q", m.Body)
	}
}

func TestNotifyOmitsClickHeaderWhenUnset(t *testing.T) {
	rec := &ntfyRecorder{}
	srv := newNtfyServer(t, rec)
	if err := notify(ntfyConfig(srv.URL), "t", "m", 3, "books"); err != nil {
		t.Fatalf("notify: %v", err)
	}
	msgs := rec.received()
	if len(msgs) != 1 {
		t.Fatalf("requests = %d, want 1", len(msgs))
	}
	if msgs[0].Click != "" {
		t.Errorf("X-Click = %q, want it omitted", msgs[0].Click)
	}
}

func TestNotifyNon200ReturnsErrorWithBody(t *testing.T) {
	rec := &ntfyRecorder{status: http.StatusTooManyRequests, body: "rate limit exceeded"}
	srv := newNtfyServer(t, rec)

	err := notify(ntfyConfig(srv.URL), "t", "m", 3, "books")
	if err == nil {
		t.Fatal("notify succeeded, want an error")
	}
	for _, want := range []string{"429", "rate limit exceeded"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
}

func TestNotifyRunSuccessSendsOneNotification(t *testing.T) {
	tests := []struct {
		name  string
		title string
	}{
		{"daily", "Daily Digest ready"},
		{"windowed", "Daily Digest - 26 Nov 2026 to 27 Nov 2026"},
		{"collection", "Rust Blog - 12 articles - 2026-11-28"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &ntfyRecorder{}
			srv := newNtfyServer(t, rec)
			cfg := ntfyConfig(srv.URL)
			res := &runResult{
				Entries:    82,
				Parts:      []*part{{size: 6_900_000}},
				TotalBytes: 6_900_000,
				Mode:       "send",
				Title:      tt.title,
				Emails:     2,
			}
			notifyRun(cfg, &logger{buf: &bytes.Buffer{}}, res, nil)

			msgs := rec.received()
			if len(msgs) != 1 {
				t.Fatalf("requests = %d, want exactly one", len(msgs))
			}
			m := msgs[0]
			if m.Title != tt.title {
				t.Errorf("X-Title = %q, want %q", m.Title, tt.title)
			}
			if m.Priority != "3" {
				t.Errorf("X-Priority = %q, want 3", m.Priority)
			}
			if m.Tags != "books" {
				t.Errorf("X-Tags = %q, want books", m.Tags)
			}
			want := "82 articles (2 from email), 1 part, 6.9 MB sent to Kindle"
			if m.Body != want {
				t.Errorf("body = %q, want %q", m.Body, want)
			}
		})
	}
}

func TestNotifyRunFailureSendsFailureNotification(t *testing.T) {
	t.Run("wraps the error", func(t *testing.T) {
		rec := &ntfyRecorder{}
		srv := newNtfyServer(t, rec)
		notifyRun(ntfyConfig(srv.URL), &logger{buf: &bytes.Buffer{}}, nil,
			errors.New("SMTP delivery: connection refused"))

		msgs := rec.received()
		if len(msgs) != 1 {
			t.Fatalf("requests = %d, want exactly one", len(msgs))
		}
		if msgs[0].Title != "Digest failed" {
			t.Errorf("X-Title = %q, want Digest failed", msgs[0].Title)
		}
		if msgs[0].Priority != "4" {
			t.Errorf("X-Priority = %q, want 4", msgs[0].Priority)
		}
		if msgs[0].Tags != "warning" {
			t.Errorf("X-Tags = %q, want warning", msgs[0].Tags)
		}
		if !strings.Contains(msgs[0].Body, "SMTP delivery") {
			t.Errorf("body = %q, want the run error", msgs[0].Body)
		}
	})

	t.Run("truncates to 200 characters", func(t *testing.T) {
		rec := &ntfyRecorder{}
		srv := newNtfyServer(t, rec)
		notifyRun(ntfyConfig(srv.URL), &logger{buf: &bytes.Buffer{}}, nil,
			errors.New(strings.Repeat("x", 300)))

		msgs := rec.received()
		if len(msgs) != 1 {
			t.Fatalf("requests = %d, want exactly one", len(msgs))
		}
		if len([]rune(msgs[0].Body)) != 200 {
			t.Errorf("body length = %d runes, want 200", len([]rune(msgs[0].Body)))
		}
	})
}

func TestNotifyRunGenerateOnlySendsNothing(t *testing.T) {
	rec := &ntfyRecorder{}
	srv := newNtfyServer(t, rec)
	res := &runResult{Entries: 3, Parts: []*part{{size: 100}}, TotalBytes: 100, Mode: "generate-only", Title: "Daily Digest ready"}
	notifyRun(ntfyConfig(srv.URL), &logger{buf: &bytes.Buffer{}}, res, nil)

	if msgs := rec.received(); len(msgs) != 0 {
		t.Errorf("requests = %d, want none for a generate-only run", len(msgs))
	}
}

func TestNotifyRunDisabledMakesNoRequest(t *testing.T) {
	rec := &ntfyRecorder{}
	srv := newNtfyServer(t, rec)
	cfg := ntfyConfig(srv.URL)
	cfg.NtfyToken = ""

	notifyRun(cfg, &logger{buf: &bytes.Buffer{}},
		&runResult{Mode: "send", Title: "Daily Digest ready"}, nil)
	notifyRun(cfg, &logger{buf: &bytes.Buffer{}}, nil, errors.New("boom"))

	if msgs := rec.received(); len(msgs) != 0 {
		t.Errorf("requests = %d, want none when ntfy is not configured", len(msgs))
	}
}

func TestNotifyRunFailureIsBestEffort(t *testing.T) {
	rec := &ntfyRecorder{status: http.StatusInternalServerError, body: "boom"}
	srv := newNtfyServer(t, rec)
	var logBuf bytes.Buffer

	notifyRun(ntfyConfig(srv.URL), &logger{buf: &logBuf},
		&runResult{Mode: "send", Title: "Daily Digest ready"}, nil)

	if !strings.Contains(logBuf.String(), "ntfy notification failed") {
		t.Errorf("log = %q, want an ntfy failure line", logBuf.String())
	}
}

func TestRunNotifiesFailureEndToEnd(t *testing.T) {
	rec := &ntfyRecorder{}
	ntfySrv := newNtfyServer(t, rec)
	rss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "miniflux is down", http.StatusInternalServerError)
	}))
	defer rss.Close()

	raw := validConfig()
	raw["miniflux_url"] = rss.URL
	raw["output_dir"] = t.TempDir()
	raw["ntfy_url"] = ntfySrv.URL
	raw["ntfy_topic"] = "kindle-digest"
	raw["ntfy_token"] = "tok"

	if err := run(writeConfigFile(t, raw), options{}); err == nil {
		t.Fatal("run succeeded, want the miniflux error")
	}

	msgs := rec.received()
	if len(msgs) != 1 {
		t.Fatalf("requests = %d, want exactly one failure notification", len(msgs))
	}
	if msgs[0].Title != "Digest failed" {
		t.Errorf("X-Title = %q, want Digest failed", msgs[0].Title)
	}
	if msgs[0].Body == "" {
		t.Error("failure notification body is empty")
	}
}

func TestRunGenerateOnlySendsNoNotificationEndToEnd(t *testing.T) {
	rec := &ntfyRecorder{}
	ntfySrv := newNtfyServer(t, rec)
	rss, _ := newDigestRSSServer(t)

	raw := validConfig()
	raw["miniflux_url"] = rss.URL
	raw["output_dir"] = t.TempDir()
	raw["ntfy_url"] = ntfySrv.URL
	raw["ntfy_topic"] = "kindle-digest"
	raw["ntfy_token"] = "tok"

	if err := run(writeConfigFile(t, raw), options{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if msgs := rec.received(); len(msgs) != 0 {
		t.Errorf("requests = %d, want none for a generate-only run", len(msgs))
	}
}
