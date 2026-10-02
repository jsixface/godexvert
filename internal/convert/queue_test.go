package convert

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jsixface/godexvert/internal/config"
	"github.com/jsixface/godexvert/internal/media"
	"github.com/jsixface/godexvert/internal/store"
)

type saver struct {
	mu   sync.Mutex
	recs []store.JobRecord
}

func (s *saver) SaveJob(_ context.Context, j store.JobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = append(s.recs, j)
	return nil
}

func (s *saver) all() []store.JobRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.JobRecord(nil), s.recs...)
}

func newQueue(t *testing.T, backups bool, run Runner) (*Queue, *saver, string) {
	t.Helper()
	cfg, _ := config.Load(filepath.Join(t.TempDir(), config.FileName))
	set := config.Default()
	set.WorkspaceLocation = t.TempDir()
	set.TakeBackups = backups
	cfg.Save(set)
	sv := &saver{}
	q := NewQueue(sv, cfg, run, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return q, sv, t.TempDir()
}

// fakeFFmpeg writes "converted" to the output path (last arg) after emitting progress.
func fakeFFmpeg(ctx context.Context, args []string, onLine func(string)) error {
	onLine("  Duration: 00:00:10.00, start: 0")
	onLine("time=00:00:05.00 bitrate=1")
	return os.WriteFile(args[len(args)-1], []byte("converted"), 0o644)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}

func TestQueueConvertsAndBacksUp(t *testing.T) {
	q, sv, lib := newQueue(t, true, fakeFFmpeg)
	orig := filepath.Join(lib, "a.mkv")
	os.WriteFile(orig, []byte("original"), 0o644)

	var mu sync.Mutex
	var progress []int
	var done []string
	q.OnUpdate = func(j Job) { mu.Lock(); progress = append(progress, j.Progress); mu.Unlock() }
	q.OnDone = func(p string) { mu.Lock(); done = append(done, p); mu.Unlock() }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go q.Run(ctx)
	q.Enqueue(media.File{Path: orig, Name: "a.mkv"}, nil)
	waitFor(t, func() bool { return len(sv.all()) == 1 })

	if r := sv.all()[0]; r.Status != string(Completed) || r.FilePath != orig {
		t.Fatalf("record %+v", r)
	}
	if b, _ := os.ReadFile(orig); string(b) != "converted" {
		t.Fatalf("orig content %q", b)
	}
	bkps, _ := filepath.Glob(orig + ".*.bkp")
	if len(bkps) != 1 {
		t.Fatalf("backups %v", bkps)
	}
	if b, _ := os.ReadFile(bkps[0]); string(b) != "original" {
		t.Fatalf("backup content %q", b)
	}
	if len(q.Jobs()) != 0 {
		t.Fatal("job should be removed")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(done) != 1 || progress[len(progress)-1] != 100 {
		t.Fatalf("done=%v progress=%v", done, progress)
	}
	entries, _ := os.ReadDir(q.cfg.Get().WorkspaceLocation)
	if len(entries) != 0 {
		t.Fatalf("workspace not cleaned: %v", entries)
	}
}

func TestQueueWithoutBackup(t *testing.T) {
	q, sv, lib := newQueue(t, false, fakeFFmpeg)
	orig := filepath.Join(lib, "a.mkv")
	os.WriteFile(orig, []byte("original"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go q.Run(ctx)
	q.Enqueue(media.File{Path: orig, Name: "a.mkv"}, nil)
	waitFor(t, func() bool { return len(sv.all()) == 1 })
	entries, _ := os.ReadDir(lib)
	if len(entries) != 1 {
		t.Fatalf("expected only the converted file, got %v", entries)
	}
}

func TestQueueFailureKeepsOriginal(t *testing.T) {
	q, sv, lib := newQueue(t, true, func(context.Context, []string, func(string)) error {
		return &ExitError{Err: errors.New("exit status 1"), Tail: "Unknown encoder"}
	})
	orig := filepath.Join(lib, "a.mkv")
	os.WriteFile(orig, []byte("original"), 0o644)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go q.Run(ctx)
	q.Enqueue(media.File{Path: orig, Name: "a.mkv"}, nil)
	waitFor(t, func() bool { return len(sv.all()) == 1 })
	r := sv.all()[0]
	if r.Status != string(Failed) || !strings.Contains(r.Error, "Unknown encoder") {
		t.Fatalf("record %+v", r)
	}
	if b, _ := os.ReadFile(orig); string(b) != "original" {
		t.Fatal("original must be untouched")
	}
}

func TestQueueCancel(t *testing.T) {
	started := make(chan struct{})
	q, sv, lib := newQueue(t, true, func(ctx context.Context, _ []string, _ func(string)) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	orig := filepath.Join(lib, "a.mkv")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Queued jobs are removed without a record.
	j1 := q.Enqueue(media.File{Path: orig, Name: "a.mkv"}, nil)
	j2 := q.Enqueue(media.File{Path: "/other.mkv", Name: "other.mkv"}, nil)
	if !q.Has(orig) || q.Has("/nope") {
		t.Fatal("Has")
	}
	go q.Run(ctx)
	<-started
	if !q.Cancel(j2.ID) || len(q.Jobs()) != 1 {
		t.Fatalf("cancel queued: %+v", q.Jobs())
	}
	// Running jobs are stopped and recorded as cancelled.
	if !q.Cancel(j1.ID) {
		t.Fatal("cancel running")
	}
	waitFor(t, func() bool { return len(sv.all()) == 1 })
	if r := sv.all()[0]; r.Status != string(Cancelled) || r.JobID != j1.ID {
		t.Fatalf("record %+v", r)
	}
	if q.Cancel("missing") {
		t.Fatal("unknown id")
	}
}

func TestSwapAcrossDirs(t *testing.T) {
	lib, ws := t.TempDir(), t.TempDir()
	orig, conv := filepath.Join(lib, "x.mp4"), filepath.Join(ws, "x.mp4")
	os.WriteFile(orig, []byte("old"), 0o644)
	os.WriteFile(conv, []byte("new"), 0o644)
	if err := Swap(orig, conv, true, time.Unix(1700000000, 0)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(orig + ".1700000000.bkp"); string(b) != "old" {
		t.Fatal("backup missing")
	}
	if b, _ := os.ReadFile(orig); string(b) != "new" {
		t.Fatal("not replaced")
	}
	if _, err := os.Stat(conv); !os.IsNotExist(err) {
		t.Fatal("converted file should be moved")
	}
}
