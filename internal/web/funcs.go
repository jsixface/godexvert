package web

import (
	"fmt"
	"html/template"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/jsixface/godexvert/internal/convert"
	"github.com/jsixface/godexvert/internal/library"
	"github.com/jsixface/godexvert/internal/media"
)

// funcs returns the template functions; some need the server's settings.
func (s *Server) funcs() template.FuncMap {
	return template.FuncMap{
		"qs":          url.QueryEscape,
		"join":        strings.Join,
		"dir":         filepath.Dir,
		"size":        size,
		"when":        when,
		"day":         func(ms int64) string { return time.UnixMilli(ms).Local().Format("2006-01-02") },
		"millis":      func(ms int64) string { return when(time.UnixMilli(ms)) },
		"dur":         dur,
		"eta":         eta,
		"icon":        icon,
		"codecs":      codecs,
		"codecClass":  codecClass,
		"res":         resLabel,
		"facts":       facts,
		"upper":       strings.ToUpper,
		"targets":     media.TargetsFor,
		"audioCodecs": func() []media.Codec { return media.CodecsFor(media.Audio) },
		"rule":        func(from, to string) autoconvRule { return autoconvRule{from, to} },
		"specs":       specs,
		"rel":         s.relPath,
		"running":     running,
		"queued":      queued,
		"add":         func(a, b int) int { return a + b },
		"sub":         func(a, b int) int { return a - b },
	}
}

type autoconvRule struct{ From, To string }

func size(mb int64) string {
	if mb == 0 {
		return "<1 MB"
	}
	if mb >= 1024 {
		return fmt.Sprintf("%.1f GB", float64(mb)/1024)
	}
	return fmt.Sprintf("%d MB", mb)
}

func when(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// eta estimates the time left from elapsed run time and percent done; empty until there is a basis.
func eta(j convert.Job) string {
	if j.Status != convert.InProgress || j.Progress <= 0 || j.Progress >= 100 || j.RunStart.IsZero() {
		return ""
	}
	left := time.Since(j.RunStart) * time.Duration(100-j.Progress) / time.Duration(j.Progress)
	return "~" + dur(left) + " left"
}

func dur(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s%3600/60, s%60)
	}
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

