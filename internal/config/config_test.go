package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingGivesDefaults(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Get(); got.WorkspaceLocation != "/tmp/vid-con" || !got.TakeBackups || len(got.VideoExtensions) != 4 {
		t.Fatalf("unexpected defaults: %+v", got)
	}
}

func TestLoadKotlinFormat(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	body := `{"settings":{"libraryLocations":["/media"],"takeBackups":false,"autoConversion":{"conversion":{"AC3":"AAC"}},"unknown":1}}`
	os.WriteFile(p, []byte(body), 0o644)
	s, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	got := s.Get()
	if got.LibraryLocations[0] != "/media" || got.TakeBackups || got.AutoConversion.Conversion["AC3"] != "AAC" {
		t.Fatalf("bad parse: %+v", got)
	}
	if got.WorkspaceLocation != "/tmp/vid-con" {
		t.Fatalf("missing field should keep default, got %q", got.WorkspaceLocation)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	s, _ := Load(p)
	v := Default()
	v.LibraryLocations = []string{"/a", "/b"}
	if err := s.Save(v); err != nil {
		t.Fatal(err)
	}
	s2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.Get().LibraryLocations; len(got) != 2 || got[1] != "/b" {
		t.Fatalf("round trip failed: %v", got)
	}
}

func TestLoadFallsBackToLegacyFile(t *testing.T) {
	dir := t.TempDir()
	p, legacy := filepath.Join(dir, FileName), filepath.Join(dir, LegacyFileName)
	os.WriteFile(legacy, []byte(`{"settings":{"libraryLocations":["/old"]}}`), 0o644)
	s, err := Load(p, legacy)
	if err != nil || s.Get().LibraryLocations[0] != "/old" {
		t.Fatalf("fallback not read: %+v %v", s, err)
	}
	if err := s.Save(s.Get()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("save should write the new file")
	}
	// Once the new file exists, the legacy one is ignored.
	os.WriteFile(legacy, []byte(`{"settings":{"libraryLocations":["/changed"]}}`), 0o644)
	s, _ = Load(p, legacy)
	if s.Get().LibraryLocations[0] != "/old" {
		t.Fatal("new file should win")
	}
}
