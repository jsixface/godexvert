package media

import "testing"

func audioFile(codecs ...string) File {
	f := File{Path: "/m/a.avi", Name: "a.avi"}
	for i, c := range codecs {
		f.Tracks = append(f.Tracks, Track{Kind: Audio, Index: i, Codec: c, Channels: 2, Language: "eng"})
	}
	return f
}

// Port of WatchersTest "should convert AAC to MP3".
func TestAutoSpecsConvertsMatchingAudio(t *testing.T) {
	got := AutoSpecs(audioFile("aac", "eac3"), map[string]string{"AAC": "MP3"})
	if len(got) != 1 || got[0].Op != Convert || got[0].Codec.Name != "MP3" {
		t.Fatalf("got %+v", got)
	}
}

func TestAutoSpecsSkipsIdentityAndUnknown(t *testing.T) {
	if got := AutoSpecs(audioFile("aac"), map[string]string{"AAC": "AAC"}); got != nil {
		t.Fatalf("identity mapping should be ignored, got %+v", got)
	}
	if got := AutoSpecs(audioFile("aac"), map[string]string{"AAC": "Nope"}); got != nil {
		t.Fatalf("unknown target should be ignored, got %+v", got)
	}
	if got := AutoSpecs(audioFile("aac"), map[string]string{"AAC": "HEVC"}); got != nil {
		t.Fatalf("video target for audio should be ignored, got %+v", got)
	}
	if got := AutoSpecs(audioFile("dts"), map[string]string{"AC3": "AAC"}); got != nil {
		t.Fatalf("no matching track, got %+v", got)
	}
}

func TestParseAction(t *testing.T) {
	for in, want := range map[string]string{"": "copy", "copy": "copy", "drop": "drop", "convert:aac": "convert:AAC"} {
		a, ok := ParseAction(in)
		if !ok || a.String() != want {
			t.Errorf("ParseAction(%q) = %v,%v want %s", in, a, ok, want)
		}
	}
	for _, in := range []string{"convert:xyz", "bogus"} {
		if _, ok := ParseAction(in); ok {
			t.Errorf("ParseAction(%q) should fail", in)
		}
	}
}

func TestTargetsForExcludesOwnCodec(t *testing.T) {
	for _, c := range TargetsFor(Track{Kind: Audio, Codec: "aac"}) {
		if c.Name == "AAC" || c.Kind != Audio {
			t.Fatalf("unexpected target %v", c)
		}
	}
	if len(TargetsFor(Track{Kind: Subtitle, Codec: "subrip"})) != 0 {
		t.Fatal("subtitles have no targets")
	}
}
