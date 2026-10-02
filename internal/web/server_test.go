package web

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jsixface/godexvert/internal/config"
	"github.com/jsixface/godexvert/internal/convert"
	"github.com/jsixface/godexvert/internal/events"
	"github.com/jsixface/godexvert/internal/library"
	"github.com/jsixface/godexvert/internal/media"
	"github.com/jsixface/godexvert/internal/store"
)

type nopWatcher struct{ starts int }

func (w *nopWatcher) Start(context.Context) error { w.starts++; return nil }

type env struct {
	srv *Server
	ts  *httptest.Server
	dir string
	w   *nopWatcher
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "movie & co.mkv"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "old.mkv.1700000000.bkp"), []byte("x"), 0o644)

	cfg, _ := config.Load(filepath.Join(t.TempDir(), config.FileName))
	set := config.Default()
	set.LibraryLocations = []string{dir}
	cfg.Save(set)
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	probe := func(context.Context, string) ([]media.Track, error) {
		return []media.Track{
			{Kind: media.Video, Index: 0, Codec: "h264", Resolution: "1920x1080"},
			{Kind: media.Audio, Index: 1, Codec: "ac3", Language: "eng", Channels: 6},
			{Kind: media.Subtitle, Index: 2, Codec: "subrip"},
		}, nil
	}
	lib := library.New(st, cfg, probe, log)
	if _, err := lib.Scan(ctx); err != nil {
		t.Fatal(err)
	}
	// The queue is never started, so enqueued jobs stay queued.
	q := convert.NewQueue(st, cfg, nil, log)
	w := &nopWatcher{}
	srv, err := New(ctx, Deps{Log: log, Cfg: cfg, Store: st, Lib: lib, Queue: q, Bus: events.NewBroker(), Watcher: w})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &env{srv: srv, ts: ts, dir: dir, w: w}
}

