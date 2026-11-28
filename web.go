package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	webCookieName = "kindle_digest_auth"
	webCookieAge  = 30 * 24 * 60 * 60
	maxWebRuns    = 5
)

// partRecord is one written EPUB in a run.
type partRecord struct {
	Name  string `json:"name"`
	Bytes int64  `json:"bytes"`
}

// runRecord is one digest or custom run shown on the dashboard.
type runRecord struct {
	ID         string       `json:"id"`
	Started    time.Time    `json:"started"`
	Label      string       `json:"label"`
	Send       bool         `json:"send"`
	Mode       string       `json:"mode"`
	Entries    int          `json:"entries"`
	Parts      []partRecord `json:"parts"`
	TotalBytes int64        `json:"total_bytes"`
	DurationMS int64        `json:"duration_ms"`
	Outcome    string       `json:"outcome"`
	Error      string       `json:"error,omitempty"`
}

func (r *runRecord) StatusText() string {
	switch r.Outcome {
	case "running":
		return "Running…"
	case "error":
		return "Failed"
	default:
		return "Success"
	}
}

func (r *runRecord) StatusClass() string {
	switch r.Outcome {
	case "running":
		return "warn"
	case "error":
		return "err"
	default:
		return "ok"
	}
}

func (r *runRecord) ModeText() string {
	switch {
	case r.Outcome == "running":
		return ""
	case !r.Send:
		return "Generated only"
	case r.Mode == "send":
		return "Sent to Kindle"
	default:
		return "Generated only (SMTP not configured)"
	}
}

type formState struct {
	Feed   string
	Since  string
	Until  string
	Limit  int
	Send   bool
	Errors map[string]string
}

type pageData struct {
	Title   string
	Feeds   []apiFeed
	Form    formState
	Record  *runRecord
	Runs    []*runRecord
	Error   string
	Running bool
	Refresh bool
}

// webApp serves the dashboard and owns the single background run.
type webApp struct {
	cfg  *Config
	log  *logger
	mux  *http.ServeMux
	tmpl *template.Template

	tokenHash [32]byte
	now       func() time.Time

	runFn   func(options) (*runResult, error)
	feedsFn func(context.Context) ([]apiFeed, error)

	mu      sync.Mutex
	running *runRecord
	runs    []*runRecord
}

func newWebApp(cfg *Config, log *logger) *webApp {
	a := &webApp{
		cfg:       cfg,
		log:       log,
		mux:       http.NewServeMux(),
		tokenHash: sha256.Sum256([]byte(cfg.WebToken)),
		now:       time.Now,
	}
	a.runFn = func(opts options) (*runResult, error) {
		started := time.Now()
		var res *runResult
		var err error
		if opts.feed != "" {
			res, err = runCollection(a.cfg, a.log, started, opts)
		} else {
			res, err = runDigest(a.cfg, a.log, started, opts)
		}
		notifyRun(a.cfg, a.log, res, err)
		return res, err
	}
	a.feedsFn = func(ctx context.Context) ([]apiFeed, error) {
		feeds, err := newMinifluxClient(cfg.MinifluxURL, cfg.MinifluxAPIKey).fetchFeeds(ctx)
		if err != nil {
			return nil, err
		}
		return feedsSortedByTitle(feeds), nil
	}

	funcs := template.FuncMap{
		"time":     func(t time.Time) string { return t.In(digestLocation).Format("2 Jan 15:04") },
		"bytes":    fmtBytes,
		"duration": func(ms int64) string { return (time.Duration(ms) * time.Millisecond).Round(time.Millisecond).String() },
		"itoa":     func(n int64) string { return strconv.FormatInt(n, 10) },
	}
	a.tmpl = template.Must(template.New("web").Funcs(funcs).Parse(webTemplates))

	a.mux.HandleFunc("GET /login", a.loginPage)
	a.mux.HandleFunc("POST /login", a.loginSubmit)
	a.mux.HandleFunc("GET /{$}", a.authorize(a.home))
	a.mux.HandleFunc("POST /run", a.authorize(a.startRun))
	a.mux.HandleFunc("GET /api/status", a.authorize(a.apiStatus))
	return a
}

func (a *webApp) Handler() http.Handler { return a.mux }

// serveWeb runs the dashboard until the process is stopped.
func serveWeb(cfg *Config, log *logger, addr string) error {
	app := newWebApp(cfg, log)
	log.info("web UI listening on %s", addr)
	srv := &http.Server{
		Addr:              addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serving web UI: %w", err)
	}
	return nil
}

