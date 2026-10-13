# AGENTS.md

Guidance for AI agents and contributors working on Media Maid.

## What it is

A single-binary Go sidecar that runs next to one Jellyfin, Emby or Plex
container. It watches `DOWNLOADS_DIR`, parses release names, hardlinks videos,
subtitles and extras into `MOVIES_DIR` / `TV_DIR` with server-friendly names,
asks the media server to refresh, lists (and optionally notifies) downloads
that need a decision, and reports (never auto-removes) orphaned entries.
User docs: [`README.md`](README.md). API contract: [`internal/api/openapi.yaml`](internal/api/openapi.yaml).

## Commands

```sh
gofmt -l .                 # must print nothing
go vet ./...
go test -race ./...        # watcher tests take ~10s; CI runs all of these
GOOS=darwin go vet ./... # macOS must still compile (renameat2 is Linux only)
go build ./cmd/media-maid
docker build -t media-maid .
```

Go 1.25+ is required (`os.Root`). Dependencies are stdlib plus `fsnotify` and
`golang.org/x/sys`; avoid adding more. Linux-only syscalls live in
`*_linux.go` files with a `!linux` stub that fails safely.

CI (`.github/workflows/ci.yml`) runs the commands above; `release.yml`
publishes a multi-arch image to GHCR on `vX.Y.Z` tags. Every third-party
action is pinned to a full commit SHA with the version as a comment: keep it
that way when bumping (`git ls-remote --tags https://github.com/<action>`,
use the peeled `^{}` commit of annotated tags).

## Layout and flow

`cmd/media-maid/main.go` wires everything in this order:

1. `internal/config`: loads env vars, collects **all** errors with
   `errors.Join`, rejects `JELLYFIN_*` / `EMBY_*` / `PLEX_*` and multi-valued
   settings (single tenant). `STATE_DIR` is required.
2. `internal/safepath`: parses `/proc/self/mountinfo` into authorized volumes
   (`Guard`); resolves the roots (plus `STATE_DIR`); `config.DistinctRoots`
   re-checks them. `internal/state` then opens `STATE_DIR` (a nil
   `*state.Store`, used by tests, keeps everything in memory) and each
   persisted document (history, ledger, pending list, aliases, TMDB/LLM
   caches, orphan report) is loaded and saved by a debounced `state.Saver` through its
   `Export`/`Import`/`OnChange`.
3. Library roots are opened with `os.OpenRoot` and passed as
   `organizer.Library`.
4. `internal/integration`: `Client` (generic HTTP) + `Syncer` (debounced,
   max-wait refresh of the touched folders via `PlatformSpec.SyncPaths`, full
   refresh as fallback). A nil client means sync is disabled (`ErrSyncDisabled`).
5. `internal/metadata`: `TMDB` provider when `TMDB_API_KEY` is set, else nil
   (the layer is skipped); `Search` + `Pick` scoring, `Titles` + `Localized`,
   `Lookup` by id (manual identification). HTTP 429 is retried with
   `Retry-After` (bounded).
6. `internal/assist`: optional LLM decision layer (`LLM_PROVIDER`; nil
   `assist.Resolver` = disabled). OpenAI-compatible and Anthropic clients on
   stdlib `net/http`, fixed versioned prompts in `prompt.go`, strict decoding
   and validation in `validate.go`, cache and circuit breaker.
7. `internal/organizer`: `parser.Analyze` → `Ledger` lookup (automatic runs
   reuse a recorded placement) → a manual `Override` (explicit requests) or
   `metadata` (candidates + `metadata.PickIn`) → `assist` only when ambiguous
   (`identify.go`) → absolute episode mapping → optional localization
   (`PREFERRED_LANGUAGE`) → `naming` → `anchor` (the library folder already
   tagged with the TMDB id) → dedupe by quality within the run (key:
   destination without extension) → `placeVideo` compares with the release
   already in the library (`findPeer`: same name, any video extension; its
   quality from the ledger) → `linker` (hardlink, copy fallback, atomic
   replace on overwrite or `AUTO_UPGRADE`, `removeChecked` for the old file)
   → subtitles → extras (`LINK_EXTRAS`, `PlatformSpec.ExtrasDirs`) → `Ledger`
   + `Pending` + `Notify` + `jobs.History` + `Syncer.Request(folders...)`.
   Identification runs outside `Organizer.mu`; only dedupe and writes hold
   it.
