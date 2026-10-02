package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jsixface/godexvert/internal/backups"
	"github.com/jsixface/godexvert/internal/config"
	"github.com/jsixface/godexvert/internal/convert"
	"github.com/jsixface/godexvert/internal/library"
	"github.com/jsixface/godexvert/internal/media"
	"github.com/jsixface/godexvert/internal/store"
)

// ---- Library ----

type libraryData struct {
	Filter                   library.Filter
	Sort                     string
	Desc                     bool
	Columns                  []column
	Files                    []media.File
	Total                    int
	SizeMB                   int64
	VideoCodecs, AudioCodecs []string
	OOB                      bool // fragment response: also refresh the form's sort state
}

// column is a sortable library table header.
type column struct {
	Key, Label, Class string
	Active, Desc      bool
	Href, Vals        string // link to the column sorted in its next direction
}

var columns = []struct {
	key, label, class string
	desc              bool // default direction
}{
	{library.SortName, "Name", "", false},
	{library.SortRes, "Quality", "", true},
	{"", "Video", "", false},
	{"", "Audio", "", false},
	{"", "Subs", "hide-sm", false},
	{library.SortAdded, "Added", "hide-sm", true},
	{library.SortSize, "Size", "num", true},
}

func (s *Server) library(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := library.Filter{Name: strings.TrimSpace(q.Get("name")), Video: q.Get("video"), Audio: q.Get("audio")}
	sortKey, desc := q.Get("sort"), q.Get("dir") == "desc"
	if !slices.Contains([]string{library.SortSize, library.SortRes, library.SortAdded}, sortKey) {
		sortKey = library.SortName
	}
	files := s.Lib.Find(f)
	library.SortFiles(files, sortKey, desc)

	d := libraryData{Filter: f, Sort: sortKey, Desc: desc, Files: files, Total: len(s.Lib.Files()),
		VideoCodecs: s.Lib.Codecs(media.Video), AudioCodecs: s.Lib.Codecs(media.Audio), OOB: isFragment(r)}
	for _, v := range files {
		d.SizeMB += v.SizeMB
	}
	for _, c := range columns {
		col := column{Key: c.key, Label: c.label, Class: c.class}
		if c.key != "" {
			col.Active = c.key == sortKey
			col.Desc = desc
			next := c.desc
			if col.Active {
				next = !desc
			}
			dir := "asc"
			if next {
				dir = "desc"
			}
			v := url.Values{"sort": {c.key}, "dir": {dir}}
			for k, x := range map[string]string{"name": f.Name, "video": f.Video, "audio": f.Audio} {
				if x != "" {
					v.Set(k, x)
				}
			}
			col.Href = "/?" + v.Encode()
			col.Vals = fmt.Sprintf(`{"sort":%q,"dir":%q}`, c.key, dir)
		}
		d.Columns = append(d.Columns, col)
	}
	s.render(w, r, "library", "videos", "Library", d)
}

type detailData struct {
	File     media.File
	Selected map[int]string // track index -> chosen action
	Queued   bool
	Error    string
}

// lookup finds a library file by path, rejecting paths outside the configured locations.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request, path string) *media.File {
	if !library.Within(path, s.Cfg.Get().LibraryLocations) {
		http.Error(w, "Path is not inside a library location", http.StatusBadRequest)
		return nil
	}
	f, err := s.Lib.Get(r.Context(), path)
	if err != nil {
		s.Log.Error("lookup", "path", path, "err", err)
		http.Error(w, "Lookup failed", http.StatusInternalServerError)
		return nil
	}
	if f == nil {
		http.Error(w, "File is not in the library; try a rescan", http.StatusNotFound)
	}
	return f
}

func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	f := s.lookup(w, r, r.URL.Query().Get("path"))
	if f == nil {
		return
	}
	sel := map[int]string{}
	for _, t := range f.Tracks {
		sel[t.Index] = "copy"
	}
	s.fragment(w, "detail", detailData{File: *f, Selected: sel, Queued: s.Queue.Has(f.Path)})
}