func (e *env) do(t *testing.T, method, path string, form url.Values, htmx bool) (int, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestPagesRender(t *testing.T) {
	e := setup(t)
	for path, want := range map[string]string{
		"/":         "movie &amp; co.mkv",
		"/jobs":     "Nothing converting",
		"/backups":  "old.mkv.1700000000.bkp",
		"/settings": "Library locations",
		"/healthz":  "ok",
	} {
		code, body := e.do(t, "GET", path, nil, false)
		if code != 200 || !strings.Contains(body, want) {
			t.Errorf("GET %s: %d, missing %q", path, code, want)
		}
		if path != "/healthz" && !strings.Contains(body, "<!doctype html>") {
			t.Errorf("GET %s: not a full page", path)
		}
	}
}

func TestLibraryFilterFragment(t *testing.T) {
	e := setup(t)
	_, body := e.do(t, "GET", "/?audio=ac3", nil, true)
	if strings.Contains(body, "<!doctype") || !strings.Contains(body, `id="videos"`) || !strings.Contains(body, "movie") {
		t.Fatalf("fragment: %s", body)
	}
	_, body = e.do(t, "GET", "/?audio=aac", nil, true)
	if strings.Contains(body, "movie") {
		t.Fatal("filter should exclude the file")
	}
}

func TestDetailAndConvert(t *testing.T) {
	e := setup(t)
	p := filepath.Join(e.dir, "movie & co.mkv")
	code, body := e.do(t, "GET", "/videos/detail?path="+url.QueryEscape(p), nil, true)
	if code != 200 || !strings.Contains(body, `value="convert:AAC"`) || strings.Contains(body, `value="convert:AC3"`) {
		t.Fatalf("detail %d: %s", code, body)
	}

	// Nothing selected.
	_, body = e.do(t, "POST", "/videos/convert", url.Values{"path": {p}}, true)
	if !strings.Contains(body, "Nothing to do") {
		t.Fatalf("expected nothing-to-do error: %s", body)
	}
	// Wrong kind for track.
	code, _ = e.do(t, "POST", "/videos/convert", url.Values{"path": {p}, "t0": {"convert:AAC"}}, true)
	if code != 400 {
		t.Fatalf("expected 400 for audio codec on video track, got %d", code)
	}
	code, body = e.do(t, "POST", "/videos/convert", url.Values{"path": {p}, "t1": {"convert:AAC"}, "t2": {"drop"}}, true)
	if code != 200 || !strings.Contains(body, "Queued") || !strings.Contains(body, "hx-swap-oob") {
		t.Fatalf("convert %d: %s", code, body)
	}
	jobs := e.srv.Queue.Jobs()
	if len(jobs) != 1 || jobs[0].Specs[1].Codec.Name != "AAC" || jobs[0].Specs[2].Op != media.Drop {
		t.Fatalf("jobs %+v", jobs)
	}
	_, body = e.do(t, "GET", "/jobs", nil, true)
	if !strings.Contains(body, "ac3 #1 → AAC") {
		t.Fatalf("jobs fragment: %s", body)
	}
	code, _ = e.do(t, "DELETE", "/jobs/"+jobs[0].ID, nil, true)
	if code != 204 || len(e.srv.Queue.Jobs()) != 0 {
		t.Fatalf("cancel %d", code)
	}
	if code, _ = e.do(t, "DELETE", "/jobs/nope", nil, true); code != 404 {
		t.Fatalf("cancel unknown %d", code)
	}
}

func TestPathValidation(t *testing.T) {
	e := setup(t)
	for _, p := range []string{"/etc/passwd", filepath.Join(e.dir, "..", "x.mkv"), "relative.mkv"} {
		if code, _ := e.do(t, "GET", "/videos/detail?path="+url.QueryEscape(p), nil, true); code != 400 {
			t.Errorf("detail %s: %d", p, code)
		}
	}
	if code, _ := e.do(t, "GET", "/videos/detail?path="+url.QueryEscape(filepath.Join(e.dir, "missing.mkv")), nil, true); code != 404 {
		t.Errorf("missing file: %d", code)
	}
	if code, _ := e.do(t, "DELETE", "/backups/item?path="+url.QueryEscape(filepath.Join(e.dir, "movie & co.mkv")), nil, true); code != 400 {
		t.Errorf("deleting a non-backup must fail: %d", code)
	}
}

func TestBackups(t *testing.T) {
	e := setup(t)
	bkp := filepath.Join(e.dir, "old.mkv.1700000000.bkp")
	if code, _ := e.do(t, "DELETE", "/backups/item?path="+url.QueryEscape(bkp), nil, true); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	if _, err := os.Stat(bkp); !os.IsNotExist(err) {
		t.Fatal("backup still exists")
	}
	_, body := e.do(t, "DELETE", "/backups", nil, true)
	if !strings.Contains(body, "Deleted 0 backups") {
		t.Fatalf("delete all: %s", body)
	}
}

func TestSettings(t *testing.T) {
	e := setup(t)
	form := url.Values{
		"locations": {e.dir + "\n\n" + e.dir + "/\n"}, "extensions": {"MKV, .mp4,"}, "workspace": {"/tmp/ws"},
		"from": {"AC3", "AAC"}, "to": {"AAC", "AAC"},
	}
	code, body := e.do(t, "POST", "/settings", form, true)
	if code != 200 || !strings.Contains(body, "Settings saved") {
		t.Fatalf("save %d: %s", code, body)
	}
	got := e.srv.Cfg.Get()
	if len(got.LibraryLocations) != 1 || strings.Join(got.VideoExtensions, ",") != "mkv,mp4" || got.TakeBackups ||
		len(got.AutoConversion.Conversion) != 1 || got.AutoConversion.Conversion["AC3"] != "AAC" {
		t.Fatalf("saved %+v", got)
	}
	if e.w.starts != 1 {
		t.Fatal("watcher should restart")
	}

	form = url.Values{"locations": {"relative"}, "extensions": {""}, "workspace": {"ws"}, "from": {"AC3"}, "to": {"HEVC"}}
	_, body = e.do(t, "POST", "/settings", form, true)
	for _, want := range []string{"absolute path", "at least one video extension", "Workspace must", "Unknown audio codec"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing error %q", want)
		}
	}
	if e.srv.Cfg.Get().VideoExtensions[0] != "mkv" {
		t.Fatal("invalid settings must not be saved")
	}
	// Without htmx, a successful save redirects.
	req, _ := http.NewRequest("POST", e.ts.URL+"/settings", strings.NewReader(url.Values{"locations": {e.dir}, "extensions": {"mkv"}, "workspace": {"/tmp"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil || resp.StatusCode != 303 {
		t.Fatalf("redirect: %v %v", resp, err)
	}
}

func TestSSE(t *testing.T) {
	e := setup(t)
	resp, err := http.Get(e.ts.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	r := bufio.NewReader(resp.Body)
	r.ReadString('\n') // ": connected"
	r.ReadString('\n')
	deadline := time.Now().Add(2 * time.Second)
	for e.srv.Bus.Subscribers() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	e.srv.JobUpdated(convert.Job{ID: "abc", File: media.File{Name: "x.mkv"}, Status: convert.InProgress, Progress: 42})
	var lines []string
	for {
		l, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if l == "\n" {
			break
		}
		lines = append(lines, l)
	}
	if lines[0] != "event: job-abc\n" || !strings.Contains(strings.Join(lines, ""), `width: 42%`) {
		t.Fatalf("event: %q", lines)
	}
	for _, l := range lines[1:] {
		if !strings.HasPrefix(l, "data: ") {
			t.Fatalf("bad line %q", l)
		}
	}
}

func TestLibrarySortAndIndicator(t *testing.T) {
	e := setup(t)
	os.WriteFile(filepath.Join(e.dir, "big.mkv"), make([]byte, 2<<20), 0o644)
	e.srv.Lib.Scan(context.Background())
	_, body := e.do(t, "GET", "/?sort=size&dir=desc", nil, true)
	big, small := strings.Index(body, "big.mkv"), strings.Index(body, "movie &amp; co.mkv")
	if big < 0 || small < 0 || big > small {
		t.Fatalf("size desc order wrong (big=%d small=%d)", big, small)
	}
	if !strings.Contains(body, `id="sort-state" hx-swap-oob="true"`) || !strings.Contains(body, `value="desc"`) {
		t.Fatal("fragment should refresh the form's sort state out of band")
	}
	if !strings.Contains(body, `class="badge r-fhd">1080p`) || !strings.Contains(body, `class="badge c-warn">ac3`) {
		t.Fatal("missing quality/codec badges")
	}
	// Full pages render the sort state once, without OOB.
	_, page := e.do(t, "GET", "/?sort=size&dir=desc", nil, false)
	if strings.Count(page, `id="sort-state"`) != 1 || strings.Contains(page, "hx-swap-oob") {
		t.Fatal("full page sort state")
	}
	if !strings.Contains(page, `id="jobs-indicator"`) || !strings.Contains(page, "Idle") {
		t.Fatal("header indicator missing")
	}
	e.srv.Queue.Enqueue(media.File{Path: "/x.mkv", Name: "x.mkv"}, nil)
	_, page = e.do(t, "GET", "/", nil, false)
	if !strings.Contains(page, "1 queued") {
		t.Fatal("indicator should count queued jobs")
	}
}