// codecs lists the distinct lower-cased codecs of tracks, in order.
func codecs(ts []media.Track) []string {
	var out []string
	for _, t := range ts {
		if c := strings.ToLower(t.Codec); !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}

// codecClass colours codec badges: efficient modern codecs, common ones, and the Dolby/DTS
// family that many players can't decode (what this app usually converts).
func codecClass(kind media.Kind, codec string) string {
	c := strings.ToLower(codec)
	switch kind {
	case media.Video:
		switch c {
		case "hevc", "h265", "av1", "vp9":
			return "c-good"
		case "h264":
			return "c-info"
		}
	case media.Audio:
		switch c {
		case "aac", "opus", "flac":
			return "c-good"
		case "mp3", "vorbis":
			return "c-info"
		case "ac3", "eac3", "dts", "truehd", "mlp":
			return "c-warn"
		}
	}
	return ""
}

type resolution struct{ Label, Class string }

// resLabel classifies a file's first video track as 4K/1080p/720p/SD.
func resLabel(f media.File) resolution {
	w, h := library.Dimensions(f)
	switch {
	case w == 0:
		return resolution{}
	case w >= 3800 || h >= 2100:
		return resolution{"4K", "r-uhd"}
	case w >= 1900 || h >= 1060:
		return resolution{"1080p", "r-fhd"}
	case w >= 1260 || h >= 700:
		return resolution{"720p", "r-hd"}
	}
	return resolution{"SD", "r-sd"}
}

// facts lists a track's details for display, excluding codec and language.
func facts(t media.Track) []string {
	var out []string
	add := func(s string) {
		if s != "" && s != "0x0" {
			out = append(out, s)
		}
	}
	switch t.Kind {
	case media.Video:
		add(t.Resolution)
		add(t.AspectRatio)
		if t.FrameRate > 0 {
			add(fmt.Sprintf("%.3g fps", t.FrameRate))
		}
		add(t.Profile)
		add(t.PixelFormat)
	case media.Audio:
		if t.Layout != "" {
			add(t.Layout)
		} else if t.Channels > 0 {
			add(fmt.Sprintf("%d ch", t.Channels))
		}
		if t.SampleRate != "" {
			add(t.SampleRate + " Hz")
		}
	}
	if t.BitRate > 0 {
		add(fmt.Sprintf("%d kb/s", t.BitRate/1000))
	}
	return out
}

// relPath shortens path to "<location name>/<path inside it>".
func (s *Server) relPath(p string) string {
	for _, loc := range s.Cfg.Get().LibraryLocations {
		if library.Within(p, []string{loc}) {
			r, _ := filepath.Rel(loc, p)
			return filepath.Join(filepath.Base(loc), r)
		}
	}
	return p
}

// running returns the job being converted, if any.
func running(jobs []convert.Job) *convert.Job {
	for _, j := range jobs {
		if j.Status != convert.Queued {
			return &j
		}
	}
	return nil
}

func queued(jobs []convert.Job) int {
	n := 0
	for _, j := range jobs {
		if j.Status == convert.Queued {
			n++
		}
	}
	return n
}

// specs summarises a job's non-copy actions, e.g. "ac3 #1 → AAC, drop subrip #3".
func specs(j convert.Job) string {
	var parts []string
	for _, t := range j.File.Tracks {
		switch a := j.Specs[t.Index]; a.Op {
		case media.Convert:
			parts = append(parts, fmt.Sprintf("%s #%d → %s", t.Codec, t.Index, a.Codec.Name))
		case media.Drop:
			parts = append(parts, fmt.Sprintf("drop %s #%d", t.Codec, t.Index))
		}
	}
	if len(parts) == 0 {
		return "remux"
	}
	return strings.Join(parts, ", ")
}

// Icon paths: 24×24, stroked with currentColor.
var icons = map[string]string{
	"film":     `<rect x="3" y="3" width="18" height="18" rx="2"/><path d="M7 3v18M17 3v18M3 7.5h4M3 12h18M3 16.5h4M17 7.5h4M17 16.5h4"/>`,
	"activity": `<path d="M22 12h-4l-3 9L9 3l-3 9H2"/>`,
	"archive":  `<rect x="2" y="3" width="20" height="5" rx="1"/><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8M10 12h4"/>`,
	"sliders":  `<path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3M1 14h6M9 8h6M17 16h6"/>`,
	"refresh":  `<path d="M21 12a9 9 0 1 1-2.64-6.36L21 8"/><path d="M21 3v5h-5"/>`,
	"x":        `<path d="M18 6 6 18M6 6l12 12"/>`,
	"search":   `<circle cx="11" cy="11" r="7"/><path d="m21 21-4.3-4.3"/>`,
	"trash":    `<path d="M3 6h18M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2M19 6l-1 14a2 2 0 0 1-2 2H8a2 2 0 0 1-2-2L5 6M10 11v6M14 11v6"/>`,
	"convert":  `<path d="M16 3l4 4-4 4M20 7H4M8 21l-4-4 4-4M4 17h16"/>`,
	"stop":     `<rect x="6" y="6" width="12" height="12" rx="1.5"/>`,
	"plus":     `<path d="M12 5v14M5 12h14"/>`,
	"check":    `<path d="M20 6 9 17l-5-5"/>`,
	"alert":    `<circle cx="12" cy="12" r="9"/><path d="M12 8v4M12 16h.01"/>`,
	"ban":      `<circle cx="12" cy="12" r="9"/><path d="m5.7 5.7 12.6 12.6"/>`,
	"clock":    `<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>`,
	"folder":   `<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>`,
	"video":    `<rect x="2" y="6" width="14" height="12" rx="2"/><path d="m16 10 6-3v10l-6-3z"/>`,
	"audio":    `<path d="M11 5 6 9H2v6h4l5 4z"/><path d="M15.5 8.5a5 5 0 0 1 0 7M19 5a10 10 0 0 1 0 14"/>`,
	"subtitle": `<rect x="2" y="5" width="20" height="14" rx="2"/><path d="M6 12h4M14 12h4M6 15h8"/>`,
	"up":       `<path d="m18 15-6-6-6 6"/>`,
	"down":     `<path d="m6 9 6 6 6-6"/>`,
	"save":     `<path d="M5 3h11l5 5v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2z"/><path d="M7 3v5h8M7 21v-7h10v7"/>`,
}

func icon(name string) template.HTML {
	return template.HTML(`<svg class="i" viewBox="0 0 24 24" aria-hidden="true">` + icons[name] + `</svg>`)
}
