// Package convert runs ffmpeg conversion jobs one at a time and swaps the result into the library.
package convert

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jsixface/godexvert/internal/media"
)

// BuildArgs returns the ffmpeg arguments (without the binary) converting in to out.
// Every track is mapped in source order; tracks without a spec are copied, dropped tracks are
// omitted. Codec options are addressed by output stream number, which only counts mapped tracks.
func BuildArgs(in media.File, specs media.Specs, out string) []string {
	args := []string{"-hide_banner", "-nostdin", "-i", in.Path}
	tracks := slices.Clone(in.Tracks)
	slices.SortFunc(tracks, func(a, b media.Track) int { return a.Index - b.Index })
	n := 0
	for _, t := range tracks {
		a := specs[t.Index]
		if a.Op == media.Drop {
			continue
		}
		args = append(args, "-map", "0:"+strconv.Itoa(t.Index), "-codec:"+strconv.Itoa(n))
		if a.Op == media.Convert {
			args = append(args, a.Codec.Params...)
		} else {
			args = append(args, "copy")
		}
		n++
	}
	return append(args, out)
}

// CommandLine renders the ffmpeg invocation as a copy-pasteable shell command.
func CommandLine(args []string) string {
	parts := []string{"ffmpeg"}
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./:=,+@%", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

var (
	durationRe = regexp.MustCompile(`^\s*Duration: (\d+):(\d+):(\d+(?:\.\d+)?)`)
	timeRe     = regexp.MustCompile(`time=(\d+):(\d+):(\d+(?:\.\d+)?)`)
)

// Progress turns ffmpeg log lines into a completion percentage.
type Progress struct {
	total time.Duration
}

// Line consumes one log line and returns the percentage (0-99) when it carries progress.
// The first "Duration:" line (the input's) sets the total.
func (p *Progress) Line(line string) (int, bool) {
	if p.total == 0 {
		if m := durationRe.FindStringSubmatch(line); m != nil {
			p.total = hms(m)
			return 0, false
		}
	}
	m := timeRe.FindStringSubmatch(line)
	if m == nil || p.total <= 0 {
		return 0, false
	}
	pct := int(hms(m) * 100 / p.total)
	return min(max(pct, 0), 99), true
}

func hms(m []string) time.Duration {
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	s, _ := strconv.ParseFloat(m[3], 64)
	return time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(s*float64(time.Second))
}

// Runner runs ffmpeg with args, calling onLine for each line of its log output.
// It must stop the process when ctx is cancelled.
type Runner func(ctx context.Context, args []string, onLine func(string)) error

// FFmpeg is the Runner that executes the ffmpeg binary.
func FFmpeg(ctx context.Context, args []string, onLine func(string)) error {
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.WaitDelay = 5 * time.Second
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	tail := scanLines(stderr, onLine)
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &ExitError{Err: err, Tail: tail}
	}
	return nil
}

// ExitError is a failed ffmpeg run with the last lines of its output.
type ExitError struct {
	Err  error
	Tail string
}

func (e *ExitError) Error() string {
	if e.Tail == "" {
		return e.Err.Error()
	}
	return e.Err.Error() + ": " + e.Tail
}

func (e *ExitError) Unwrap() error { return e.Err }

// scanLines splits r on \n or \r (ffmpeg redraws its progress line with \r) and returns
// the last non-progress line, which usually explains a failure.
func scanLines(r io.Reader, onLine func(string)) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	sc.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	var last string
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		onLine(line)
		if !timeRe.MatchString(line) {
			last = line
		}
	}
	io.Copy(io.Discard, r)
	return last
}
