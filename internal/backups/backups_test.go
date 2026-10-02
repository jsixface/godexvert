package backups

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListAndDelete(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	for _, n := range []string{"a.mkv", "a.mkv.1700000000.bkp", "sub/b.mp4.1800000000.bkp", "c.bkp"} {
		os.WriteFile(filepath.Join(dir, n), nil, 0o644)
	}
	locs := []string{dir}
	got := List(locs)
	if len(got) != 2 || got[0].Name != "b.mp4.1800000000.bkp" || got[1].Time.Unix() != 1700000000 {
		t.Fatalf("list: %+v", got)
	}

	if err := Delete(locs, filepath.Join(dir, "a.mkv")); err != ErrInvalid {
		t.Fatalf("non-backup delete: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "x.mkv.1.bkp")
	os.WriteFile(outside, nil, 0o644)
	if err := Delete(locs, outside); err != ErrInvalid {
		t.Fatalf("outside delete: %v", err)
	}
	if err := Delete(locs, got[1].Path); err != nil {
		t.Fatal(err)
	}
	if n, err := DeleteAll(locs); n != 1 || err != nil {
		t.Fatalf("delete all: %d %v", n, err)
	}
	if len(List(locs)) != 0 {
		t.Fatal("backups remain")
	}
	if _, err := os.Stat(filepath.Join(dir, "a.mkv")); err != nil {
		t.Fatal("video removed")
	}
}
