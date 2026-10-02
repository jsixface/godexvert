package library

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jsixface/godexvert/internal/config"
	"github.com/jsixface/godexvert/internal/media"
	"github.com/jsixface/godexvert/internal/store"
)

func setup(t *testing.T, locs ...string) (*Library, *atomic.Int32) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg, _ := config.Load(filepath.Join(t.TempDir(), config.FileName))
	set := config.Default()
	set.LibraryLocations = locs
	set.VideoExtensions = []string{"mkv", ".MP4"}
	if err := cfg.Save(set); err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	fake := func(_ context.Context, path string) ([]media.Track, error) {
		probes.Add(1)
		return []media.Track{{Kind: media.Video, Codec: "h264"}, {Kind: media.Audio, Index: 1, Codec: "ac3"}}, nil
	}
	return New(st, cfg, fake, slog.New(slog.NewTextHandler(io.Discard, nil))), &probes
}

func touch(t *testing.T, p string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "a.mkv"))
	touch(t, filepath.Join(dir, "sub", "B.mp4"))
	touch(t, filepath.Join(dir, "notes.txt"))
	touch(t, filepath.Join(dir, "a.mkv.1700000000.bkp"))
	lib, probes := setup(t, dir)

	changed, err := lib.Scan(ctx)
	if err != nil || !changed {
		t.Fatalf("first scan: %v %v", changed, err)
	}
	if got := lib.Files(); len(got) != 2 || got[0].Name != "a.mkv" || got[1].Name != "B.mp4" {
		t.Fatalf("files: %+v", got)
	}
	if probes.Load() != 2 {
		t.Fatalf("probes %d", probes.Load())
	}

	// Unchanged rescan probes nothing.
	if changed, _ := lib.Scan(ctx); changed || probes.Load() != 2 {
		t.Fatalf("rescan changed=%v probes=%d", changed, probes.Load())
	}

	// Modify one, delete the other.
	future := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(dir, "a.mkv"), future, future)
	os.Remove(filepath.Join(dir, "sub", "B.mp4"))
	if changed, _ := lib.Scan(ctx); !changed || probes.Load() != 3 || len(lib.Files()) != 1 {
		t.Fatalf("after change: changed=%v probes=%d files=%d", changed, probes.Load(), len(lib.Files()))
	}
}

func TestScanKeepsUnreachableLocation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "a.mkv"))
	lib, _ := setup(t, dir)
	lib.Scan(ctx)
	os.RemoveAll(dir)
	lib.Scan(ctx)
	if len(lib.Files()) != 1 {
		t.Fatal("files under an unreachable location should be kept")
	}
}

func TestFindAndCodecs(t *testing.T) {
	lib, _ := setup(t)
	lib.cache = []media.File{
		{Name: "Alpha.mkv", Tracks: []media.Track{{Kind: media.Video, Codec: "hevc"}, {Kind: media.Audio, Codec: "AAC"}}},
		{Name: "beta.mkv", Tracks: []media.Track{{Kind: media.Video, Codec: "h264"}, {Kind: media.Audio, Codec: "ac3"}}},
	}
	if got := lib.Find(Filter{Name: "ALP"}); len(got) != 1 || got[0].Name != "Alpha.mkv" {
		t.Fatalf("name filter: %+v", got)
	}
	if got := lib.Find(Filter{Audio: "aac", Video: "hevc"}); len(got) != 1 {
		t.Fatalf("codec filter: %+v", got)
	}
	if got := lib.Find(Filter{Audio: "aac", Video: "h264"}); len(got) != 0 {
		t.Fatalf("codec filter: %+v", got)
	}
	if got := lib.Codecs(media.Audio); len(got) != 2 || got[0] != "aac" {
		t.Fatalf("codecs: %v", got)
	}
}

func TestWithin(t *testing.T) {
	roots := []string{"/media/tv", "/media/movies/"}
	for p, want := range map[string]bool{
		"/media/tv/a.mkv":            true,
		"/media/movies/x/y.mkv":      true,
		"/media/tv":                  false,
		"/media/tvshows/a.mkv":       false,
		"/media/tv/../../etc/passwd": false,
		"relative/a.mkv":             false,
	} {
		if got := Within(p, roots); got != want {
			t.Errorf("Within(%q)=%v want %v", p, got, want)
		}
	}
}

func TestMatchesExt(t *testing.T) {
	exts := []string{"mkv", ".mp4"}
	for p, want := range map[string]bool{"a.MKV": true, "a.mp4": true, "a.avi": false, "mkv": false, "a.mkv.1.bkp": false} {
		if got := MatchesExt(p, exts); got != want {
			t.Errorf("MatchesExt(%q)=%v", p, got)
		}
	}
}

func TestSortFiles(t *testing.T) {
	v := func(res string) []media.Track { return []media.Track{{Kind: media.Video, Resolution: res}} }
	files := []media.File{
		{Name: "b", SizeMB: 5, Added: 3, Tracks: v("1920x1080")},
		{Name: "A", SizeMB: 9, Added: 1, Tracks: v("3840x2160")},
		{Name: "c", SizeMB: 1, Added: 2},
	}
	order := func() string {
		s := ""
		for _, f := range files {
			s += f.Name
		}
		return s
	}
	for _, tc := range []struct {
		key  string
		desc bool
		want string
	}{
		{SortName, false, "Abc"}, {SortName, true, "cbA"}, {SortSize, true, "Abc"},
		{SortRes, true, "Abc"}, {SortAdded, false, "Acb"},
	} {
		SortFiles(files, tc.key, tc.desc)
		if got := order(); got != tc.want {
			t.Errorf("%s desc=%v: %s want %s", tc.key, tc.desc, got, tc.want)
		}
	}
}