func (s *Server) convert(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f := s.lookup(w, r, r.PostForm.Get("path"))
	if f == nil {
		return
	}
	d := detailData{File: *f, Selected: map[int]string{}}
	specs := media.Specs{}
	kept := 0
	for _, t := range f.Tracks {
		v := r.PostForm.Get("t" + strconv.Itoa(t.Index))
		a, ok := media.ParseAction(v)
		if !ok || (a.Op == media.Convert && a.Codec.Kind != t.Kind) {
			http.Error(w, fmt.Sprintf("Invalid action %q for track %d", v, t.Index), http.StatusBadRequest)
			return
		}
		d.Selected[t.Index] = a.String()
		if a.Op != media.Copy {
			specs[t.Index] = a
		}
		if a.Op != media.Drop {
			kept++
		}
	}
	switch {
	case s.Queue.Has(f.Path):
		d.Queued = true
	case len(specs) == 0:
		d.Error = "Nothing to do: choose a track to convert or delete."
	case kept == 0:
		d.Error = "At least one track must be kept."
	default:
		s.Queue.Enqueue(*f, specs)
		d.Queued = true
		html, _ := s.renderString("toast-oob", toast{Msg: "Queued " + f.Name})
		s.fragment(w, "detail", d)
		w.Write([]byte(html))
		return
	}
	s.fragment(w, "detail", d)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	if s.Rescan() {
		s.fragment(w, "toast-oob", toast{Msg: "Scanning library…"})
	} else {
		s.fragment(w, "toast-oob", toast{Msg: "A scan is already running"})
	}
}

// ---- Jobs ----

var jobLimits = []int{10, 20, 50, 100}

type jobsData struct {
	Active             []convert.Job
	History            []store.JobRecord
	Counts             map[string]int // finished jobs per status
	Page, Pages, Limit int
	Limits             []int
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if !slices.Contains(jobLimits, limit) {
		limit = jobLimits[0]
	}
	total, err := s.Store.CountJobs(r.Context())
	if err != nil {
		s.Log.Error("count jobs", "err", err)
	}
	pages := max(1, (total+limit-1)/limit)
	pg, _ := strconv.Atoi(q.Get("page"))
	pg = min(max(pg, 1), pages)
	hist, err := s.Store.Jobs(r.Context(), (pg-1)*limit, limit)
	if err != nil {
		s.Log.Error("list jobs", "err", err)
	}
	counts, err := s.Store.CountJobsByStatus(r.Context())
	if err != nil {
		s.Log.Error("count jobs by status", "err", err)
	}
	s.render(w, r, "jobs", "jobs", "Jobs", jobsData{
		Active: s.Queue.Jobs(), History: hist, Counts: counts, Page: pg, Pages: pages, Limit: limit, Limits: jobLimits,
	})
}

func (s *Server) cancelJob(w http.ResponseWriter, r *http.Request) {
	if !s.Queue.Cancel(r.PathValue("id")) {
		http.Error(w, "Job not found; it may have finished", http.StatusNotFound)
		return
	}
	w.Header().Set("HX-Trigger", "jobs-changed")
	w.WriteHeader(http.StatusNoContent)
}

// ---- Backups ----

type backupsData struct {
	Backups []backups.Backup
	SizeMB  int64
}

func listBackups(locs []string) backupsData {
	d := backupsData{Backups: backups.List(locs)}
	for _, b := range d.Backups {
		d.SizeMB += b.SizeMB
	}
	return d
}

func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "backups", "backups", "Backups", listBackups(s.Cfg.Get().LibraryLocations))
}

func (s *Server) deleteAllBackups(w http.ResponseWriter, r *http.Request) {
	locs := s.Cfg.Get().LibraryLocations
	n, err := backups.DeleteAll(locs)
	s.Log.Info("deleted backups", "count", n, "err", err)
	msg := fmt.Sprintf("Deleted %d backups", n)
	if err != nil {
		msg += " (some could not be deleted)"
	}
	s.fragment(w, "backups", listBackups(locs))
	html, _ := s.renderString("toast-oob", toast{Msg: msg})
	w.Write([]byte(html))
}

