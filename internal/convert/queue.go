package convert

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/jsixface/godexvert/internal/config"
	"github.com/jsixface/godexvert/internal/media"
	"github.com/jsixface/godexvert/internal/store"
)

type Status string

const (
	Queued     Status = "Queued"
	Starting   Status = "Starting"
	InProgress Status = "InProgress"
	Completed  Status = "Completed"
	Failed     Status = "Failed"
	Cancelled  Status = "Cancelled"
)

// Active reports whether a job with this status can still be cancelled.
func (s Status) Active() bool { return s == Queued || s == Starting || s == InProgress }

// Job is a snapshot of a queued or running conversion.
type Job struct {
	ID        string
	File      media.File
	Specs     media.Specs
	Status    Status
	Progress  int
	StartedAt time.Time // when the job was queued, as in the Kotlin app
	RunStart  time.Time // when ffmpeg began, for the ETA
}

// JobSaver records finished jobs.
type JobSaver interface {
	SaveJob(ctx context.Context, j store.JobRecord) error
}

type entry struct {
	Job
	cancel    context.CancelFunc
	cancelled bool
}

// Queue runs conversions serially. Set the On* hooks before calling Run.
type Queue struct {
	saver JobSaver
	cfg   *config.Store
	run   Runner
	log   *slog.Logger

	OnUpdate func(Job)         // an active job's status or progress changed
	OnChange func()            // jobs were added, removed or finished
	OnDone   func(path string) // a file was replaced by its converted version

	mu   sync.Mutex
	jobs []*entry
	wake chan struct{}
}

func NewQueue(saver JobSaver, cfg *config.Store, run Runner, log *slog.Logger) *Queue {
	return &Queue{saver: saver, cfg: cfg, run: run, log: log, wake: make(chan struct{}, 1),
		OnUpdate: func(Job) {}, OnChange: func() {}, OnDone: func(string) {}}
}

// Enqueue adds a conversion of f and returns the queued job.
func (q *Queue) Enqueue(f media.File, specs media.Specs) Job {
	e := &entry{Job: Job{ID: newID(), File: f, Specs: specs, Status: Queued, StartedAt: time.Now()}}
	snap := e.Job
	q.mu.Lock()
	q.jobs = append(q.jobs, e)
	q.mu.Unlock()
	q.log.Info("queued", "job", snap.ID, "file", f.Path, "specs", fmt.Sprint(specs))
	select {
	case q.wake <- struct{}{}:
	default:
	}
	q.OnChange()
	return snap
}

// Jobs returns the queued and running jobs in queue order.
func (q *Queue) Jobs() []Job {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]Job, len(q.jobs))
	for i, e := range q.jobs {
		out[i] = e.Job
	}
	return out
}

// Has reports whether path is queued or being converted.
func (q *Queue) Has(path string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.jobs {
		if e.File.Path == path {
			return true
		}
	}
	return false
}

// Cancel removes a queued job or stops a running one. It reports whether the job was found.
func (q *Queue) Cancel(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i, e := range q.jobs {
		if e.ID != id {
			continue
		}
		if e.cancel == nil {
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			q.log.Info("dequeued", "job", id)
			go q.OnChange()
		} else {
			e.cancelled = true
			e.cancel()
			q.log.Info("cancelling", "job", id)
		}
		return true
	}
	return false
}

// Run processes jobs until ctx is cancelled.
func (q *Queue) Run(ctx context.Context) {
	for {
		if e, jctx := q.next(ctx); e != nil {
			q.process(ctx, jctx, e)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		}
	}
}

// next claims the first queued job, giving it a cancel func while still under the lock
// so Cancel can never see a claimed job as merely queued.
func (q *Queue) next(ctx context.Context) (*entry, context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if ctx.Err() != nil {
		return nil, nil
	}
	for _, e := range q.jobs {
		if e.Status == Queued {
			e.Status = Starting
			jctx, cancel := context.WithCancel(ctx)
			e.cancel = cancel
			return e, jctx
		}
	}
	return nil, nil
}

func (q *Queue) set(e *entry, fn func(*entry)) {
	q.mu.Lock()
	fn(e)
	snap := e.Job
	q.mu.Unlock()
	q.OnUpdate(snap)
}

func (q *Queue) process(ctx, jctx context.Context, e *entry) {
	defer e.cancel()
	start := time.Now()
	q.mu.Lock()
	e.RunStart = start
	snap := e.Job
	q.mu.Unlock()
	q.OnUpdate(snap)

	settings := q.cfg.Get()
	dir := filepath.Join(settings.WorkspaceLocation, e.ID)
	err := q.convert(jctx, e, dir, settings.TakeBackups)
	os.RemoveAll(dir)

	q.mu.Lock()
	switch {
	case err == nil:
		e.Status, e.Progress = Completed, 100
	case e.cancelled || errors.Is(err, context.Canceled):
		e.Status = Cancelled
	default:
		e.Status = Failed
	}
	final := e.Job
	for i, x := range q.jobs {
		if x == e {
			q.jobs = append(q.jobs[:i], q.jobs[i+1:]...)
			break
		}
	}
	q.mu.Unlock()

	rec := store.JobRecord{JobID: final.ID, Status: string(final.Status), FilePath: final.File.Path,
		FileName: final.File.Name, StartedAt: final.StartedAt, Duration: time.Since(start)}
	if err != nil {
		if final.Status != Cancelled {
			rec.Error = err.Error()
		}
		q.log.Warn("job ended", "job", e.ID, "status", final.Status, "err", err)
	} else {
		q.log.Info("job done", "job", e.ID, "file", final.File.Path, "took", rec.Duration.Round(time.Second))
	}
	if serr := q.saver.SaveJob(context.WithoutCancel(ctx), rec); serr != nil {
		q.log.Error("save job", "job", e.ID, "err", serr)
	}
	q.OnUpdate(final)
	q.OnChange()
	if err == nil {
		q.OnDone(final.File.Path)
	}
}

func (q *Queue) convert(ctx context.Context, e *entry, dir string, backup bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	out := filepath.Join(dir, e.File.Name)
	args := BuildArgs(e.File, e.Specs, out)
	q.log.Info("running ffmpeg", "job", e.ID, "cmd", CommandLine(args))

	var p Progress
	last := -1
	err := q.run(ctx, args, func(line string) {
		if pct, ok := p.Line(line); ok && pct != last {
			last = pct
			q.set(e, func(e *entry) { e.Status, e.Progress = InProgress, pct })
		}
	})
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return Swap(e.File.Path, out, backup, time.Now())
}

// Swap replaces orig with converted. The converted file is first moved next to orig (copying
// across filesystems), then orig is renamed to "<orig>.<unix>.bkp" (or deleted when backup is
// false) and the new file is renamed into place.
func Swap(orig, converted string, backup bool, now time.Time) error {
	tmp := filepath.Join(filepath.Dir(orig), "."+filepath.Base(orig)+".godexvert.tmp")
	if err := move(converted, tmp); err != nil {
		return fmt.Errorf("stage converted file: %w", err)
	}
	if backup {
		if err := os.Rename(orig, orig+"."+strconv.FormatInt(now.Unix(), 10)+".bkp"); err != nil {
			os.Remove(tmp)
			return fmt.Errorf("backup original: %w", err)
		}
	} else if err := os.Remove(orig); err != nil && !errors.Is(err, os.ErrNotExist) {
		os.Remove(tmp)
		return fmt.Errorf("remove original: %w", err)
	}
	if err := os.Rename(tmp, orig); err != nil {
		return fmt.Errorf("replace original: %w", err)
	}
	return nil
}

func move(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

func newID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
