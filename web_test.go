package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testWebToken = "test-token"

func newTestWebApp(t *testing.T) (*webApp, *httptest.Server) {
	t.Helper()
	cfg := &Config{OutputDir: t.TempDir(), WebToken: testWebToken, MaxPartBytes: defaultMaxPartBytes}
	a := newWebApp(cfg, &logger{})
	a.feedsFn = func(_ context.Context) ([]apiFeed, error) {
		return []apiFeed{{ID: 13, Title: "Netflix"}, {ID: 42, Title: "Example Feed"}}, nil
	}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(srv.Close)
	return a, srv
}

func newWebClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookie jar: %v", err)
	}
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

func login(t *testing.T, client *http.Client, srv *httptest.Server) {
	t.Helper()
	resp, err := client.PostForm(srv.URL+"/login", url.Values{"token": {testWebToken}})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200 after redirect", resp.StatusCode)
	}
}

func postRunJSON(t *testing.T, client *http.Client, srv *httptest.Server, form url.Values) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/run", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("POST /run: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	return resp, body
}

func waitIdle(t *testing.T, a *webApp) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		idle := a.running == nil
		a.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("run did not finish")
}

func TestWebLogin(t *testing.T) {
	_, srv := newTestWebApp(t)
	client := newWebClient(t)

	resp, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Access token") {
		t.Fatalf("unauthenticated home = %d, want login form:\n%s", resp.StatusCode, body)
	}

	wrong, err := client.PostForm(srv.URL+"/login", url.Values{"token": {"nope"}})
	if err != nil {
		t.Fatalf("wrong login: %v", err)
	}
	wrongBody := readBody(t, wrong)
	if wrong.StatusCode != http.StatusUnauthorized || !strings.Contains(wrongBody, "Invalid token.") {
		t.Fatalf("wrong token = %d %q, want 401 Invalid token.", wrong.StatusCode, wrongBody)
	}

	unauth, err := http.PostForm(srv.URL+"/run", url.Values{"mode": {"daily"}})
	if err != nil {
		t.Fatalf("unauth run: %v", err)
	}
	_ = unauth.Body.Close()
	if unauth.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated POST /run = %d, want 401", unauth.StatusCode)
	}

	login(t, client, srv)
	home, err := client.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	homeBody := readBody(t, home)
	if !strings.Contains(homeBody, "Send today's digest now") || !strings.Contains(homeBody, "Custom run") {
		t.Fatalf("authenticated home is missing the dashboard:\n%s", homeBody)
	}
}

func TestWebFormValidation(t *testing.T) {
	a, srv := newTestWebApp(t)
	client := newWebClient(t)
	login(t, client, srv)

	cases := []url.Values{
		{"mode": {"custom"}, "since": {"2026-11-27"}, "until": {"2026-11-26"}, "limit": {"5"}},
		{"mode": {"custom"}, "since": {"not-a-date"}, "limit": {"5"}},
		{"mode": {"custom"}, "limit": {"0"}},
		{"mode": {"custom"}, "limit": {"501"}},
	}
	for _, form := range cases {
		resp, body := postRunJSON(t, client, srv, form)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("form %v status = %d, want 400", form, resp.StatusCode)
		}
		if msg, _ := body["error"].(string); msg == "" {
			t.Errorf("form %v returned no error message", form)
		}
	}

	// The no-JS path renders the inline field error.
	resp, err := client.PostForm(srv.URL+"/run", url.Values{
		"mode": {"custom"}, "since": {"2026-11-27"}, "until": {"2026-11-26"}, "limit": {"5"},
	})
	if err != nil {
		t.Fatalf("POST /run: %v", err)
	}
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, "Until must be on or after since.") {
		t.Errorf("no-JS validation = %d, want inline until error:\n%s", resp.StatusCode, body)
	}
	a.mu.Lock()
	runs := len(a.runs)
	a.mu.Unlock()
	if runs != 0 {
		t.Errorf("validation started %d runs, want 0", runs)
	}
}

