# GodeXvert (Go + htmx) — Rewrite Plan

Goal: a small, static Linux binary (`CGO_ENABLED=0`), one language, no JS toolchain.
Port of the Kotlin CodeXvert app at `~/workspace/codexvert` (Ktor + Compose wasm).

## Decisions
- **Repo:** sibling repo, `~/workspace/codexvert-go`.
- **DB:** fresh SQLite via `modernc.org/sqlite` (pure Go), embedded SQL migrations. No Liquibase adoption;
  the library is derived data and a re-scan rebuilds it. Job history is not carried over. SQLite only
  (no Postgres/MySQL).
- **Settings:** `~/.godexvert.json`, same format as `~/.codexvert.json`; the old file is read until the
  first save, so existing settings carry over.
- **UI:** `html/template` + vendored htmx. Full pages and fragments share templates (`HX-Request` header).
- **Live progress:** SSE. A single `/events` stream, fed by an in-process broadcaster; the htmx SSE
  extension swaps progress fragments. Jobs page falls back to a normal render on load.
- **Deps:** `modernc.org/sqlite`, `fsnotify`, htmx (vendored), stdlib `net/http` (1.22+ mux), `log/slog`.

## Layout
```
cmd/godexvert/main.go          wiring, env (PORT, BIND, DB path)
internal/config/               settings file (load/save, mutex)
internal/store/                sqlite + migrations/*.sql (embedded)
internal/probe/                ffprobe -> structs
internal/library/              scan + fsnotify (recursive, debounced)
internal/convert/              queue, ffmpeg command builder, progress parser
internal/backups/              list/delete .bkp files
internal/events/               SSE broadcaster
internal/web/                  handlers + routes
web/templates, web/static      embedded assets
```

## Routes
| Method + path | Behaviour |
|---|---|
| `GET /` | Library page; filters via `hx-get` swapping table body |
| `GET /videos/detail?path=` | Detail fragment into side pane |
| `POST /videos/convert` | Per-track conversion form -> enqueue; toast fragment |
| `POST /videos/refresh` | Rescan |
| `GET /jobs?page=` | Jobs page, paginated |
| `DELETE /jobs/{id}` | Cancel/stop |
| `GET /events` | SSE: `job-<id>` progress/status fragments, `jobs-changed` |
| `GET /backups`, `DELETE /backups`, `DELETE /backups/item?path=` | List / delete (hx-confirm) |
| `GET/POST /settings` | Form; auto-conversion map as repeatable rows |
| `GET /healthz` | Liveness |

## Phases
1. **Scaffold** (done): module, Makefile, embed, slog, `/healthz`, static linux build.
2. **Foundation** (done): config, store + migrations, probe parsing, library scan.
3. **Library UI** (done): home, filters, detail pane, refresh.
4. **Conversion** (done): ffmpeg arg builder + progress parser (tested), serial worker, cancel via
   `exec.CommandContext`, backup-and-swap, jobs page, SSE progress.
5. **Watcher + auto-conversion** (done): fsnotify, recursive registration on dir create, extension filter,
   quiet-period debounce, auto-convert path. `WatchersTest.kt` ported to `internal/watcher` + `internal/media`.
6. **Backups + settings pages** (done).
7. **Packaging** (done, Dockerfile not yet built locally): multi-stage Dockerfile (Alpine + ffmpeg, multi-arch).
   GitHub Actions CI runs vet + race tests on every `master` push (image tag `testing`) and tag push
   (image tags `latest` and the tag name).
8. **Parity pass** against a real library: still to do. Smoke-tested end to end with generated files and
   real ffmpeg/ffprobe (manual convert, drop, auto-convert in a new dir, cancel, backups).

## Env
`PORT` (8080), `BIND` (localhost), `DB_PATH` (data/godexvert.db), `CONFIG` ($HOME/.godexvert.json, falling back to .codexvert.json), `LOG_LEVEL` (info).

## Behaviour changes vs the Kotlin app
- Output streams are mapped in source order and `-codec:N` counts only kept streams (Kotlin counted dropped
  tracks too, so codec options landed on the wrong stream after a Delete).
- Job status comes from ffmpeg's exit code (Kotlin reported failures as Completed); new `Cancelled` status;
  the failure reason (last ffmpeg line) is stored and shown on hover.
- Auto-conversion ignores identity rules (the default `AAC -> AAC` would re-encode in a loop).
- The converted file is staged next to the original before the original is moved, so a failed cross-device
  move cannot lose the file.
- A library location that can't be read (e.g. unmounted share) keeps its rows instead of being wiped.
- Detail/convert/backup endpoints reject paths outside the configured library locations.
- History pagination is separate from active jobs; a full rescan runs at startup.

## Risks / notes
- ffmpeg arg builder + progress parsing is the logic that matters most; port with tests first.
- Validate all `path=` inputs against configured library/backup dirs (the Kotlin app doesn't).
- No auth in the original; assume a reverse proxy or add basic auth later.
- SSE behind a reverse proxy needs buffering disabled (`X-Accel-Buffering: no`); send a periodic comment keep-alive.