func (s *Server) deleteBackup(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	err := backups.Delete(s.Cfg.Get().LibraryLocations, p)
	switch {
	case errors.Is(err, backups.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case err != nil:
		s.Log.Error("delete backup", "path", p, "err", err)
		http.Error(w, "Could not delete backup", http.StatusInternalServerError)
	default:
		s.Log.Info("deleted backup", "path", p)
		w.WriteHeader(http.StatusOK) // empty body removes the row
	}
}

// ---- Settings ----

type settingsData struct {
	S      config.Settings
	Saved  bool
	Errors []string
}

func (s *Server) settings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "settings", "settings", "Settings", settingsData{S: s.Cfg.Get()})
}

func (s *Server) autoconvRow(w http.ResponseWriter, r *http.Request) {
	s.fragment(w, "autoconv-row", autoconvRule{})
}

func (s *Server) saveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	set, errs := parseSettings(r.PostForm)
	if len(errs) > 0 {
		// Respond 200 (htmx only swaps 2xx); the errors are shown in the form.
		s.render(w, r, "settings", "settings", "Settings", settingsData{S: set, Errors: errs})
		return
	}
	old := s.Cfg.Get()
	if err := s.Cfg.Save(set); err != nil {
		s.Log.Error("save settings", "err", err)
		s.render(w, r, "settings", "settings", "Settings", settingsData{S: set, Errors: []string{"Could not save: " + err.Error()}})
		return
	}
	s.Log.Info("settings saved", "settings", fmt.Sprintf("%+v", set))
	if err := s.Watcher.Start(s.ctx); err != nil {
		s.Log.Error("restart watcher", "err", err)
	}
	if !slices.Equal(old.LibraryLocations, set.LibraryLocations) || !slices.Equal(old.VideoExtensions, set.VideoExtensions) {
		s.Rescan()
	}
	if !isFragment(r) {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	s.fragment(w, "settings", settingsData{S: set, Saved: true})
}

// parseSettings validates the settings form. On error it returns the submitted values so the
// form can be shown again.
func parseSettings(form map[string][]string) (config.Settings, []string) {
	get := func(k string) string {
		if v := form[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	var errs []string
	set := config.Settings{
		LibraryLocations:  []string{},
		VideoExtensions:   []string{},
		WorkspaceLocation: strings.TrimSpace(get("workspace")),
		TakeBackups:       get("takeBackups") != "",
		AutoConversion:    config.AutoConversion{Conversion: map[string]string{}},
	}
	for _, l := range strings.Split(get("locations"), "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		if !filepath.IsAbs(l) {
			errs = append(errs, fmt.Sprintf("Location %q must be an absolute path", l))
			continue
		}
		l = filepath.Clean(l)
		if !slices.Contains(set.LibraryLocations, l) {
			set.LibraryLocations = append(set.LibraryLocations, l)
		}
	}
	for _, e := range strings.Split(get("extensions"), ",") {
		e = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(e), "."))
		if e != "" && !slices.Contains(set.VideoExtensions, e) {
			set.VideoExtensions = append(set.VideoExtensions, e)
		}
	}
	if len(set.VideoExtensions) == 0 {
		errs = append(errs, "Add at least one video extension")
	}
	if !filepath.IsAbs(set.WorkspaceLocation) {
		errs = append(errs, "Workspace must be an absolute path")
	}
	from, to := form["from"], form["to"]
	for i := range min(len(from), len(to)) {
		f, ok1 := media.LookupCodec(from[i])
		t, ok2 := media.LookupCodec(to[i])
		switch {
		case !ok1 || !ok2 || f.Kind != media.Audio || t.Kind != media.Audio:
			errs = append(errs, fmt.Sprintf("Unknown audio codec in rule %s → %s", from[i], to[i]))
		case f.Name == t.Name:
			// A no-op rule; drop it.
		case set.AutoConversion.Conversion[f.Name] != "":
			errs = append(errs, fmt.Sprintf("Duplicate rule for %s", f.Name))
		default:
			set.AutoConversion.Conversion[f.Name] = t.Name
		}
	}
	return set, errs
}
