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
| `SERVER_PATH_MAP` | no | — | `server_prefix=local_prefix` when the server sees other paths |
| `ORPHAN_SCAN_INTERVAL` | no | — | periodic orphan report (e.g. `24h`) |
| `VIDEO_EXTENSIONS` | no | `mkv,mp4,avi,m4v,mov,wmv,ts,webm` | video allowlist |
| `SUBTITLE_EXTENSIONS` | no | `srt,ass,ssa,sub,idx,vtt` | subtitle allowlist |
| `SYNC_DEBOUNCE` | no | `30s` | coalesce links into one refresh |
| `STABLE_SECONDS` | no | `30` | quiet time before a download counts as done |
| `LISTEN_ADDR` | no | `:8080` | HTTP address |
| `DRY_RUN` | no | `false` | never link or remove anything |

¹ Set both or neither; without them, sync and server-side orphan checks are off.
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
| `POST` | `/api/v1/organize` | `{"path": "..."}` organize one file/folder |
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
  writes go through `os.Root`; existing files are never overwritten.
- Names from parsing and TMDB are sanitized; server item IDs are validated.
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