8. `internal/orphans`: `Detector.Scan` builds a report (server items, empty
   library folders, downloads with a single hard link); `Remove` revalidates
   each ID before acting.
9. `internal/watcher`: fsnotify, groups events by release folder (`unitOf`:
   climbs season/subs/extras folders, a folder holding other releases is a
   category and its loose files are units), waits `STABLE_SECONDS`, organizes
   on a single worker goroutine. `SCAN_INTERVAL` adds a periodic scan with
   `MinAge` (every 15m anyway once `Degraded`: a folder could not be
   watched). `ORGANIZE_TRIGGER=hook` runs neither: only the API organizes.
10. `internal/api`: stdlib `ServeMux`; `/api/v1/*` behind bearer auth;
   `/healthz`, `/openapi.yaml`, `/docs` are public; `/healthz` answers 503
   when `watcher.Healthy` is false (`Run` returned or its loop stalled).

Other packages: `parser` (release name → `Analysis`: `Media` with part,
edition and absolute episode, `Quality`, country, confidence and signals;
`Parse` is the `Media`-only wrapper; `testdata/corpus.tsv` holds real-world
names), `naming` (paths + `Sanitize`), `jobs` (ring-buffer history and **all**
status/action strings), `state` (atomic JSON documents in `STATE_DIR`),
`notify` (batched webhook for `NOTIFY_URL`; the URL is a secret).

## Platform table

All platform-specific data lives in `internal/integration/platforms.go`
(`PlatformSpec`). Everything else is generic.

| Mode | Sync | Sync folders | List items | Delete item | Auth header | ID tag | Edition | Extras folders |
|---|---|---|---|---|---|---|---|---|
| `jellyfin` | `POST /Library/Refresh` | `POST /Library/Media/Updated` | `GET /Items` | `DELETE /Items/{id}` | `X-Emby-Token` | `[tmdbid-N]` | ` - Label` file | `Extras` (extras, bonus), `Clips`, `Featurettes`, `Trailers`, … |
| `emby` | `POST /emby/Library/Refresh` | `POST /emby/Library/Media/Updated` | `GET /emby/Items` | `DELETE /emby/Items/{id}` | `X-Emby-Token` | `[tmdbid=N]` | ` - Label` file | `Extras` (extras, bonus, clips), `Featurettes`, `Trailers`, … |
| `plex` | `GET /library/sections/all/refresh` | `GET /library/sections/{id}/refresh?path=` | `GET /library/sections/{id}/all` | `DELETE /library/metadata/{id}` | `X-Plex-Token` | `{tmdb-N}` | `{edition-Label}` | `Other` (extras, bonus, clips), `Featurettes`, `Trailers`, … |

## Invariants (do not break)

- **Filesystem confinement**: user paths go through `safepath.CleanInput`;
  sources through `Guard.Resolve` + `Guard.OpenRegular` (`O_NOFOLLOW` + `fstat`
  on the descriptor); library writes only through `os.Root`
  (`organizer/linker.go`). Never `os.Create`/`os.Remove` on library paths
  directly.
- **Never overwrite by default**: an existing different destination is
  `conflict`. Only an explicit `"overwrite": true` API request replaces it,
  or, with `AUTO_UPGRADE=true`, an automatic run replacing a release the
  **ledger** says Media Maid placed with one that ranks strictly higher
  (`parser.Quality`, then size; `upgraded`; unknown quality or a file not in the ledger is never
  upgraded). Regular files only: unique temp name,
  `renameat2(RENAME_EXCHANGE)` inside the `os.Root` directory, undone if the
  swapped-out entry is not the file that was checked; no plain-rename
  fallback. A file is only deleted through `removeChecked` (moved aside with
  `RENAME_NOREPLACE`, verified, else moved back) or when it is our staged
  file. Releases sharing a destination stem (any extension) in one run are
  deduplicated by `parser.Quality`, then size, before anything is written
  (`duplicate`; `conflict` when neither is identified by TMDB and qualities
  are equal); a release already in the library is ranked the same way.
- **Same file means same inode**: a library file that is not the source's
  inode is "already placed" only when `copyOf` confirms the ledger's copy of
  that source (status `copied`, same size/mtime, `DestIno`). Equal size and
  mtime alone never make two files the same.