func TestWebGenerateOnlyRun(t *testing.T) {
	a, srv := newTestWebApp(t)
	client := newWebClient(t)
	login(t, client, srv)

	var got options
	a.runFn = func(opts options) (*runResult, error) {
		got = opts
		return &runResult{Entries: 5, Mode: "generate-only", Parts: []*part{{path: filepath.Join(a.cfg.OutputDir, "x.epub"), size: 1234}}, TotalBytes: 1234, Duration: 1500 * time.Millisecond}, nil
	}

	resp, body := postRunJSON(t, client, srv, url.Values{
		"mode": {"custom"}, "feed": {"13"}, "limit": {"5"},
	})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d, want 202: %v", resp.StatusCode, body)
	}
	waitIdle(t, a)
	if !got.generateOnly || got.send {
		t.Errorf("opts send=%v generateOnly=%v, want generate-only", got.send, got.generateOnly)
	}
	if got.feed != "13" || got.limit != 5 {
		t.Errorf("opts feed=%q limit=%d, want 13, 5", got.feed, got.limit)
	}

	status := getStatus(t, client, srv)
	if !strings.Contains(status, "Success") || !strings.Contains(status, "Generated only") {
		t.Errorf("status does not show success/generated:\n%s", status)
	}
	if !strings.Contains(status, "x.epub") || !strings.Contains(status, "1.2 KB") {
		t.Errorf("status does not show part details:\n%s", status)
	}
}

func TestWebSingleFlight(t *testing.T) {
	a, srv := newTestWebApp(t)
	client := newWebClient(t)
	login(t, client, srv)

	release := make(chan struct{})
	var once sync.Once
	a.runFn = func(options) (*runResult, error) {
		<-release
		return &runResult{Entries: 1, Mode: "generate-only"}, nil
	}

	resp, body := postRunJSON(t, client, srv, url.Values{"mode": {"daily"}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first run status = %d, want 202: %v", resp.StatusCode, body)
	}
	resp2, body2 := postRunJSON(t, client, srv, url.Values{"mode": {"daily"}})
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("second run status = %d, want 409", resp2.StatusCode)
	}
	if msg, _ := body2["error"].(string); !strings.Contains(msg, "already in progress") {
		t.Errorf("second run error = %q, want in-progress message", msg)
	}
	once.Do(func() { close(release) })
	waitIdle(t, a)
}

func TestWebFailureRendering(t *testing.T) {
	a, srv := newTestWebApp(t)
	client := newWebClient(t)
	login(t, client, srv)

	a.runFn = func(options) (*runResult, error) {
		return nil, errors.New("miniflux said boom")
	}
	resp, _ := postRunJSON(t, client, srv, url.Values{"mode": {"daily"}})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start status = %d, want 202", resp.StatusCode)
	}
	waitIdle(t, a)

	status := getStatus(t, client, srv)
	if !strings.Contains(status, "Failed") || !strings.Contains(status, "miniflux said boom") {
		t.Errorf("failure status does not show the error:\n%s", status)
	}
	if !strings.Contains(status, "miniflux said boom") {
		t.Error("failed run missing from recent runs")
	}
}

func TestWebRunsCappedAtFive(t *testing.T) {
	a, srv := newTestWebApp(t)
	client := newWebClient(t)
	login(t, client, srv)

	a.runFn = func(options) (*runResult, error) {
		return &runResult{Entries: 1, Mode: "generate-only"}, nil
	}
	for i := 0; i < 6; i++ {
		resp, _ := postRunJSON(t, client, srv, url.Values{"mode": {"daily"}})
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("run %d status = %d, want 202", i+1, resp.StatusCode)
		}
		waitIdle(t, a)
	}
	a.mu.Lock()
	runs := len(a.runs)
	a.mu.Unlock()
	if runs != maxWebRuns {
		t.Errorf("remembered runs = %d, want %d", runs, maxWebRuns)
	}
}

func TestWebNoFileRoute(t *testing.T) {
	_, srv := newTestWebApp(t)
	client := newWebClient(t)
	login(t, client, srv)

	resp, err := client.Get(srv.URL + "/files/2026-11-27/digest.epub")
	if err != nil {
		t.Fatalf("GET file: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("file route = %d, want 404 (no download endpoint)", resp.StatusCode)
	}
}

func TestServeRequiresWebToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := `{"miniflux_url":"https://example.org","miniflux_api_key":"k","output_dir":"` + t.TempDir() + `"}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	err := run(path, options{serve: "127.0.0.1:0"})
	if err == nil || !strings.Contains(err.Error(), "web_token is required") {
		t.Errorf("run error = %v, want web_token requirement", err)
	}
}

func getStatus(t *testing.T, client *http.Client, srv *httptest.Server) string {
	t.Helper()
	resp, err := client.Get(srv.URL + "/api/status")
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		_ = resp.Body.Close()
		t.Fatalf("decoding status: %v", err)
	}
	_ = resp.Body.Close()
	return body.Status
}

func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return string(b)
}
