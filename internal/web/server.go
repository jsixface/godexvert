// Package web wires HTTP routes, templates and static assets.
package web

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jsixface/godexvert/internal/config"
	"github.com/jsixface/godexvert/internal/convert"
	"github.com/jsixface/godexvert/internal/events"
	"github.com/jsixface/godexvert/internal/library"
	"github.com/jsixface/godexvert/internal/store"
	assets "github.com/jsixface/godexvert/web"
)

// Watcher is restarted when settings change.
type Watcher interface {
	Start(ctx context.Context) error
}

type Deps struct {
	Log     *slog.Logger
	Cfg     *config.Store
	Store   *store.Store
	Lib     *library.Library
	Queue   *convert.Queue
	Bus     *events.Broker
	Watcher Watcher
}

type Server struct {
	Deps
	ctx      context.Context // app lifetime, for background work started by requests
	base     *template.Template
	pages    map[string]*template.Template
	mux      *http.ServeMux
	scanning atomic.Bool
}

// New builds the server. ctx bounds background work (scans) and open SSE streams.
func New(ctx context.Context, d Deps) (*Server, error) {
	s := &Server{Deps: d, ctx: ctx, pages: map[string]*template.Template{}, mux: http.NewServeMux()}
	base, err := template.New("").Funcs(s.funcs()).ParseFS(assets.FS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	s.base = base
	names, _ := fs.Glob(assets.FS, "templates/pages/*.html")
	for _, n := range names {
		t, err := template.Must(base.Clone()).ParseFS(assets.FS, n)
		if err != nil {
			return nil, err
		}
		s.pages[strings.TrimSuffix(path.Base(n), ".html")] = t
	}
	s.routes()
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	static, _ := fs.Sub(assets.FS, "static")
	s.mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	s.mux.HandleFunc("GET /events", s.events)

	s.mux.HandleFunc("GET /{$}", s.library)
	s.mux.HandleFunc("GET /videos/detail", s.detail)
	s.mux.HandleFunc("POST /videos/convert", s.convert)
	s.mux.HandleFunc("POST /videos/refresh", s.refresh)

	s.mux.HandleFunc("GET /jobs", s.jobs)
	s.mux.HandleFunc("DELETE /jobs/{id}", s.cancelJob)

	s.mux.HandleFunc("GET /backups", s.backups)
	s.mux.HandleFunc("DELETE /backups", s.deleteAllBackups)
	s.mux.HandleFunc("DELETE /backups/item", s.deleteBackup)

	s.mux.HandleFunc("GET /settings", s.settings)
	s.mux.HandleFunc("POST /settings", s.saveSettings)
	s.mux.HandleFunc("GET /settings/autoconv-row", s.autoconvRow)
}

type page struct {
	Title, Nav string
	Jobs       []convert.Job // active jobs, for the header indicator
	Data       any
}

// isFragment reports whether htmx asked for a partial (not a boosted navigation or history restore).
func isFragment(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Boosted") != "true" &&
		r.Header.Get("HX-History-Restore-Request") != "true"
}

// render writes the named fragment for htmx partial requests, or the full page otherwise.
func (s *Server) render(w http.ResponseWriter, r *http.Request, name, fragment, title string, data any) {
	if isFragment(r) {
		s.fragment(w, fragment, data)
		return
	}
	var buf bytes.Buffer
	if err := s.pages[name].ExecuteTemplate(&buf, "layout", page{Title: title, Nav: name, Jobs: s.Queue.Jobs(), Data: data}); err != nil {
		s.Log.Error("render page", "page", name, "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

func (s *Server) fragment(w http.ResponseWriter, name string, data any) {
	html, err := s.renderString(name, data)
	if err != nil {
		s.Log.Error("render fragment", "name", name, "err", err)
		http.Error(w, "render error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(html))
}

func (s *Server) renderString(name string, data any) (string, error) {
	var buf bytes.Buffer
	err := s.base.ExecuteTemplate(&buf, name, data)
	return buf.String(), err
}

type toast struct{ Msg, Kind string }

// Toast pushes a notification to every connected browser.
func (s *Server) Toast(msg, kind string) {
	s.publish("toast", "toast", toast{msg, kind})
}

func (s *Server) publish(event, fragment string, data any) {
	if s.Bus.Subscribers() == 0 {
		return
	}
	html := ""
	if fragment != "" {
		var err error
		if html, err = s.renderString(fragment, data); err != nil {
			s.Log.Error("render event", "event", event, "err", err)
			return
		}
	}
	s.Bus.Publish(events.Event{Name: event, Data: html})
}

// JobUpdated pushes a job's new status/progress to the jobs page and the header.
func (s *Server) JobUpdated(j convert.Job) {
	s.publish("job-"+j.ID, "job", j)
	s.publish("jobs-indicator", "jobs-indicator", s.Queue.Jobs())
	if j.Status.Active() {
		s.publish("running-pct", "pct", j)
	}
}

// JobsChanged tells the jobs page to reload its list and refreshes the header.
func (s *Server) JobsChanged() {
	s.publish("jobs-changed", "", nil)
	s.publish("jobs-indicator", "jobs-indicator", s.Queue.Jobs())
}

// LibraryChanged tells the library page to reload its table.
func (s *Server) LibraryChanged() { s.publish("library-changed", "", nil) }

// Rescan scans the library in the background unless a scan is already running.
// It reports whether a scan was started.
func (s *Server) Rescan() bool {
	if !s.scanning.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer s.scanning.Store(false)
		changed, err := s.Lib.Scan(s.ctx)
		switch {
		case err != nil && s.ctx.Err() == nil:
			s.Log.Error("scan", "err", err)
			s.Toast("Library scan failed: "+err.Error(), "err")
		case err == nil:
			s.Toast(fmt.Sprintf("Library scan finished: %d files", len(s.Lib.Files())), "")
		}
		if changed {
			s.LibraryChanged()
		}
	}()
	return true
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	ch, unsubscribe := s.Bus.Subscribe()
	defer unsubscribe()

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")
	if err := rc.Flush(); err != nil {
		return
	}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.ctx.Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
		case e := <-ch:
			writeEvent(w, e)
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

func writeEvent(w http.ResponseWriter, e events.Event) {
	fmt.Fprintf(w, "event: %s\n", e.Name)
	for line := range strings.SplitSeq(e.Data, "\n") {
		fmt.Fprintf(w, "data: %s\n", strings.TrimRight(line, "\r"))
	}
	fmt.Fprint(w, "\n")
}