- **Doubt is not a decision**: a weak or tied TMDB pick that no layer settles
  is `skipped` (`ambiguous title: ...`, pending) unless `AMBIGUOUS_MATCH=best`;
  it never gets an ID tag by default. The only automatic tie-break is
  popularity, when one tied candidate has ≥ 100 TMDB votes and ≥ 10× every
  other's (`metadata.PickIn`). A TMDB that cannot be reached is not
  "not found" either: the file is `skipped` (`metadata unavailable: ...`,
  pending) and `Organizer.RetryUnavailable` organizes it again later.
- **Manual identification** (`kind`/`tmdb_id`/`title`/`year` on organize or
  preview) only comes from an explicit API request, is validated in
  `api.override`, and its title is sanitized like any other. The earlier
  placement of the same file is removed only while it is provably that file.
  A placed one is remembered (`Aliases`, keyed by the parsed kind, folded
  title, year and country) and applied to later files with the same reading;
  imported aliases are validated again, and one TMDB rejects is ignored.
- **Respect deletions**: automatic runs never re-link a file (video or extra)
  the ledger says was placed and whose library copy is gone or was replaced
  by another release; only an explicit organize does.
- **Sanitize names**: anything from the parser or TMDB goes through
  `naming.Sanitize` before becoming a path component.
- **Strict request JSON**: `api.decode` rejects unknown fields, duplicate keys
  and trailing data (hooks interpolate untrusted names into JSON).
- **Orphans are report-only**: removal happens only via
  `POST /api/v1/orphans/remove`, after revalidation; doubt means keep
  (`containsContent` treats walk errors as content). An orphan folder holds
  only metadata (`isMetadata`); it is removed under `Organizer.Exclusive`.
  A server item is fetched again (`GetItem`) and its current paths checked
  before it is deleted. Server items with any path outside the managed
  libraries are ignored. `unlinked_download` is never
  removable: downloads belong to the torrent client.
- **State is data**: documents in `STATE_DIR` never hold secrets; cached LLM
  answers are validated again on every use.
- **Secrets**: `API_TOKEN`, `MEDIA_SERVER_TOKEN`, `TMDB_API_KEY`,
  `LLM_API_KEY` and `NOTIFY_URL` (it may embed a token) never reach logs,
  errors or responses; token comparison is constant time.
- **Notifications are best effort** and never block or fail organizing; a
  pending item is notified once per change, not on every scan.
- **Single tenant**: one media server per process.
- `DRY_RUN` must make every mutating path a no-op (`planned` / `dry_run`).
- **Copies are the exception**: a file that cannot be hardlinked (EXDEV,
  EPERM, EMLINK) is copied only while `COPY_FALLBACK` is on (off by
  default); with it off, `place` returns `errNoCopy` and writes nothing.
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
  `ListItems` decoder if the format differs, `GetItem` for one item (orphan
  removal needs it), `SyncPaths` if it can refresh
  single folders, `EditionTagFormat` if it has an edition tag, `ExtrasDirs`
  for the extras folders it reads); add a compose file in `examples/`; update
  the table above.
- **New API route**: handler in `internal/api/server.go`, update
  `internal/api/openapi.yaml` (`TestSpecCoversRoutes` fails otherwise), add a
  case to `TestAPI`, update the README route table.
- **New status/error code**: constant in `internal/jobs/history.go` or
  `api.fail`, plus the matching enum in `openapi.yaml`. Error codes are stable
  snake_case strings returned as `{"error": "..."}`.
- **Tests**: table-driven, real filesystems under `t.TempDir()`, small fakes
  for the interfaces each package consumes (`api.Organizer`, `api.Orphans`,
  `integration.LibrarySyncer`, `metadata.Provider`, `assist.Resolver`, `orphans.Server`),
  `httptest` for media server/TMDB/LLM/webhook clients. Add a regression test
  for every security fix, and a `parser/testdata/corpus.tsv` line for every
  misread release name.
- **Logging**: `log/slog` JSON; pass `*slog.Logger` explicitly.
- **Config**: every new variable is validated in `config.Load`, documented in
  the README table and, if relevant, in `examples/`.
- **Persistence**: a new persisted document gets `Export`/`Import` and an
  `OnChange` hook, wired with `persist(...)` in `main.go`; never store secrets.
- **Language**: code, comments, docs and examples are in English.
