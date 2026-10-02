// Package backups lists and deletes the "<file>.<unix>.bkp" originals kept after conversion.
package backups

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/jsixface/godexvert/internal/library"
)

var pattern = regexp.MustCompile(`\.([0-9]+)\.bkp$`)

type Backup struct {
	Path   string
	Name   string
	Time   time.Time // when the backup was taken
	SizeMB int64
}

// IsBackup reports whether name looks like a backup file.
func IsBackup(name string) bool { return pattern.MatchString(name) }

// List finds backups under the given locations, newest first.
func List(locations []string) []Backup {
	var out []Backup
	for _, loc := range locations {
		filepath.WalkDir(loc, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			m := pattern.FindStringSubmatch(d.Name())
			if m == nil {
				return nil
			}
			b := Backup{Path: p, Name: d.Name()}
			if ts, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				b.Time = time.Unix(ts, 0)
			}
			if info, err := d.Info(); err == nil {
				b.SizeMB = info.Size() / 1024 / 1024
			}
			out = append(out, b)
			return nil
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out
}

var ErrInvalid = errors.New("not a backup inside a library location")

// Delete removes one backup. The path must be a backup file inside one of locations.
func Delete(locations []string, path string) error {
	if !IsBackup(filepath.Base(path)) || !library.Within(path, locations) {
		return ErrInvalid
	}
	return os.Remove(filepath.Clean(path))
}

// DeleteAll removes every backup under locations and returns how many were removed.
func DeleteAll(locations []string) (int, error) {
	n := 0
	var errs []error
	for _, b := range List(locations) {
		if err := os.Remove(b.Path); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}
