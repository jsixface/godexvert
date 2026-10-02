// Package probe runs ffprobe and converts its JSON into track structs.
package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"

	"github.com/jsixface/godexvert/internal/media"
)

type Stream struct {
	Index         int               `json:"index"`
	CodecName     string            `json:"codec_name"`
	CodecType     string            `json:"codec_type"`
	CodecTag      string            `json:"codec_tag_string"`
	Profile       string            `json:"profile"`
	Width         int               `json:"width"`
	Height        int               `json:"height"`
	AspectRatio   string            `json:"display_aspect_ratio"`
	AvgFrameRate  string            `json:"avg_frame_rate"`
	BitRate       string            `json:"bit_rate"`
	PixFmt        string            `json:"pix_fmt"`
	Channels      int               `json:"channels"`
	ChannelLayout string            `json:"channel_layout"`
	SampleRate    string            `json:"sample_rate"`
	Tags          map[string]string `json:"tags"`
}

type Format struct {
	FileName   string `json:"filename"`
	NumStreams int    `json:"nb_streams"`
	FormatName string `json:"format_long_name"`
	Duration   string `json:"duration"`
}

type Info struct {
	Streams []Stream `json:"streams"`
	Format  Format   `json:"format"`
}

// Parse decodes ffprobe -of json output.
func Parse(data []byte) (*Info, error) {
	var in Info
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// Run executes ffprobe on path.
func Run(ctx context.Context, path string) (*Info, error) {
	out, err := exec.CommandContext(ctx, "ffprobe",
		"-v", "error", "-of", "json",
		"-show_entries", "stream:program:format:chapter", path).Output()
	if err != nil {
		return nil, fmt.Errorf("ffprobe %s: %w", path, err)
	}
	return Parse(out)
}

// Language returns the stream's language tag, or "".
func (s Stream) Language() string { return s.Tags["language"] }

// Resolution formats as "WxH".
func (s Stream) Resolution() string { return fmt.Sprintf("%dx%d", s.Width, s.Height) }

// FrameRate parses "24000/1001" or "25"; invalid or zero-denominator gives 0.
func (s Stream) FrameRate() float64 {
	num, den, found := strings.Cut(s.AvgFrameRate, "/")
	n, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0
	}
	if !found {
		return n
	}
	d, err := strconv.ParseFloat(den, 64)
	if err != nil || d == 0 {
		return 0
	}
	return n / d
}

// Bitrate returns bit_rate in bits/s, or 0.
func (s Stream) Bitrate() int {
	n, _ := strconv.Atoi(s.BitRate)
	return n
}

// AspectString normalizes "16:9"-style ratios like the Kotlin AspectRatio class:
// ratios with both sides >= 10 are reduced to "x:1" or "1:y".
func AspectString(ratio string) string {
	a, b, ok := strings.Cut(ratio, ":")
	if !ok {
		return ""
	}
	w, err1 := strconv.ParseFloat(a, 64)
	h, err2 := strconv.ParseFloat(b, 64)
	if err1 != nil || err2 != nil {
		return ""
	}
	switch {
	case w < 10 || h < 10:
	case w > h:
		w, h = w/h, 1
	default:
		w, h = 1, h/w
	}
	return short(w) + ":" + short(h)
}

func short(f float64) string {
	if f == math.Trunc(f) {
		return strconv.Itoa(int(f))
	}
	return fmt.Sprintf("%.2f", f)
}

// Tracks converts the probed streams into media tracks, in stream order.
// Streams other than video, audio and subtitle (data, attachments) are skipped.
func (in *Info) Tracks() []media.Track {
	var out []media.Track
	for _, s := range in.Streams {
		t := media.Track{Kind: media.Kind(s.CodecType), Index: s.Index, Codec: s.CodecName}
		switch t.Kind {
		case media.Video:
			t.CodecTag = s.CodecTag
			t.Profile = s.Profile
			t.Resolution = s.Resolution()
			t.AspectRatio = AspectString(s.AspectRatio)
			t.FrameRate = s.FrameRate()
			t.PixelFormat = s.PixFmt
			t.BitRate = s.Bitrate()
		case media.Audio:
			t.Channels = s.Channels
			t.Layout = s.ChannelLayout
			t.SampleRate = s.SampleRate
			t.BitRate = s.Bitrate()
			t.Language = s.Language()
		case media.Subtitle:
			t.Language = s.Language()
		default:
			continue
		}
		out = append(out, t)
	}
	return out
}
