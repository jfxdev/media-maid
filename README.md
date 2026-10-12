<p align="center">
  <img src="docs/logo.png" alt="Media Maid" width="200">
</p>

<h1 align="center">media-maid</h1>

A sidecar for **Jellyfin**, **Emby** or **Plex** that watches your downloads
folder, **hardlinks** movies and episodes into a library with names the server
recognizes, triggers a library refresh and reports orphaned entries. Torrents
keep seeding and no extra disk space is used.

```
downloads/Inception.2010.1080p.BluRay.x264-GRP/Inception.2010.1080p.BluRay.x264-GRP.mkv
downloads/Inception.2010.1080p.BluRay.x264-GRP/Subs/Brazilian.srt
downloads/Dark.S01.1080p/Dark.S01E01E02.1080p.mkv
                         ↓
movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].mkv
movies/Inception (2010) [tmdbid-27205]/Inception (2010) [tmdbid-27205].pt-BR.srt
tv/Dark (2017) [tmdbid-70523]/Season 01/Dark S01E01-E02.mkv
```

The ID tag only appears when `TMDB_API_KEY` is set, in the server's format:
`[tmdbid-1]` (Jellyfin), `[tmdbid=1]` (Emby), `{tmdb-1}` (Plex).

## Quick start

Pick a ready-made compose file from [`examples/`](examples/) (one per server),
set `MEDIA_SERVER_TOKEN` and `MEDIA_MAID_API_TOKEN`, and run
`docker compose up -d`. Keep downloads and library on the **same filesystem**
so hardlinks work (otherwise files are copied).

Each instance is bound to **exactly one** media server (`INTEGRATION_MODE`).
Multiple values or `JELLYFIN_*` / `EMBY_*` / `PLEX_*` variables abort startup;
run one sidecar per server.

## Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `INTEGRATION_MODE` | yes | — | `jellyfin`, `emby` or `plex` |
| `MEDIA_SERVER_URL` | no¹ | — | e.g. `http://jellyfin:8096` |
| `MEDIA_SERVER_TOKEN` | no¹ | — | API key (Jellyfin/Emby) or X-Plex-Token |
| `API_TOKEN` | yes | — | bearer token for the API (≥ 32 chars) |
| `DOWNLOADS_DIR` | yes | — | source folder |
| `MOVIES_DIR` / `TV_DIR` | yes | — | library folders |
| `TMDB_API_KEY` | no | — | TMDB v3 key or v4 token for canonical title/year/ID |
| `PREFERRED_LANGUAGE` | no | — | BCP 47 tag (`pt-BR`, `es`) for localized folder names, see below |
| `LLM_PROVIDER` | no | — | `openai` or `anthropic`; leave empty to disable the LLM layer |
| `LLM_API_KEY` | no² | — | key for the LLM endpoint |
| `LLM_MODEL` | no² | `claude-opus-5-5` (anthropic) | model name; required for `openai` |
| `LLM_BASE_URL` | no | provider default | any OpenAI-compatible server (Ollama, LM Studio, OpenRouter, vLLM) or an Anthropic gateway |
| `LLM_MIN_CONFIDENCE` | no | `0.7` | below this parser confidence the LLM is consulted; also the minimum confidence its answers need |
| `LLM_TIMEOUT` | no | `20s` | per request, at most `2m` |
| `SERVER_PATH_MAP` | no | — | `server_prefix=local_prefix` when the server sees other paths |
| `ORPHAN_SCAN_INTERVAL` | no | — | periodic orphan report (e.g. `24h`) |
| `VIDEO_EXTENSIONS` | no | `mkv,mp4,avi,m4v,mov,wmv,ts,webm` | video allowlist |
| `SUBTITLE_EXTENSIONS` | no | `srt,ass,ssa,sub,idx,vtt` | subtitle allowlist |
| `SYNC_DEBOUNCE` | no | `30s` | coalesce links into one refresh |
| `STABLE_SECONDS` | no | `30` | quiet time before a download counts as done |
| `LISTEN_ADDR` | no | `:8080` | HTTP address |
| `DRY_RUN` | no | `false` | never link or remove anything |

¹ Set both or neither; without them, sync and server-side orphan checks are off.
² `LLM_API_KEY` is required with the public endpoints (no `LLM_BASE_URL`);
`LLM_MODEL` is required with `openai`.
The three folders must be distinct, non-nested and inside mounted volumes.

## API

Interactive docs are served at **`/docs`** and the OpenAPI spec at
**`/openapi.yaml`** (both unauthenticated; the spec is embedded in the binary,
source in [`internal/api/openapi.yaml`](internal/api/openapi.yaml)).
Every `/api/v1/*` route requires `Authorization: Bearer $API_TOKEN`.

