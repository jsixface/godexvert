// Package library scans the configured locations, probes video files and keeps the store in sync.
package library

import (
	"cmp"
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jsixface/godexvert/internal/config"
	"github.com/jsixface/godexvert/internal/media"
	"github.com/jsixface/godexvert/internal/probe"
	"github.com/jsixface/godexvert/internal/store"
)

// ProbeFunc returns the tracks of the file at path.
type ProbeFunc func(ctx context.Context, path string) ([]media.Track, error)

// FFProbe probes with the ffprobe binary.
func FFProbe(ctx context.Context, path string) ([]media.Track, error) {
	in, err := probe.Run(ctx, path)
	if err != nil {
		return nil, err
	}
	return in.Tracks(), nil
}

const probeWorkers = 4

type Library struct {
	store *store.Store
	cfg   *config.Store
	probe ProbeFunc
	log   *slog.Logger

	scanMu sync.Mutex // one scan at a time

	mu    sync.RWMutex
	cache []media.File
}

func New(st *store.Store, cfg *config.Store, probe ProbeFunc, log *slog.Logger) *Library {
	return &Library{store: st, cfg: cfg, probe: probe, log: log}
}

// Load fills the in-memory cache from the store.
func (l *Library) Load(ctx context.Context) error {
	files, err := l.store.Files(ctx)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.cache = files
	l.mu.Unlock()
	return nil
}

// Files returns the cached library, ordered by name.
func (l *Library) Files() []media.File {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cache
}

// Get returns the stored file at path, or nil.
func (l *Library) Get(ctx context.Context, path string) (*media.File, error) {
	return l.store.File(ctx, path)
}

// Filter selects files whose name contains name (case-insensitive) and that have a
// video/audio track with the given codecs. Empty criteria match everything.
type Filter struct {
	Name, Video, Audio string
}

func (f Filter) Match(v media.File) bool {
	return (f.Name == "" || strings.Contains(strings.ToLower(v.Name), strings.ToLower(f.Name))) &&
		(f.Video == "" || v.HasCodec(media.Video, f.Video)) &&
		(f.Audio == "" || v.HasCodec(media.Audio, f.Audio))
}

func (l *Library) Find(f Filter) []media.File {
	var out []media.File
	for _, v := range l.Files() {
		if f.Match(v) {
			out = append(out, v)
		}
	}
	return out
}

