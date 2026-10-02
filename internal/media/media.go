// Package media holds the domain types shared across the app: video files, tracks, codecs and conversions.
package media

import (
	"slices"
	"strings"
)

type Kind string

const (
	Video    Kind = "video"
	Audio    Kind = "audio"
	Subtitle Kind = "subtitle"
)

// Track is one stream of a video file. Fields not relevant to the Kind are zero.
type Track struct {
	Kind  Kind
	Index int
	Codec string

	// video
	CodecTag    string
	Profile     string
	Resolution  string
	AspectRatio string
	FrameRate   float64
	PixelFormat string

	// video + audio
	BitRate int

	// audio
	Channels   int
	Layout     string
	SampleRate string

	// audio + subtitle
	Language string
}

type File struct {
	ID       int64
	Path     string
	Name     string
	SizeMB   int64
	Modified int64 // unix millis
	Added    int64 // unix millis
	Tracks   []Track
}

// OfKind returns the tracks of kind k, in stream order.
func (f File) OfKind(k Kind) []Track {
	var out []Track
	for _, t := range f.Tracks {
		if t.Kind == k {
			out = append(out, t)
		}
	}
	return out
}

func (f File) Videos() []Track    { return f.OfKind(Video) }
func (f File) Audios() []Track    { return f.OfKind(Audio) }
func (f File) Subtitles() []Track { return f.OfKind(Subtitle) }

// HasCodec reports whether a track of kind k uses codec (case-insensitive).
func (f File) HasCodec(k Kind, codec string) bool {
	return slices.ContainsFunc(f.Tracks, func(t Track) bool {
		return t.Kind == k && strings.EqualFold(t.Codec, codec)
	})
}

// Codec is a conversion target, mirroring the Kotlin Codec enum.
type Codec struct {
	Name   string
	Kind   Kind
	Params []string // ffmpeg arguments following -codec:N
}

var Codecs = []Codec{
	{"AAC", Audio, []string{"aac"}},
	{"AC3", Audio, []string{"ac3"}},
	{"EAC3", Audio, []string{"eac3"}},
	{"Opus", Audio, []string{"libopus", "-b:a", "128K"}},
	{"MP3", Audio, []string{"mp3", "-b:a", "128K"}},
	{"HEVC", Video, []string{"libx265"}},
	{"H264", Video, []string{"libx264"}},
	{"MPEG4", Video, []string{"mpeg4"}},
}

// LookupCodec finds a codec by name, case-insensitively.
func LookupCodec(name string) (Codec, bool) {
	for _, c := range Codecs {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return Codec{}, false
}

// CodecsFor lists the codecs of kind k.
func CodecsFor(k Kind) []Codec {
	var out []Codec
	for _, c := range Codecs {
		if c.Kind == k {
			out = append(out, c)
		}
	}
	return out
}

// TargetsFor lists conversion targets for a track: same kind, excluding the track's own codec.
func TargetsFor(t Track) []Codec {
	var out []Codec
	for _, c := range CodecsFor(t.Kind) {
		if !strings.EqualFold(c.Name, t.Codec) {
			out = append(out, c)
		}
	}
	return out
}

type Op int

const (
	Copy Op = iota
	Drop
	Convert
)

// Action is what to do with one track during conversion.
type Action struct {
	Op    Op
	Codec Codec // set when Op == Convert
}

// ParseAction parses the form values "copy", "drop" and "convert:<codec>".
func ParseAction(s string) (Action, bool) {
	switch {
	case s == "" || s == "copy":
		return Action{Op: Copy}, true
	case s == "drop":
		return Action{Op: Drop}, true
	case strings.HasPrefix(s, "convert:"):
		c, ok := LookupCodec(strings.TrimPrefix(s, "convert:"))
		return Action{Op: Convert, Codec: c}, ok
	}
	return Action{}, false
}

func (a Action) String() string {
	switch a.Op {
	case Drop:
		return "drop"
	case Convert:
		return "convert:" + a.Codec.Name
	}
	return "copy"
}

// Specs maps a source stream index to its action. Missing indices are copied.
type Specs map[int]Action

// AutoSpecs plans an automatic conversion: audio tracks whose codec is a key of conversion
// (source codec name -> target codec name, case-insensitive) are converted. Mappings to the
// same codec are ignored, so a converted file is not converted again. Returns nil when there
// is nothing to do.
func AutoSpecs(f File, conversion map[string]string) Specs {
	targets := map[string]Codec{}
	for from, to := range conversion {
		c, ok := LookupCodec(to)
		if !ok || c.Kind != Audio || strings.EqualFold(from, to) {
			continue
		}
		targets[strings.ToLower(from)] = c
	}
	specs := Specs{}
	for _, t := range f.Audios() {
		if c, ok := targets[strings.ToLower(t.Codec)]; ok {
			specs[t.Index] = Action{Op: Convert, Codec: c}
		}
	}
	if len(specs) == 0 {
		return nil
	}
	return specs
}
