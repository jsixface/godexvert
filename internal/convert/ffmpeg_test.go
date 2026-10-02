package convert

import (
	"slices"
	"strings"
	"testing"

	"github.com/jsixface/godexvert/internal/media"
)

func file() media.File {
	return media.File{Path: "/m/in.mkv", Name: "in.mkv", Tracks: []media.Track{
		{Kind: media.Video, Index: 0, Codec: "h264"},
		{Kind: media.Audio, Index: 1, Codec: "ac3"},
		{Kind: media.Audio, Index: 2, Codec: "dts"},
		{Kind: media.Subtitle, Index: 3, Codec: "subrip"},
	}}
}

func TestBuildArgsCopiesUnspecified(t *testing.T) {
	got := strings.Join(BuildArgs(file(), nil, "/w/out.mkv"), " ")
	want := "-hide_banner -nostdin -i /m/in.mkv -map 0:0 -codec:0 copy -map 0:1 -codec:1 copy -map 0:2 -codec:2 copy -map 0:3 -codec:3 copy /w/out.mkv"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestBuildArgsConvertAndDrop(t *testing.T) {
	opus, _ := media.LookupCodec("opus")
	specs := media.Specs{1: {Op: media.Drop}, 2: {Op: media.Convert, Codec: opus}}
	got := strings.Join(BuildArgs(file(), specs, "/w/out.mkv"), " ")
	// Output stream numbers skip the dropped track.
	want := "-hide_banner -nostdin -i /m/in.mkv -map 0:0 -codec:0 copy -map 0:2 -codec:1 libopus -b:a 128K -map 0:3 -codec:2 copy /w/out.mkv"
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestBuildArgsSortsByIndex(t *testing.T) {
	f := file()
	slices.Reverse(f.Tracks)
	args := BuildArgs(f, nil, "o")
	if args[5] != "0:0" || args[len(args)-4] != "0:3" {
		t.Fatalf("not sorted: %v", args)
	}
}

func TestProgress(t *testing.T) {
	var p Progress
	lines := []string{
		"Input #0, matroska,webm, from 'in.mkv':",
		"  Duration: 00:10:00.00, start: 0.000000, bitrate: 5000 kb/s",
		"frame=  100 fps=0.0 q=-1.0 size=    1024kB time=N/A bitrate=N/A speed=N/A",
		"frame= 1000 fps=50 q=-1.0 size=   10240kB time=00:05:00.00 bitrate=1000kbits/s speed=10x",
		"  Duration: 01:00:00.00, start: 0.000000, bitrate: 5000 kb/s",
		"size=  20480kB time=00:10:00.00 bitrate=1000kbits/s speed=10x",
	}
	var got []int
	for _, l := range lines {
		if pct, ok := p.Line(l); ok {
			got = append(got, pct)
		}
	}
	// A later Duration line (e.g. a second input) does not reset the total; 100% is reserved for success.
	if !slices.Equal(got, []int{50, 99}) {
		t.Fatalf("got %v", got)
	}
}

func TestProgressWithoutDuration(t *testing.T) {
	var p Progress
	if _, ok := p.Line("size= 1kB time=00:00:01.00 bitrate=1"); ok {
		t.Fatal("no progress without a known duration")
	}
}

func TestScanLinesSplitsCarriageReturns(t *testing.T) {
	var lines []string
	tail := scanLines(strings.NewReader("a\nframe=1 time=00:00:01.00\rframe=2 time=00:00:02.00\rerror here\n"), func(l string) {
		lines = append(lines, l)
	})
	if len(lines) != 4 || tail != "error here" {
		t.Fatalf("lines=%q tail=%q", lines, tail)
	}
}

func TestCommandLineQuotes(t *testing.T) {
	got := CommandLine([]string{"-i", "/m/it's a movie.mkv", "-codec:0", "copy", "/w/out.mkv"})
	want := `ffmpeg -i '/m/it'\''s a movie.mkv' -codec:0 copy /w/out.mkv`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}