// Codecs lists the distinct (lower-cased) codecs of kind k across the library, sorted.
func (l *Library) Codecs(k media.Kind) []string {
	seen := map[string]bool{}
	for _, f := range l.Files() {
		for _, t := range f.OfKind(k) {
			seen[strings.ToLower(t.Codec)] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	slices.Sort(out)
	return out
}

// Scan walks the library locations, probes new and modified files and drops vanished ones.
// It reports whether anything changed.
func (l *Library) Scan(ctx context.Context) (bool, error) {
	l.scanMu.Lock()
	defer l.scanMu.Unlock()
	start := time.Now()
	set := l.cfg.Get()

	found := map[string]int64{} // path -> modified millis
	var unreadable []string
	for _, loc := range set.LibraryLocations {
		err := walk(loc, func(path string, info fs.FileInfo) {
			if MatchesExt(path, set.VideoExtensions) {
				found[path] = info.ModTime().UnixMilli()
			}
		})
		if err != nil {
			// Keep what we know about an unreachable location (e.g. an unmounted share).
			l.log.Warn("scan location", "loc", loc, "err", err)
			unreadable = append(unreadable, loc)
		}
	}
	stamps, err := l.store.Stamps(ctx)
	if err != nil {
		return false, err
	}

	var changed []string
	for p, mod := range found {
		if st, ok := stamps[p]; !ok || st.Modified != mod {
			changed = append(changed, p)
		}
	}
	var deleted []int64
	for p, st := range stamps {
		if _, ok := found[p]; !ok && !Within(p, unreadable) {
			deleted = append(deleted, st.ID)
		}
	}
	l.log.Info("scan", "files", len(found), "changed", len(changed), "deleted", len(deleted))

	updated := l.probeAll(ctx, changed)
	if err := l.store.Delete(ctx, deleted...); err != nil {
		return false, err
	}
	didChange := updated > 0 || len(deleted) > 0
	if didChange {
		if err := l.Load(ctx); err != nil {
			return true, err
		}
	}
	l.log.Info("scan done", "took", time.Since(start).Round(time.Millisecond), "updated", updated)
	return didChange, ctx.Err()
}

// probeAll probes and stores paths concurrently, returning how many were stored.
func (l *Library) probeAll(ctx context.Context, paths []string) int {
	ch := make(chan string)
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for range min(probeWorkers, len(paths)) {
		wg.Go(func() {
			for p := range ch {
				if err := l.update(ctx, p); err != nil {
					l.log.Warn("probe", "path", p, "err", err)
					continue
				}
				mu.Lock()
				n++
				mu.Unlock()
			}
		})
	}
	for _, p := range paths {
		if ctx.Err() != nil {
			break
		}
		ch <- p
	}
	close(ch)
	wg.Wait()
	return n
}

// Refresh re-probes a single file (e.g. after a conversion) and updates the cache.
func (l *Library) Refresh(ctx context.Context, path string) error {
	if err := l.update(ctx, path); err != nil {
		return err
	}
	return l.Load(ctx)
}

func (l *Library) update(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tracks, err := l.probe(ctx, path)
	if err != nil {
		return err
	}
	return l.store.Upsert(ctx, media.File{
		Path:     path,
		Name:     filepath.Base(path),
		SizeMB:   info.Size() / 1024 / 1024,
		Modified: info.ModTime().UnixMilli(),
		Added:    time.Now().UnixMilli(),
		Tracks:   tracks,
	})
}

// MatchesExt reports whether path has one of exts (case-insensitive, leading dot optional).
func MatchesExt(path string, exts []string) bool {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	if ext == "" {
		return false
	}
	return slices.ContainsFunc(exts, func(e string) bool {
		return strings.EqualFold(strings.TrimPrefix(strings.TrimSpace(e), "."), ext)
	})
}

// Within reports whether path is inside one of roots (after cleaning, symlinks not resolved).
func Within(path string, roots []string) bool {
	if !filepath.IsAbs(path) {
		return false
	}
	path = filepath.Clean(path)
	for _, r := range roots {
		r = filepath.Clean(r)
		if rel, err := filepath.Rel(r, path); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
			return true
		}
	}
	return false
}

// walk visits regular files under root, following symlinks (with cycle protection).
// Unreadable subdirectories are skipped; only an unreadable root is an error.
func walk(root string, fn func(path string, info fs.FileInfo)) error {
	root = filepath.Clean(root)
	if _, err := os.Stat(root); err != nil {
		return err
	}
	visited := map[string]bool{}
	var visit func(dir string)
	visit = func(dir string) {
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || visited[real] {
			return
		}
		visited[real] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			info, err := os.Stat(p) // follows symlinks
			if err != nil {
				continue
			}
			switch {
			case info.IsDir():
				visit(p)
			case info.Mode().IsRegular():
				fn(p, info)
			}
		}
	}
	visit(root)
	return nil
}

// Sort keys accepted by SortFiles.
const (
	SortName  = "name"
	SortSize  = "size"
	SortRes   = "res"
	SortAdded = "added"
)

// SortFiles orders files in place by key (name, size, res or added), ties broken by name.
func SortFiles(files []media.File, key string, desc bool) {
	slices.SortStableFunc(files, func(a, b media.File) int {
		var c int
		switch key {
		case SortSize:
			c = cmp.Compare(a.SizeMB, b.SizeMB)
		case SortRes:
			c = cmp.Compare(Pixels(a), Pixels(b))
		case SortAdded:
			c = cmp.Compare(a.Added, b.Added)
		}
		if c == 0 {
			c = cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}
		if desc {
			return -c
		}
		return c
	})
}

// Pixels is the frame size of the file's first video track, or 0.
func Pixels(f media.File) int {
	w, h := Dimensions(f)
	return w * h
}

// Dimensions parses the first video track's "WxH" resolution.
func Dimensions(f media.File) (w, h int) {
	for _, t := range f.Videos() {
		ws, hs, ok := strings.Cut(t.Resolution, "x")
		if !ok {
			continue
		}
		w, _ = strconv.Atoi(ws)
		h, _ = strconv.Atoi(hs)
		if w > 0 && h > 0 {
			return w, h
		}
	}
	return 0, 0
}
