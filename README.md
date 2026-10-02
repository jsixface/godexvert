# GodeXvert

A small web app that scans your video library and converts audio/subtitle tracks with ffmpeg — for
example, re-encoding AC3 audio to AAC so a media player that can't handle it will still play the file.

GodeXvert is a Go + [htmx](https://htmx.org) rewrite of [CodeXvert](https://github.com/jsixface/codexvert)
(Kotlin). It ships as a single static binary (no CGO, no JS toolchain) and a multi-arch Docker image.

## Features

- Library scan of one or more folders, kept up to date by a recursive file watcher
- Per-track detail view (video, audio, subtitles) with filters
- Convert, copy, or drop individual tracks; jobs run one at a time with live progress (SSE) and can be cancelled
- Auto-conversion rules (e.g. `AC3 → AAC`) applied to new files as they appear
- Optional backups of originals (`.bkp`), with a page to review and delete them
- The converted file is staged next to the original before the swap, so a failed move can't lose data
- Settings editable in the UI; SQLite database for library and job history

## Requirements

- `ffmpeg` and `ffprobe` on `PATH` (included in the Docker image)
- Go 1.27+ to build from source

## Quick start

### Docker

```bash
docker build -t godexvert .
docker run -p 8080:8080 -v /path/to/videos:/videos -v godexvert-data:/app/data godexvert
```

Then open <http://localhost:8080> and add `/videos` as a library location under **Settings**.
Mount the settings file as well (`-v ./godexvert.json:/app/.godexvert.json`) if you want it to live
outside the container.

### From source

```bash
make run        # go run ./cmd/godexvert, http://localhost:8080
make build      # static binary in bin/godexvert
```

## Configuration

Environment variables:

| Variable    | Default                                  | Description                                    |
|-------------|------------------------------------------|------------------------------------------------|
| `PORT`      | `8080`                                   | HTTP port                                      |
| `BIND`      | `localhost` (`0.0.0.0` in the image)     | Bind address                                   |
| `DB_PATH`   | `data/godexvert.db`                      | SQLite database file                           |
| `CONFIG`    | `$HOME/.godexvert.json`                  | Settings file                                  |
| `LOG_LEVEL` | `info`                                   | `debug`, `info`, `warn` or `error`             |

Settings (edited in the UI, stored as JSON in the settings file):

- `libraryLocations` — folders to scan
- `workspaceLocation` — scratch directory for in-progress conversions (default `/tmp/vid-con`)
- `videoExtensions` — file extensions to treat as video (default `avi, mp4, mkv, mpeg4`)
- `autoConversion` — source codec → target codec map; identity rules like `AAC → AAC` are ignored
- `takeBackups` — keep the original as a backup after conversion (default on)

If you are migrating from CodeXvert, an existing `~/.codexvert.json` is read until the first save, at
which point `~/.godexvert.json` is written. The job history database is not carried over; a rescan
rebuilds the library.

## Security

There is no authentication. Run it on a trusted network or behind a reverse proxy that provides auth.
File paths from requests are rejected unless they fall inside a configured library location.

When proxying, disable response buffering for `/events` (e.g. `X-Accel-Buffering: no` on nginx), or live
progress will stall.

## Development

```bash
make test       # go test -race ./...
make vet
make build-linux  # linux/amd64 and linux/arm64 binaries
```

Layout:

```
cmd/godexvert/     entry point, env wiring
internal/config/   settings file
internal/store/    SQLite + embedded migrations
internal/probe/    ffprobe parsing
internal/library/  scanning
internal/watcher/  fsnotify watcher and auto-convert
internal/convert/  job queue, ffmpeg command builder, progress parser
internal/backups/  backup listing/deletion
internal/events/   SSE broadcaster
internal/web/      handlers and routes
web/               embedded templates and static assets
```

See [PLAN.md](PLAN.md) for design decisions and behaviour differences from the Kotlin app.

## License

[AGPL-3.0](LICENSE)