| Method | Route | Description |
|---|---|---|
| `GET` | `/healthz` | liveness |
| `POST` | `/api/v1/scan` | organize the whole downloads folder |
| `POST` | `/api/v1/organize` | `{"path": "...", "overwrite": false}` organize one file/folder |
| `POST` | `/api/v1/preview` | same as organize, without touching disk |
| `GET` | `/api/v1/history` | recent actions and last sync |
| `POST` | `/api/v1/sync` | refresh the media server library now |
| `POST` | `/api/v1/orphans/scan` | build an orphan report |
| `GET` | `/api/v1/orphans` | latest report |
| `POST` | `/api/v1/orphans/remove` | `{"ids": [...]}` remove reported orphans |

qBittorrent hook (*Run external program on torrent finished*):

```sh
curl -s -X POST -H "Authorization: Bearer $API_TOKEN" \
  -d "{\"path\":\"%N\"}" http://media-maid:8080/api/v1/organize
```

## How titles are resolved

Identification runs in layers. Only the first is always on; each optional
layer can only refine the previous one, and when it fails, times out or is
disabled, the previous decision stands.

1. **Parser** (always, local, deterministic). Cleans the name (dots,
   underscores, `+`, `~`, spaced dashes, release groups, site prefixes,
   invisible Unicode; keeps `Spider-Man`, `Grey's`, `S.H.I.E.L.D`), extracts
   year, season/episodes (`S01E02`, `S01E01-E03`, `1x02`, `Season 1 Episode 2`,
   `T01E02`, air dates, `Part 2 of 6`, anime `- 12`) and quality, and scores
   its own confidence.
2. **TMDB** (optional, `TMDB_API_KEY`). Up to five candidates are scored
   against the parsed title and year; a clear winner sets the canonical
   title, year and ID tag.
3. **LLM** (optional, `LLM_PROVIDER`, off by default). Consulted only when
   the parser's confidence is below `LLM_MIN_CONFIDENCE`, TMDB found nothing,
   or the candidates are too close to call. It acts as a tie-breaker: it picks
   a TMDB candidate or confirms the parse. Its answer is treated as untrusted
   data and validated: season, episode and year numbers must appear in the
   file name (or in the chosen TMDB entry), a title it proposes outside the
   candidates must be confirmed by TMDB, and it never chooses paths, IDs or
   libraries.

Each organize result reports `resolved_by` (`parser`, `tmdb` or `llm`).

What the LLM receives: the file name and at most two parent folders, the
parser's reading and the TMDB candidates. Nothing else leaves the container.
Calls are cached, and after five consecutive failures the layer pauses for
five minutes. A local model works too:

```yaml
LLM_PROVIDER: openai
LLM_BASE_URL: http://ollama:11434/v1
LLM_MODEL: llama3.1
```

### Localized titles

With `PREFERRED_LANGUAGE` (e.g. `pt-BR`), folders use the official title in
that language: the TMDB translation for that language and country first; if
there is none, the LLM (when enabled) may only pick one of the titles TMDB
lists for that language or market; without TMDB, it may answer only when it
is confident an official title exists. Otherwise the original title is kept.
The ID tag still points at the TMDB entry, so the server matches it either
way. Results report `title_source` (`tmdb_translation`, `llm_localized` or
`original`).

## Duplicate releases

Two releases of the same title (say `The.Matrix.1999.720p` and
`The.Matrix.1999.1080p`) map to the same destination. Before anything is
written, releases in the same run are compared and only the best one is linked
(resolution, then source — REMUX > BluRay > WEB-DL > WEBRip > HDTV > DVD —,
then HDR, then file size); the others are reported as `duplicate`.

A destination that already exists with different content is a `conflict` and
is left alone. To upgrade it, call organize with `"overwrite": true`: the file
is replaced atomically (directories and symlinks never are), the old download
stays untouched, and the result is `replaced`. Preview with `overwrite` reports
`planned` with `"replaces": true`. The watcher and scans never overwrite.

## Orphans

- `server_item_missing_file`: the server lists an item whose files are all gone
  (only when every path is inside the managed libraries). Removal calls the
  server's delete endpoint.
- `orphan_directory`: a library folder with no video left. Removal deletes it.

Nothing is removed automatically. Each ID is revalidated on removal and
returns `stale` if it is no longer an orphan.

## Security

- Constant-time bearer token check; tokens never appear in logs or responses.
- Only volumes mounted into the container (from `/proc/self/mountinfo`) are
  authorized; links pointing elsewhere fail with `unauthorized_volume`.
- Source files are opened with `O_NOFOLLOW` and verified with `fstat`; library
  writes go through `os.Root`; existing files are only replaced on an explicit
  `"overwrite": true` request, atomically and never through a symlink.
- Names from parsing, TMDB and the LLM are sanitized; server item IDs are
  validated. LLM answers are schema-checked and cross-checked against the
  file name and TMDB; `LLM_API_KEY` never appears in logs or errors.
- Non-root distroless image; examples use `read_only`, `cap_drop: [ALL]` and
  `no-new-privileges`.

## Development

```sh
go test ./...
go build ./cmd/media-maid
docker build -t media-maid .
```

Requires Go 1.25+. See [`AGENTS.md`](AGENTS.md) for architecture and
contribution notes.
