# AGENTS.md

Guidance for AI agents and contributors working on Media Maid.

## What it is

A single-binary Go sidecar that runs next to one Jellyfin, Emby or Plex
container. It watches `DOWNLOADS_DIR`, parses release names, hardlinks videos
and subtitles into `MOVIES_DIR` / `TV_DIR` with server-friendly names, asks the
media server to refresh, and reports (never auto-removes) orphaned entries.
User docs: [`README.md`](README.md). API contract: [`internal/api/openapi.yaml`](internal/api/openapi.yaml).

## Commands

```sh
gofmt -l .                 # must print nothing
go vet ./...
go test ./...              # watcher tests take ~10s
go build ./cmd/media-maid
docker build -t media-maid .
```

Go 1.25+ is required (`os.Root`). Dependencies are stdlib plus `fsnotify` and
`golang.org/x/sys`; avoid adding more.

## Layout and flow

`cmd/media-maid/main.go` wires everything in this order:

1. `internal/config`: loads env vars, collects **all** errors with
   `errors.Join`, rejects `JELLYFIN_*` / `EMBY_*` / `PLEX_*` and multi-valued
   settings (single tenant).
2. `internal/safepath`: parses `/proc/self/mountinfo` into authorized volumes
   (`Guard`); resolves the roots (plus `STATE_DIR`); `config.DistinctRoots`
   re-checks them. `internal/state` then opens `STATE_DIR` (optional; a nil
   `*state.Store` keeps everything in memory) and each persisted document
   (history, ledger, TMDB/LLM caches, orphan report) is loaded and saved by a
   debounced `state.Saver` through its `Export`/`Import`/`OnChange`.
3. Library roots are opened with `os.OpenRoot` and passed as
   `organizer.Library`.
4. `internal/integration`: `Client` (generic HTTP) + `Syncer` (debounced,
   max-wait refresh of the touched folders via `PlatformSpec.SyncPaths`, full
   refresh as fallback). A nil client means sync is disabled (`ErrSyncDisabled`).
5. `internal/metadata`: `TMDB` provider when `TMDB_API_KEY` is set, else nil
   (the layer is skipped); `Search` + `Pick` scoring, `Titles` + `Localized`.
6. `internal/assist`: optional LLM decision layer (`LLM_PROVIDER`; nil
   `assist.Resolver` = disabled). OpenAI-compatible and Anthropic clients on
   stdlib `net/http`, fixed versioned prompts in `prompt.go`, strict decoding
   and validation in `validate.go`, cache and circuit breaker.
7. `internal/organizer`: `parser.Analyze` → `Ledger` lookup (automatic runs
   reuse a recorded placement) → `metadata` (candidates + `metadata.PickIn`) →
   `assist` only when ambiguous (`identify.go`) → absolute episode mapping →
   optional localization (`PREFERRED_LANGUAGE`) → `naming` → dedupe by
   quality → `linker` (hardlink, copy fallback, opt-in atomic replace) →
   `Ledger` + `jobs.History` + `Syncer.Request(folders...)`. Identification
   runs outside `Organizer.mu`; only dedupe and writes hold it.
8. `internal/orphans`: `Detector.Scan` builds a report (server items, empty
   library folders, downloads with a single hard link); `Remove` revalidates
   each ID before acting.
9. `internal/watcher`: fsnotify, groups events by release folder (`unitOf`:
   climbs season/subs/extras folders, a folder holding other releases is a
   category and its loose files are units), waits `STABLE_SECONDS`, organizes
   on a single worker goroutine. `SCAN_INTERVAL` adds a periodic scan with
   `MinAge`.
10. `internal/api`: stdlib `ServeMux`; `/api/v1/*` behind bearer auth;
   `/healthz`, `/openapi.yaml`, `/docs` are public; `/healthz` answers 503
   when `watcher.Healthy` is false (`Run` returned or its loop stalled).

Other packages: `parser` (release name → `Analysis`: `Media` with part,
edition and absolute episode, `Quality`, country, confidence and signals;
`Parse` is the `Media`-only wrapper), `naming` (paths + `Sanitize`), `jobs`
(ring-buffer history and **all** status/action strings), `state` (atomic JSON
documents in `STATE_DIR`).

## Platform table

All platform-specific data lives in `internal/integration/platforms.go`
(`PlatformSpec`). Everything else is generic.

