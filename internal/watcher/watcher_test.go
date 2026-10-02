package watcher

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jsixface/godexvert/internal/config"
	"github.com/jsixface/godexvert/internal/media"
)

type fakeLib struct {
	mu    sync.Mutex
	scans int
	files map[string]media.File
}

func (l *fakeLib) Scan(context.Context) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.scans++
	return true, nil
}

func (l *fakeLib) Get(_ context.Context, p string) (*media.File, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f, ok := l.files[p]; ok {
		return &f, nil
	}
	return nil, nil
}

type harness struct {
	w      *Watcher
	lib    *fakeLib
	dir    string
	mu     sync.Mutex
	queued []media.File
}

func (h *harness) enqueued() []media.File {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]media.File(nil), h.queued...)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	cfg, _ := config.Load(filepath.Join(t.TempDir(), config.FileName))
	set := config.Default()
	set.LibraryLocations = []string{dir}
	set.VideoExtensions = []string{".mkv", ".mp4"}
	set.AutoConversion.Conversion = map[string]string{"AAC": "MP3"}
	cfg.Save(set)
	h := &harness{lib: &fakeLib{files: map[string]media.File{}}, dir: dir}
	h.w = New(cfg, h.lib, func(f media.File, _ media.Specs) {
		h.mu.Lock()
		h.queued = append(h.queued, f)
		h.mu.Unlock()
	}, func(string) bool { return false }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.w.Debounce = 100 * time.Millisecond
	if err := h.w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.w.Stop)
	return h
}

func (h *harness) expect(t *testing.T, path string) {
	t.Helper()
	aac := media.File{Path: path, Name: filepath.Base(path), Tracks: []media.Track{{Kind: media.Audio, Codec: "aac"}}}
	h.lib.mu.Lock()
	h.lib.files[path] = aac
	h.lib.mu.Unlock()
}

func waitQueued(t *testing.T, h *harness, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, f := range h.enqueued() {
			if f.Path == path {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s was not queued; queued=%v", path, h.enqueued())
}

// Port of WatchersTest "should detect file system changes recursively".
func TestDetectsChangesRecursively(t *testing.T) {
	h := newHarness(t)
	h.w.Stop()
	sub := filepath.Join(h.dir, "subdir")
	os.Mkdir(sub, 0o755)
	// Restart so subdir is registered by the initial recursive walk.
	if err := h.w.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(sub, "test.mkv")
	h.expect(t, p)
	os.WriteFile(p, nil, 0o644)
	waitQueued(t, h, p)
}

// Port of WatchersTest "should detect file system changes in newly created directory".
func TestDetectsFilesInNewDirectory(t *testing.T) {
	h := newHarness(t)
	nd := filepath.Join(h.dir, "newdir")
	os.Mkdir(nd, 0o755)
	time.Sleep(200 * time.Millisecond)
	p := filepath.Join(nd, "test.mkv")
	h.expect(t, p)
	os.WriteFile(p, nil, 0o644)
	waitQueued(t, h, p)
}

func TestIgnoresOtherExtensions(t *testing.T) {
	h := newHarness(t)
	p := filepath.Join(h.dir, "notes.txt")
	h.expect(t, p)
	os.WriteFile(p, nil, 0o644)
	time.Sleep(400 * time.Millisecond)
	h.lib.mu.Lock()
	defer h.lib.mu.Unlock()
	if h.lib.scans != 0 || len(h.enqueued()) != 0 {
		t.Fatalf("scans=%d queued=%v", h.lib.scans, h.enqueued())
	}
}

func TestAutoConvertSkipsQueued(t *testing.T) {
	var n int
	w := New(nil, nil, func(media.File, media.Specs) { n++ }, func(string) bool { return true }, slog.Default())
	w.AutoConvert(media.File{Tracks: []media.Track{{Kind: media.Audio, Codec: "aac"}}}, map[string]string{"AAC": "MP3"})
	if n != 0 {
		t.Fatal("queued file must not be queued again")
	}
}
