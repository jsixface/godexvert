// Package watcher rescans the library when video files change and queues automatic conversions.
package watcher

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/jsixface/godexvert/internal/config"
	"github.com/jsixface/godexvert/internal/library"
	"github.com/jsixface/godexvert/internal/media"
)

type Library interface {
	Scan(ctx context.Context) (bool, error)
	Get(ctx context.Context, path string) (*media.File, error)
}

// Watcher watches the library locations recursively. Changes to video files are collected
// until the tree has been quiet for Debounce, then the library is rescanned and changed
// files are auto-converted according to the settings.
type Watcher struct {
	cfg      *config.Store
	lib      Library
	enqueue  func(media.File, media.Specs)
	has      func(string) bool
	log      *slog.Logger
	Debounce time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func New(cfg *config.Store, lib Library, enqueue func(media.File, media.Specs), has func(string) bool, log *slog.Logger) *Watcher {
	return &Watcher{cfg: cfg, lib: lib, enqueue: enqueue, has: has, log: log, Debounce: 5 * time.Second}
}

// Start (re)starts watching with the current settings. It stops any previous watch first.
func (w *Watcher) Start(ctx context.Context) error {
	w.Stop()
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	set := w.cfg.Get()
	for _, loc := range set.LibraryLocations {
		w.addTree(fw, loc)
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	w.mu.Lock()
	w.cancel, w.done = cancel, done
	w.mu.Unlock()
	w.log.Info("watching", "locations", set.LibraryLocations, "dirs", len(fw.WatchList()))
	go func() {
		defer close(done)
		defer fw.Close()
		w.loop(ctx, fw, set)
	}()
	return nil
}

// Stop ends the current watch and waits for it to finish.
func (w *Watcher) Stop() {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.cancel, w.done = nil, nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (w *Watcher) addTree(fw *fsnotify.Watcher, root string) {
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == root {
				w.log.Warn("watch", "dir", p, "err", err)
			}
			return nil
		}
		if d.IsDir() {
			if err := fw.Add(p); err != nil {
				w.log.Warn("watch", "dir", p, "err", err)
			}
		}
		return nil
	})
}

func (w *Watcher) loop(ctx context.Context, fw *fsnotify.Watcher, set config.Settings) {
	pending := map[string]bool{}
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-fw.Errors:
			if !ok {
				return
			}
			w.log.Warn("watch error", "err", err)
		case ev, ok := <-fw.Events:
			if !ok {
				return
			}
			if ev.Has(fsnotify.Create) {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					// Files may already exist in a directory moved or copied in; queue them too.
					w.addTree(fw, ev.Name)
					filepath.WalkDir(ev.Name, func(p string, d fs.DirEntry, err error) error {
						if err == nil && !d.IsDir() && library.MatchesExt(p, set.VideoExtensions) {
							pending[p] = true
						}
						return nil
					})
					timer.Reset(w.Debounce)
					continue
				}
			}
			if library.MatchesExt(ev.Name, set.VideoExtensions) {
				pending[ev.Name] = true
				timer.Reset(w.Debounce)
			}
		case <-timer.C:
			paths := pending
			pending = map[string]bool{}
			w.flush(ctx, paths, set.AutoConversion.Conversion)
		}
	}
}

func (w *Watcher) flush(ctx context.Context, paths map[string]bool, conversion map[string]string) {
	changed, err := w.lib.Scan(ctx)
	if err != nil {
		w.log.Warn("rescan", "err", err)
	}
	if !changed {
		return
	}
	w.log.Info("changes detected", "files", len(paths))
	for p := range paths {
		f, err := w.lib.Get(ctx, p)
		if err != nil || f == nil {
			continue
		}
		w.AutoConvert(*f, conversion)
	}
}

// AutoConvert queues f if it has audio tracks matching the conversion map and is not
// already queued.
func (w *Watcher) AutoConvert(f media.File, conversion map[string]string) {
	if w.has(f.Path) {
		return
	}
	if specs := media.AutoSpecs(f, conversion); specs != nil {
		w.log.Info("auto converting", "file", f.Path)
		w.enqueue(f, specs)
	}
}