| Mode | Sync | Sync folders | List items | Delete item | Auth header | ID tag | Edition |
|---|---|---|---|---|---|---|---|
| `jellyfin` | `POST /Library/Refresh` | `POST /Library/Media/Updated` | `GET /Items` | `DELETE /Items/{id}` | `X-Emby-Token` | `[tmdbid-N]` | ` - Label` file |
| `emby` | `POST /emby/Library/Refresh` | `POST /emby/Library/Media/Updated` | `GET /emby/Items` | `DELETE /emby/Items/{id}` | `X-Emby-Token` | `[tmdbid=N]` | ` - Label` file |
| `plex` | `GET /library/sections/all/refresh` | `GET /library/sections/{id}/refresh?path=` | `GET /library/sections/{id}/all` | `DELETE /library/metadata/{id}` | `X-Plex-Token` | `{tmdb-N}` | `{edition-Label}` |

## Invariants (do not break)

- **Filesystem confinement**: user paths go through `safepath.CleanInput`;
  sources through `Guard.Resolve` + `Guard.OpenRegular` (`O_NOFOLLOW` + `fstat`
  on the descriptor); library writes only through `os.Root`
  (`organizer/linker.go`). Never `os.Create`/`os.Remove` on library paths
  directly.
- **Never overwrite by default**: an existing different destination is
  `conflict`. Only an explicit `"overwrite": true` API request replaces it
  (regular files only: unique temp name, `renameat2(RENAME_EXCHANGE)` inside
  the `os.Root` directory, undone if the swapped-out entry is not the file
  that was checked; no plain-rename fallback; an entry is only deleted when
  it is provably the checked file or our staged file); the watcher
  and scans never overwrite. Releases sharing a destination in one run are
  deduplicated by `parser.Quality` before anything is written (`duplicate`;
  `conflict` when neither is identified by TMDB and qualities are equal).
- **Respect deletions**: automatic runs never re-link a file the ledger says
  was placed and whose library copy is gone; only an explicit organize does.
- **Sanitize names**: anything from the parser or TMDB goes through
  `naming.Sanitize` before becoming a path component.
- **Strict request JSON**: `api.decode` rejects unknown fields, duplicate keys
  and trailing data (hooks interpolate untrusted names into JSON).
- **Orphans are report-only**: removal happens only via
  `POST /api/v1/orphans/remove`, after revalidation; doubt means keep
  (`containsVideo` treats walk errors as "has video"). Server items with any
  path outside the managed libraries are ignored. `unlinked_download` is never
  removable: downloads belong to the torrent client.
- **State is data**: documents in `STATE_DIR` never hold secrets; cached LLM
  answers are validated again on every use.
- **Secrets**: `API_TOKEN`, `MEDIA_SERVER_TOKEN`, `TMDB_API_KEY` and
  `LLM_API_KEY` never reach logs, errors or responses; token comparison is
  constant time.
- **Single tenant**: one media server per process.
- `DRY_RUN` must make every mutating path a no-op (`planned` / `dry_run`).
- Server item IDs are validated (`reItemID`) before being put in URLs.
- **The LLM layer is optional**: the pipeline must behave the same with
  `LLM_PROVIDER` unset, and every LLM failure falls back to the parser/TMDB
  decision. LLM output is untrusted data: schema-checked, numbers must appear
  in the name or TMDB, free titles need TMDB confirmation (when TMDB is on),
  localized titles must be one of TMDB's when TMDB is on, and it never
  becomes a path, ID or library choice. Release names reach the model only
  JSON-encoded in the user message, cleaned of control characters. Bump
  `promptVersion` when changing a prompt or schema. `LLM_API_KEY` is a secret.

## Conventions

- **New platform**: add a `PlatformSpec` entry in `platforms.go` (a
  `ListItems` decoder if the format differs, `SyncPaths` if it can refresh
  single folders, `EditionTagFormat` if it has an edition tag); add a compose
  file in `examples/`; update the table above.
- **New API route**: handler in `internal/api/server.go`, update
  `internal/api/openapi.yaml` (`TestSpecCoversRoutes` fails otherwise), add a
  case to `TestAPI`, update the README route table.
- **New status/error code**: constant in `internal/jobs/history.go` or
  `api.fail`, plus the matching enum in `openapi.yaml`. Error codes are stable
  snake_case strings returned as `{"error": "..."}`.
- **Tests**: table-driven, real filesystems under `t.TempDir()`, small fakes
  for the interfaces each package consumes (`api.Organizer`, `api.Orphans`,
  `integration.LibrarySyncer`, `metadata.Provider`, `assist.Resolver`, `orphans.Server`),
  `httptest` for media server/TMDB/LLM clients. Add a regression test for every
  security fix.
- **Logging**: `log/slog` JSON; pass `*slog.Logger` explicitly.
- **Config**: every new variable is validated in `config.Load`, documented in
  the README table and, if relevant, in `examples/`.
- **Persistence**: a new persisted document gets `Export`/`Import` and an
  `OnChange` hook, wired with `persist(...)` in `main.go`; never store secrets.
- **Language**: code, comments, docs and examples are in English.
