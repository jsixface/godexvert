package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jsixface/godexvert/internal/media"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertAndQuery(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	f := media.File{Path: "/m/b.mkv", Name: "b.mkv", SizeMB: 10, Modified: 1, Added: 1, Tracks: []media.Track{
		{Kind: media.Video, Index: 0, Codec: "h264", Resolution: "1920x1080", FrameRate: 23.976},
		{Kind: media.Audio, Index: 1, Codec: "ac3", Channels: 6, Language: "eng"},
	}}
	if err := s.Upsert(ctx, f); err != nil {
		t.Fatal(err)
	}
	if err := s.Upsert(ctx, media.File{Path: "/m/a.mkv", Name: "a.mkv"}); err != nil {
		t.Fatal(err)
	}

	// Update replaces tracks and keeps the id.
	f.Modified = 2
	f.Tracks = f.Tracks[1:]
	if err := s.Upsert(ctx, f); err != nil {
		t.Fatal(err)
	}
	all, err := s.Files(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Name != "a.mkv" || len(all[1].Tracks) != 1 || all[1].Tracks[0].Channels != 6 {
		t.Fatalf("files: %+v", all)
	}
	got, err := s.File(ctx, "/m/b.mkv")
	if err != nil || got == nil || got.Modified != 2 || got.Audios()[0].Language != "eng" {
		t.Fatalf("File: %+v %v", got, err)
	}
	if got, _ := s.File(ctx, "/nope"); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}

	st, _ := s.Stamps(ctx)
	if err := s.Delete(ctx, st["/m/b.mkv"].ID); err != nil {
		t.Fatal(err)
	}
	all, _ = s.Files(ctx)
	if len(all) != 1 {
		t.Fatalf("after delete: %+v", all)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM tracks`).Scan(&n)
	if n != 0 {
		t.Fatalf("tracks should cascade, %d left", n)
	}
}

func TestJobs(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	start := time.Unix(1700000000, 0)
	for _, id := range []string{"a", "b", "c"} {
		if err := s.SaveJob(ctx, JobRecord{JobID: id, Status: "Completed", FilePath: "/x", FileName: "x", StartedAt: start, Duration: 90 * time.Second}); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := s.Jobs(ctx, 0, 2)
	if err != nil || len(jobs) != 2 || jobs[0].JobID != "c" || jobs[0].Duration != 90*time.Second || !jobs[0].StartedAt.Equal(start) {
		t.Fatalf("jobs: %+v %v", jobs, err)
	}
	if n, _ := s.CountJobs(ctx); n != 3 {
		t.Fatalf("count %d", n)
	}
	s.SaveJob(ctx, JobRecord{JobID: "d", Status: "Failed", StartedAt: start})
	if m, err := s.CountJobsByStatus(ctx); err != nil || m["Completed"] != 3 || m["Failed"] != 1 {
		t.Fatalf("by status: %v %v", m, err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.db")
	for range 2 {
		s, err := Open(context.Background(), p)
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
}