func (a *webApp) authed(r *http.Request) bool {
	c, err := r.Cookie(webCookieName)
	if err != nil {
		return false
	}
	sum := sha256.Sum256([]byte(c.Value))
	return subtle.ConstantTimeCompare(sum[:], a.tokenHash[:]) == 1
}

func (a *webApp) authorize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !a.authed(r) {
			if r.Method != http.MethodGet {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			a.loginPage(w, r)
			return
		}
		next(w, r)
	}
}

func (a *webApp) loginPage(w http.ResponseWriter, _ *http.Request) {
	a.render(w, http.StatusOK, "login", pageData{Title: "Sign in"})
}

func (a *webApp) loginSubmit(w http.ResponseWriter, r *http.Request) {
	sum := sha256.Sum256([]byte(r.FormValue("token")))
	if subtle.ConstantTimeCompare(sum[:], a.tokenHash[:]) != 1 {
		data := pageData{Title: "Sign in", Error: "Invalid token."}
		a.render(w, http.StatusUnauthorized, "login", data)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     webCookieName,
		Value:    r.FormValue("token"),
		Path:     "/",
		MaxAge:   webCookieAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *webApp) home(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	feeds, err := a.feedsFn(ctx)
	data := a.pageData()
	if err != nil {
		data.Error = "Could not load feeds: " + err.Error()
	}
	data.Feeds = feeds
	a.render(w, http.StatusOK, "home", data)
}

// pageData snapshots the current run and the in-memory run history. Records
// are copied so rendering never reads fields the job goroutine is updating.
func (a *webApp) pageData() pageData {
	a.mu.Lock()
	defer a.mu.Unlock()

	data := pageData{
		Title:   "Kindle Digest",
		Form:    formState{Limit: defaultCollectionLimit, Send: true},
		Running: a.running != nil,
		Refresh: a.running != nil,
	}
	for _, r := range a.runs {
		data.Runs = append(data.Runs, cloneRecord(r))
	}
	if a.running != nil {
		data.Record = cloneRecord(a.running)
	} else if len(data.Runs) > 0 {
		data.Record = data.Runs[0]
	}
	return data
}

func cloneRecord(r *runRecord) *runRecord {
	if r == nil {
		return nil
	}
	c := *r
	c.Parts = append([]partRecord(nil), r.Parts...)
	return &c
}

func (a *webApp) startRun(w http.ResponseWriter, r *http.Request) {
	wantJSON := acceptsJSON(r)
	mode := r.FormValue("mode")

	data := a.pageData()
	var opts options
	var label string

	switch mode {
	case "daily":
		label = "Daily digest · send"
		data.Form.Send = true
	case "custom":
		fs := formState{
			Feed:   strings.TrimSpace(r.FormValue("feed")),
			Since:  strings.TrimSpace(r.FormValue("since")),
			Until:  strings.TrimSpace(r.FormValue("until")),
			Limit:  defaultCollectionLimit,
			Send:   r.FormValue("send") != "",
			Errors: map[string]string{},
		}
		if v := strings.TrimSpace(r.FormValue("limit")); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				fs.Errors["limit"] = "Enter a number from 1 to 500."
			} else {
				fs.Limit = n
			}
		}
		if fs.Limit < 1 || fs.Limit > 500 {
			fs.Errors["limit"] = "Enter a number from 1 to 500."
		}
		since, until := parseFormWindow(fs.Since, fs.Until, fs.Errors)
		opts = options{
			feed:         fs.Feed,
			limit:        fs.Limit,
			send:         fs.Send,
			generateOnly: !fs.Send,
			sinceTime:    since,
			untilTime:    until,
		}
		label = runLabel(fs)
		data.Form = fs
		if len(fs.Errors) > 0 {
			data.Error = "Check the highlighted fields."
			for _, field := range []string{"since", "until", "limit"} {
				if msg := fs.Errors[field]; msg != "" {
					data.Error = msg
					break
				}
			}
			a.renderRunError(w, r, wantJSON, data, http.StatusBadRequest)
			return
		}
	default:
		data.Error = "Unknown run mode."
		a.renderRunError(w, r, wantJSON, data, http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	if a.running != nil {
		a.mu.Unlock()
		data = a.pageData()
		data.Error = "A run is already in progress. Wait for it to finish."
		a.renderRunError(w, r, wantJSON, data, http.StatusConflict)
		return
	}
	rec := &runRecord{
		ID:      newRunID(),
		Started: a.now(),
		Label:   label,
		Send:    opts.send || (mode == "daily" && a.cfg.sendMode()),
		Outcome: "running",
	}
	a.running = rec
	a.mu.Unlock()

	unlock, err := acquireRunLock(a.cfg.OutputDir)
	if err != nil {
		a.finish(rec, nil, err)
		data = a.pageData()
		data.Error = err.Error()
		a.renderRunError(w, r, wantJSON, data, http.StatusConflict)
		return
	}

	go func() {
		defer unlock()
		res, runErr := a.runFn(opts)
		a.finish(rec, res, runErr)
	}()

	data = a.pageData()
	if wantJSON {
		a.writeJSON(w, http.StatusAccepted, map[string]any{
			"id":      rec.ID,
			"status":  a.fragment("statusInner", data),
			"runs":    a.fragment("runsInner", data),
			"running": true,
		})
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (a *webApp) finish(rec *runRecord, res *runResult, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	rec.Outcome = "success"
	if err != nil {
		rec.Outcome = "error"
		rec.Error = err.Error()
	}
	if res != nil {
		rec.Entries = res.Entries
		rec.Mode = res.Mode
		rec.TotalBytes = res.TotalBytes
		rec.DurationMS = res.Duration.Milliseconds()
		for _, pt := range res.Parts {
			rec.Parts = append(rec.Parts, partRecord{Name: filepath.Base(pt.path), Bytes: pt.size})
		}
	}
	a.running = nil
	a.runs = append([]*runRecord{rec}, a.runs...)
	if len(a.runs) > maxWebRuns {
		a.runs = a.runs[:maxWebRuns]
	}
}

func (a *webApp) apiStatus(w http.ResponseWriter, r *http.Request) {
	data := a.pageData()
	a.writeJSON(w, http.StatusOK, map[string]any{
		"running": data.Running,
		"status":  a.fragment("statusInner", data),
		"runs":    a.fragment("runsInner", data),
	})
}

func (a *webApp) renderRunError(w http.ResponseWriter, r *http.Request, wantJSON bool, data pageData, code int) {
	if wantJSON {
		a.writeJSON(w, code, map[string]any{"error": data.Error, "id": ""})
		return
	}
	data.Running = false
	data.Refresh = false
	a.render(w, code, "home", data)
}

func parseFormWindow(sinceText, untilText string, errs map[string]string) (time.Time, time.Time) {
	var since, until time.Time
	if sinceText != "" {
		t, err := time.ParseInLocation("2006-01-02", sinceText, digestLocation)
		if err != nil {
			errs["since"] = "Use YYYY-MM-DD."
		} else {
			since = t
		}
	}
	if untilText != "" {
		if sinceText == "" {
			errs["until"] = "Until needs a since date."
		}
		t, err := time.ParseInLocation("2006-01-02", untilText, digestLocation)
		if err != nil {
			errs["until"] = "Use YYYY-MM-DD."
		} else {
			until = t
		}
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		errs["until"] = "Until must be on or after since."
	}
	return since, until
}

func runLabel(fs formState) string {
	label := "All feeds"
	if fs.Feed != "" {
		label = "Feed " + fs.Feed
	}
	if fs.Since != "" {
		label += " · from " + fs.Since
		if fs.Until != "" {
			label += " to " + fs.Until
		}
	}
	label += " · limit " + strconv.Itoa(fs.Limit)
	if fs.Send {
		return label + " · send"
	}
	return label + " · generate only"
}

func newRunID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func acceptsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

func (a *webApp) render(w http.ResponseWriter, code int, name string, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if err := a.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("web template %s: %v", name, err)
	}
}

func (a *webApp) fragment(name string, data pageData) string {
	var b strings.Builder
	if err := a.tmpl.ExecuteTemplate(&b, name, data); err != nil {
		return ""
	}
	return b.String()
}

func (a *webApp) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("web json: %v", err)
	}
}

func fmtBytes(n int64) string {
	const kb = 1000
	if n < kb {
		return fmt.Sprintf("%d B", n)
	}
	if n < kb*kb {
		return fmt.Sprintf("%.1f KB", float64(n)/kb)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/(kb*kb))
}
