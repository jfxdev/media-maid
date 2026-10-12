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
   (`Guard`); resolves the three roots; `config.DistinctRoots` re-checks them.
3. Library roots are opened with `os.OpenRoot` and passed as
   `organizer.Library`.
4. `internal/integration`: `Client` (generic HTTP) + `Syncer` (debounced
   refresh). A nil client means sync is disabled (`ErrSyncDisabled`).
5. `internal/metadata`: `TMDB` provider when `TMDB_API_KEY` is set, else nil
   (the layer is skipped); `Search` + `Pick` scoring, `Titles` + `Localized`.
6. `internal/assist`: optional LLM decision layer (`LLM_PROVIDER`; nil
   `assist.Resolver` = disabled). OpenAI-compatible and Anthropic clients on
   stdlib `net/http`, fixed versioned prompts in `prompt.go`, strict decoding
   and validation in `validate.go`, cache and circuit breaker.
7. `internal/organizer`: `parser.Analyze` → `metadata` (candidates +
   `metadata.Pick`) → `assist` only when ambiguous (`identify.go`) →
   optional localization (`PREFERRED_LANGUAGE`) → `naming` → dedupe by
   quality → `linker` (hardlink, copy fallback, opt-in atomic replace) →
   `jobs.History` + `Syncer.Request()`.
8. `internal/orphans`: `Detector.Scan` builds a report; `Remove` revalidates
   each ID before acting.
9. `internal/watcher`: fsnotify, groups events by top-level release, waits
   `STABLE_SECONDS`, organizes on a single worker goroutine.
10. `internal/api`: stdlib `ServeMux`; `/api/v1/*` behind bearer auth;
   `/healthz`, `/openapi.yaml`, `/docs` are public.

Other packages: `parser` (release name → `Analysis`: `Media`, `Quality`,
confidence and signals; `Parse` is the `Media`-only wrapper), `naming` (paths +
`Sanitize`), `jobs` (ring-buffer history and **all** status/action strings).

## Platform table

All platform-specific data lives in `internal/integration/platforms.go`
(`PlatformSpec`). Everything else is generic.

| Mode | Sync | List items | Delete item | Auth header | ID tag |
|---|---|---|---|---|---|
| `jellyfin` | `POST /Library/Refresh` | `GET /Items` | `DELETE /Items/{id}` | `X-Emby-Token` | `[tmdbid-N]` |
| `emby` | `POST /emby/Library/Refresh` | `GET /emby/Items` | `DELETE /emby/Items/{id}` | `X-Emby-Token` | `[tmdbid=N]` |
| `plex` | `GET /library/sections/all/refresh` | `GET /library/sections/{id}/all` | `DELETE /library/metadata/{id}` | `X-Plex-Token` | `{tmdb-N}` |

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
  that was checked); the watcher
  and scans never overwrite. Releases sharing a destination in one run are
  deduplicated by `parser.Quality` before anything is written (`duplicate`).
- **Sanitize names**: anything from the parser or TMDB goes through
  `naming.Sanitize` before becoming a path component.
- **Orphans are report-only**: removal happens only via
  `POST /api/v1/orphans/remove`, after revalidation; doubt means keep
  (`containsVideo` treats walk errors as "has video"). Server items with any
  path outside the managed libraries are ignored.
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

- **New platform**: add a `PlatformSpec` entry in `platforms.go` (and a
  `ListItems` decoder if the format differs); add a compose file in
  `examples/`; update the table above.
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
- **Language**: code, comments, docs and examples are in English.
