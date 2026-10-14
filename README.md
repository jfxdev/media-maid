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

## When to use it (and when not)

Media Maid is the import step of a media stack, alone: for downloads you
start yourself (a torrent client, by hand) rather than through indexers.

- **Already on Sonarr/Radarr?** They import, rename, hardlink and refresh
  the server themselves; you do not need Media Maid. Use it for what you
  download outside them.
- **[FileBot](https://www.filebot.net/)** (the AMC script with
  `--action hardlink`) covers the renaming too, with far more naming options,
  as a paid desktop/CLI tool run per download. Media Maid is a free,
  always-on sidecar bound to one server: it watches, refreshes only the
  touched folders, keeps a ledger of what it placed (deletions are
  respected, upgrades are tracked) and lists what needs a decision.
- **Library cleanup tools** (Maintainerr and similar) delete by rules such
  as "watched 30 days ago". Media Maid's orphan report only lists broken
  entries (server items without files, folders with only metadata,
  downloads not in the library) and never removes anything by itself.
- **Not for you** if the library is not on the same filesystem as the
  downloads and disk space matters (hardlinks are the point), or if you need
  quality profiles, indexers or release monitoring.

## Quick start

Pick a ready-made compose file from [`examples/`](examples/) (one per server),
set `MEDIA_SERVER_TOKEN` and `MEDIA_MAID_API_TOKEN`, and run
`docker compose up -d`. Keep downloads and library on the **same filesystem**
so hardlinks work: otherwise organizing fails with an error (a startup warning
names the folders on different mounts), unless `COPY_FALLBACK=true` lets Media
Maid copy the files, using as much disk space again. Run it as the user that
owns the downloads (`user:` in compose): with `fs.protected_hardlinks`, the
default on most distributions, a process may only hardlink files it owns or
can write to, and the error says so (`hardlink not permitted (EPERM)`).

Images for `linux/amd64` and `linux/arm64` are published to
`ghcr.io/jfxdev/media-maid` for every version tag (`1.2.3`, `1.2`, `1` and
`latest`); pin a version in production.

Media Maid is built to run **in a container**: only volumes mounted into it
(read from `/proc/self/mountinfo`) are authorized, and the root filesystem
never is. Run directly on a host, folders on the root partition are refused
(`unauthorized_volume`); mount them as separate volumes or use the image. It
builds and runs on Linux only (macOS builds compile for development, without
atomic overwrite).

Each instance is bound to **exactly one** media server (`INTEGRATION_MODE`).
Multiple values, or `JELLYFIN_*` / `EMBY_*` / `PLEX_*` variables of *another*
server, abort startup; run one sidecar per server. Variables of the configured
server (say `PLEX_CLAIM` from a shared env file) are ignored with a warning.

## Configuration

| Variable | Required | Default | Description |
|---|---|---|---|
| `INTEGRATION_MODE` | yes | — | `jellyfin`, `emby` or `plex` |
| `MEDIA_SERVER_URL` | no¹ | — | e.g. `http://jellyfin:8096` |
| `MEDIA_SERVER_TOKEN` | no¹ | — | API key (Jellyfin/Emby) or X-Plex-Token |
| `API_TOKEN` | yes | — | bearer token for the API (≥ 32 chars) |
| `DOWNLOADS_DIR` | yes | — | source folder |
| `MOVIES_DIR` / `TV_DIR` | yes | — | library folders |
| `STATE_DIR` | yes | — | folder for caches, history, pending list, orphan report and the organize ledger, kept across restarts (see below) |
| `TMDB_API_KEY` | no | — | TMDB v3 key or v4 token for canonical title/year/ID |
| `PREFERRED_LANGUAGE` | no | — | BCP 47 tag (`pt-BR`, `es`) for localized folder names, see below |
| `LLM_PROVIDER` | no | — | `openai` or `anthropic`; leave empty to disable the LLM layer |
| `LLM_API_KEY` | no² | — | key for the LLM endpoint |
| `LLM_MODEL` | no² | `claude-haiku-5-5` (anthropic), `gpt-6-luna` (openai) | model name; required with `LLM_BASE_URL` on `openai` |
| `LLM_BASE_URL` | no | provider default | any OpenAI-compatible server (Ollama, LM Studio, OpenRouter, vLLM) or an Anthropic gateway |
| `LLM_MIN_CONFIDENCE` | no | `0.7` | below this parser confidence the LLM is consulted, and a reading TMDB has no match for waits as pending (also without the LLM); also the minimum confidence the LLM's answers need |
| `LLM_TIMEOUT` | no | `20s` | per request, at most `2m` |
| `SERVER_PATH_MAP` | no | — | `server_prefix=local_prefix` when the server sees other paths |
| `ORPHAN_SCAN_INTERVAL` | no | — | periodic orphan report (e.g. `24h`) |
| `SCAN_INTERVAL` | no | — | periodic downloads scan (e.g. `15m`), a fallback when filesystem events are missed (NFS/SMB) |
| `VIDEO_EXTENSIONS` | no | `mkv,mp4,avi,m4v,mov,wmv,ts,webm` | video allowlist |
| `SUBTITLE_EXTENSIONS` | no | `srt,ass,ssa,sub,idx,vtt` | subtitle allowlist |
| `SYNC_DEBOUNCE` | no | `30s` | coalesce links into one refresh (never delayed more than 5×) |
| `STABLE_SECONDS` | no | `30` | quiet time (no events, same size and modification time) before a download counts as done |
| `LISTEN_ADDR` | no | `:8080` | HTTP address |
| `DRY_RUN` | no | `false` | never link or remove anything |
| `COPY_FALLBACK` | no | `false` | `true` copies a file that cannot be hardlinked (different filesystem or mount), using extra disk space; by default that is an error |
| `ORGANIZE_TRIGGER` | no | `watch` | `watch`: the watcher (and `SCAN_INTERVAL`) organize downloads once they stop changing; `hook`: only API requests do, e.g. the torrent client's "download finished" hook (see below) |
| `AMBIGUOUS_MATCH` | no | `pending` | when TMDB's best candidate is weak or tied, or an unsure reading has no TMDB match, and nothing settles it: `pending` leaves the file pending for a manual identification; `best` takes the best candidate (or the parser's reading) anyway |
| `AUTO_UPGRADE` | no | `false` | let the watcher and scans replace a release Media Maid placed with a better one of the same title (see [Duplicate releases](#duplicate-releases-and-upgrades)) |
| `LINK_EXTRAS` | no | `true` | link videos from a release's extras folders (`Featurettes`, `Trailers`, …) as server extras |
| `NOTIFY_URL` | no | — | webhook for problems and upgrades (see [Notifications](#notifications)); a secret, never logged |
| `NOTIFY_FORMAT` | no | `json` | `json` (title, body and events; Apprise API) or `text` (plain body, `Title` header; ntfy) |
| `NOTIFY_ON` | no | `conflict,error,skipped,upgraded` | statuses to notify: any of `linked`, `copied`, `replaced`, `upgraded`, `conflict`, `duplicate`, `skipped`, `error` |
| `UI_ENABLED` | no | `false` | serve the read-only [web UI](#web-ui) at `/ui/` |
| `UI_TOKEN` | with `UI_ENABLED` | — | read-only bearer token of the web UI (≥ 32 chars, different from `API_TOKEN`): accepted on `GET` routes only; a secret, never logged |

¹ Set both or neither; without them, sync and server-side orphan checks are off.
² `LLM_API_KEY` is required with the public endpoints (no `LLM_BASE_URL`);
`LLM_MODEL` is required with `openai` plus `LLM_BASE_URL` (Ollama, LM Studio,
OpenRouter…), where the default model name does not exist.
The folders (including `STATE_DIR`) must be distinct, non-nested and inside
mounted volumes.

## API

Interactive docs are served at **`/docs`** and the OpenAPI spec at
**`/openapi.yaml`** (both unauthenticated; the spec is embedded in the binary,
source in [`internal/api/openapi.yaml`](internal/api/openapi.yaml)).
Every `/api/v1/*` route requires `Authorization: Bearer $API_TOKEN`. With
the web UI enabled, `UI_TOKEN` is accepted too, on `GET` routes only (any
other method answers `403` `read_only_token`); `GET` routes never change
anything.

| Method | Route | Description |
|---|---|---|
| `GET` | `/healthz` | liveness: `503` when the downloads watcher stopped or stalled |
| `POST` | `/api/v1/scan` | organize the whole downloads folder; `?async=true` answers `202` and logs to the history |
| `POST` | `/api/v1/organize` | `{"path": "...", "overwrite": false}` organize one file/folder; `kind`, `tmdb_id`, `title`, `year` identify it manually; `"automatic": true` behaves like the watcher (for hooks) |
| `POST` | `/api/v1/preview` | same as organize, without touching disk |
| `GET` | `/api/v1/pending` | downloads that need a decision: conflicts, duplicates, unidentified titles, errors |
| `GET` | `/api/v1/history` | recent actions and last sync |
| `POST` | `/api/v1/sync` | refresh the media server library now |
| `POST` | `/api/v1/orphans/scan` | build an orphan report |
| `GET` | `/api/v1/orphans` | latest report |
| `POST` | `/api/v1/orphans/remove` | `{"ids": [...]}` remove reported orphans |
| `GET` | `/api/v1/info` | version, integration mode and enabled layers (no secrets, no paths) |
| `GET` | `/api/v1/files` | `?root=downloads\|movies\|tv&path=...&offset=0&limit=200` list one folder: names and metadata only, never contents |

By default the watcher picks up a download once its files stop changing for
`STABLE_SECONDS`: no filesystem events, and the same size and modification
time. That is not proof it finished: a paused torrent, or one
without peers, stops changing too, and its partial file would be linked (a
hardlink then keeps showing whatever is written later). The torrent client
knows when a download is complete, so with `ORGANIZE_TRIGGER=hook` only API
requests organize, sent by the client's completion hook; the watcher and
periodic scans are off. **Use `hook` with clients built on libtorrent 2.x
(qBittorrent 4.4 and later, the default build)**: they may create a file at
its full size up front and write it through memory maps, which raise no
change events. The watcher also checks the modification time, which the
kernel moves as mapped pages are written, but a download that stalls between
pieces still looks finished to it.
(Keeping incomplete downloads in a separate folder, qBittorrent's *Keep
incomplete torrents in*, also helps in `watch` mode.) For qBittorrent (*Run external program on torrent finished*), use
[`examples/qbittorrent-hook.sh`](examples/qbittorrent-hook.sh):

```sh
/scripts/qbittorrent-hook.sh "%F"
```

It passes the content path (`%F`, which works with categories and subfolders)
JSON-encoded with `jq`, with `"automatic": true`: the request then behaves
like the watcher, so a hook fired again for the same torrent (after a
recheck) never links again a file you deleted from the library. Mount the downloads folder at the same path in both
containers. Never paste the torrent name into a JSON template by hand: the API
rejects duplicate keys and trailing data, so a crafted name cannot add fields
such as `overwrite`, but it would still break the request.

## Web UI

An optional, read-only web UI is embedded in the binary. Set
`UI_ENABLED=true` and a `UI_TOKEN` (`openssl rand -hex 32`, not the
`API_TOKEN`), then open `http://<host>:8080/ui/` and sign in with that token.
It shows:

- **Overview**: counts of conflicts, unidentified titles, errors and orphans,
  the watcher, the last sync and recent activity.
- **Pending**: conflicts and duplicates, unidentified titles and errors, with
  the full message and the `curl` call that settles each one (preview,
  overwrite, identification by TMDB id). The commands read `$API_TOKEN` (and
  `$TMDB_ID`) from your shell: the UI never has that token.
- **Errors**: what is failing now (downloads, the last sync, the orphan scan)
  and the failures in the recent history.
- **Orphans**: the latest report, by type.
- **Files**: the downloads, movies and TV folders, hidden entries included
  (and marked, with a switch to hide them). Only names, sizes, dates and hard
  link counts are shown: no file can be opened, read or downloaded, symlinks
  are listed but never followed, and nothing outside those three folders is
  reachable.

The UI only sends `GET` requests, and the `UI_TOKEN` cannot do anything else:
removing orphans, organizing and identifying stay API calls made with the
`API_TOKEN`. The token is kept in the browser tab's session storage, and every
file the page loads comes from the binary (a `'self'`-only Content Security
Policy, no CDN). Serve it over HTTPS (a reverse proxy) beyond your LAN.

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
   title, year and ID tag. When the best candidate is weak or tied (say
   `Dune.1080p.mkv`, with the 1984 and 2021 films) and the LLM is off or
   cannot settle it, the file is `skipped` as `ambiguous title: ...`, naming
   the candidates, and waits in `/api/v1/pending` for a manual
   identification, instead of getting a possibly wrong ID
   (`AMBIGUOUS_MATCH=best` takes the best candidate anyway). When TMDB finds
   nothing, the parser's reading is used without an ID if the parser is
   confident (`LLM_MIN_CONFIDENCE`, 0.7 by default); an unsure reading, say
   `One.Piece.E1080.1080p.mkv` read as a movie, is `skipped` as
   `uncertain title: ...` and waits in `/api/v1/pending` instead of creating
   a library entry under a guessed name (`AMBIGUOUS_MATCH=best` places it
   anyway). Without TMDB, nothing can confirm a reading and the parser
   decides. A tie is settled by
   popularity only when it is clear-cut: a candidate with at least 100 TMDB
   votes and ten times the votes of every other tied one. When TMDB cannot be
   reached, the file is `skipped` (`metadata unavailable: ...`, pending) and
   organized again every 10 minutes until TMDB answers, so a short outage
   never leaves an episode in a folder without the ID next to the tagged one.
3. **LLM** (optional, `LLM_PROVIDER`, off by default). Consulted only when
   the parser's confidence is below `LLM_MIN_CONFIDENCE`, TMDB found nothing,
   or the candidates are too close to call. It acts as a tie-breaker: it picks
   a TMDB candidate or confirms the parse. Its answer is treated as untrusted
   data and validated: season, episode and year numbers must appear in the
   file name (or in the chosen TMDB entry), a title it proposes outside the
   candidates must be confirmed by TMDB, and it never chooses paths, IDs or
   libraries.

Each organize result reports `resolved_by` (`parser`, `tmdb`, `llm` or
`manual`).

### Fixing a wrong identification

When a title is misread, name it yourself. `kind` (`movie` or `episode`) is
required; `tmdb_id` picks the TMDB entry exactly, `title` and `year` set the
name (a `title` alone is searched on TMDB and used as typed when nothing
matches). Season and episode numbers still come from the file name. Preview
first:

```sh
curl -sH "Authorization: Bearer $API_TOKEN" localhost:8080/api/v1/preview \
  -d '{"path": "Some.Release.2019.1080p", "kind": "movie", "tmdb_id": 496243}'
```

then send the same body to `/api/v1/organize`. If the file was placed under
the wrong name before, that entry is removed once the new one is in place
(`previous` names it; only while it still is the same file), and the ledger
remembers the correction, so scans keep it. Subtitles linked next to the
wrong entry stay behind; the orphan report lists the emptied folder.

A placed manual identification is also remembered for the name's reading
(kind, title, year and origin marker as parsed): once `Shameless.S01E01` was
identified as the US series, `Shameless.S01E02` and the next episodes get the
same entry without asking again (`resolved_by: manual`). A reading with a
year or a country marker is a different one. A new manual identification of
the same reading replaces the remembered one; previews remember nothing.

Release shapes the parser understands besides plain names:

- **Season packs** whose files only carry the episode (`Show.S01.COMPLETE/Show.E05.mkv`).
- **Multi-file movies** (`CD1`, `Disc 2`, or `Part1` after the year): one
  folder, files named `Title (Year) - part1.mkv`.
- **Editions** (Extended, Director's Cut, Unrated, Theatrical, IMAX, …): the
  title is searched without them; Plex gets `{edition-Extended Cut}`, Jellyfin
  and Emby a ` - Extended Cut` version file in the movie's folder.
- **Anime absolute numbering** (`[Group] Show - 1080`): with TMDB, the number
  is mapped onto the series' seasons; without, it stays `S01E1080`.
- **Extras folders** (`Extras`, `Featurettes`, `Trailers`, `Behind The
  Scenes`, `Deleted Scenes`, `Interviews`, `Scenes`, `Shorts`, `Clips`,
  `Bonus`) keep the release a single unit, so bonus videos never land in the
  library as movies of their own. With `LINK_EXTRAS` (default) they are
  linked into the server's extras folder inside the movie folder (or the
  show folder for series): `Featurettes/`, `Trailers/`, … on all servers;
  `Extras` and `Bonus` become `Extras/` (`Other/` on Plex), `Clips` stays
  `Clips/` on Jellyfin and becomes `Extras/` on Emby and `Other/` on Plex. A release that placed more than one title keeps its
  extras out. `Sample` folders and sample files are never linked.
- **Origin markers** (`The Office US`): TMDB is searched for `The Office` and
  the series from that country is preferred.

Compressed releases (RAR, including `.partNN.rar`/`.rNN` volumes, ZIP and 7z)
are not extracted. When a video release holds only archives, its first volume
is reported as an `error` (`archive: extract it first`) in
`/api/v1/pending` and notified once; extract it next to the archive (for
example with [unpackerr](https://github.com/Unpackerr/unpackerr)) and the
video is organized as usual while the archive leaves the pending list. Archives whose name does not look like a video
release (software, music) are ignored like any other file.

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
way.

A title identified by TMDB goes into the library folder that already carries
its ID, in any server's tag format (`[tmdbid-1]`, `[tmdbid=1]`, `{tmdb-1}`),
even under another name: a folder created by Sonarr/Radarr or by hand, or
before a TMDB title or translation changed, does not get a parallel folder
the server would list twice. A movie's file name follows that folder. Plex
editions keep their own folders, and when several folders carry the same ID
none is chosen. Folders without an ID tag are not matched. Results report `title_source` (`tmdb_translation`, `llm_localized` or
`original`).

## Duplicate releases and upgrades

Two releases of the same title (say `The.Matrix.1999.720p` and
`The.Matrix.1999.1080p.mkv`/`.mp4`) are the same library entry, whatever
their extension. Releases are ranked by resolution, then source (REMUX >
BluRay > WEB-DL > WEBRip > HDTV > DVD), then HDR, then file size.

- **In the same run**, only the best one is linked; the others are
  `duplicate`.
- **In later runs**, the new release is compared with the one in the library
  by the same ranking. When Media Maid placed that one (it is in the ledger,
  so its quality is known): a release that does not rank higher is
  `duplicate`; one that does (better quality, or equal quality and a larger
  file) is an `upgraded` replacement with `AUTO_UPGRADE=true`, and a
  `conflict` saying so without it. A library file Media Maid did not place is never
  touched: the result is `conflict`.

An upgrade, or an explicit `"overwrite": true` on organize (`replaced`),
swaps the file atomically: directories and symlinks are never replaced, nor a
file that changed after it was checked. A release under another extension is
linked first and the old file is then removed, only if it is still the file
that was checked (`previous` names it). The new release's subtitles replace
the old ones. The old download stays untouched; scans leave it alone from
then on, and the orphan report lists it as `unlinked_download` so you can
remove it in the torrent client. This needs a filesystem with `renameat2`
(ext4, XFS, Btrfs, tmpfs, overlayfs); elsewhere (e.g. NFS) the replacement
fails with an error instead of risking a race. Preview with `overwrite`
reports `planned` with `"replaces": true`.

Without TMDB, two releases of equal quality that map to the same name may be
different works (remakes sharing title and year); the second is reported as
`conflict` instead of `duplicate`.

## Notifications

`GET /api/v1/pending` lists the downloads that are not in the library and
need a decision: `conflict`, `duplicate`, `skipped` (title not identified) and
`error`, oldest first. An item goes away when the file is placed (a manual
identification, an `overwrite`) or leaves the downloads folder. Items (and
organize results) carry a stable `reason` when one applies
(`ambiguous_title`, `unidentified_title`, `metadata_unavailable`, `archive`,
…; see the OpenAPI spec): act on it rather than on the `error` text, which
is meant for people and may change.

With `NOTIFY_URL`, each **new or changed** pending item is posted once (a
conflict seen by every scan is not repeated), plus `upgraded` placements;
`NOTIFY_ON` chooses the statuses. Events of a burst are grouped into one
message after 10 seconds. `json` posts:

```json
{"title": "Media Maid: conflict The.Matrix.1999.1080p.mkv",
 "body": "conflict: The.Matrix.1999.1080p.mkv -> movies/The Matrix (1999)/The Matrix (1999).mkv (another release is in the library as ...)",
 "events": [{"time": "...", "source": "...", "dest": "...", "kind": "movie", "status": "conflict", "error": "..."}]}
```

which the [Apprise API](https://github.com/caronc/apprise-api)
(`http://apprise:8000/notify/<key>`) relays to Telegram, Discord, e-mail and
more. `NOTIFY_FORMAT=text` posts the body as plain text with a `Title`
header, which [ntfy](https://ntfy.sh) (`https://ntfy.sh/<topic>`) shows as is.
Delivery is best effort: a failure is logged (without the URL) and dropped.

## Watcher, restarts and persistence

The watcher groups files by **release folder**, not by the first folder under
`DOWNLOADS_DIR`: with torrent client categories (`downloads/movies/<release>`),
each release is organized as soon as it is complete, whatever else is still
downloading in the category. Loose files in a category folder are organized one
by one. If the kernel drops events (inotify queue overflow, e.g.
when a large pack is extracted at once), the whole downloads folder is walked
again. If a folder cannot be watched at all (the inotify watch limit,
`fs.inotify.max_user_watches`), `/healthz` reports `"watcher": "degraded"`
and, unless `SCAN_INTERVAL` is set, the downloads folder is scanned every 15
minutes so those downloads are still found.

Every placed video is recorded in an **organize ledger** (by device and inode,
so renames keep their entry; an entry follows its file when the filesystem
comes back under another device number, as btrfs, mergerfs or ZFS may after a
reboot). A library file that is not the download's own inode counts as placed
only when the ledger records it as that download's copy: equal size and
modification time alone never do. Automatic runs (watcher, scans) skip
identification for files already placed, and leave alone a file (video,
subtitle or extra) whose library copy you deleted (it is `skipped`, quietly). An explicit `POST /api/v1/organize`
ignores the ledger and links it again.

`STATE_DIR` keeps the ledger, the pending list, the remembered manual
identifications, the TMDB and LLM caches, the history and the last orphan
report across restarts, so a restart costs no
TMDB or LLM calls for work already done and never links again a file you
deleted from the library. Empty TMDB results expire after a day, so a title
added to TMDB later is found. The folder is created if missing; it must be
writable and inside a mounted volume (the image runs with a read-only root),
e.g. `STATE_DIR: /data/media-maid-state` as in the examples. TMDB rate limits
(HTTP 429) are retried up to three times, waiting what `Retry-After` asks (at
most 10 seconds).

After organizing, only the touched folders are refreshed on the server
(`Library/Media/Updated` on Jellyfin/Emby, `sections/{id}/refresh?path=` on
Plex); a full refresh is used for more than 50 folders or when that fails.

## Orphans

- `server_item_missing_file`: the server lists an item whose files are all gone
  (only when every path is inside the managed libraries). Removal fetches the
  item again and checks its current paths the same way before calling the
  server's delete endpoint.
  The servers usually drop these on their own library scan (Plex with *Empty
  trash automatically*); this report is a cross-check.
- `orphan_directory`: a library folder that is empty or holds only metadata
  (artwork, `.nfo`, `.xml`, `.txt`, subtitles). Any other file (an `.iso`, a
  Blu-ray `.m2ts`, a `.strm`, theme music) keeps the folder. Removal deletes
  it, while nothing is being organized.
- `unlinked_download`: a video in `DOWNLOADS_DIR` that is not in the library
  (deleted there, or never organized) and still uses disk space: one with no
  other hard link, or one the ledger placed whose library copy is gone even
  though it is linked elsewhere (a cross-seed folder). Report-only
  (`not_removable`): remove it in the torrent client. Files copied across
  filesystems are recognized through the ledger.

Nothing is removed automatically. Each ID is revalidated on removal and
returns `stale` if it is no longer an orphan.

## Security

- Constant-time bearer token check; tokens never appear in logs or responses.
- Only volumes mounted into the container (from `/proc/self/mountinfo`) are
  authorized; links pointing elsewhere fail with `unauthorized_volume`.
- Source files are opened with `O_NOFOLLOW` and verified with `fstat`; library
  writes go through `os.Root`; existing files are only replaced on an explicit
  `"overwrite": true` request, atomically and never through a symlink.
- Request bodies are strict JSON: unknown fields, duplicate keys and trailing
  data are rejected, so values interpolated by hooks cannot inject fields.
- Names from parsing, TMDB and the LLM are sanitized; server item IDs are
  validated. LLM answers are schema-checked and cross-checked against the
  file name and TMDB; `LLM_API_KEY` never appears in logs or errors.
- The optional web UI uses its own read-only `UI_TOKEN`, accepted on `GET`
  routes only (which never change anything). It lists folders of the three
  roots through `os.Root` (names and metadata, never contents; symlinks are
  not followed) and loads nothing from outside the binary (CSP `'self'`).
- Non-root distroless image; examples use `read_only`, `cap_drop: [ALL]` and
  `no-new-privileges`.

## Development

```sh
go test ./...
go build ./cmd/media-maid
docker build -t media-maid .
```

The web UI lives in [`web/`](web/) (React, TypeScript, Tailwind and
shadcn/ui; Node 22). `go build` works without it and serves a short notice at
`/ui/`; to embed the UI, build it first:

```sh
npm --prefix web ci
npm --prefix web run build   # writes internal/ui/dist, embedded by go build
npm --prefix web test
npm --prefix web run dev     # dev server; proxies /api to a media-maid on :8080
```

After changing `internal/api/openapi.yaml`, run `npm --prefix web run gen:api`
to regenerate the UI's API types.

Requires Go 1.25+. See [`AGENTS.md`](AGENTS.md) for architecture and
contribution notes. A misread release name is best reported as a line for
[`internal/parser/testdata/corpus.tsv`](internal/parser/testdata/corpus.tsv).

Releases: pushing a tag `vX.Y.Z` runs the CI and publishes the image to
GHCR (`.github/workflows/release.yml`). The first publish creates a private
package; make it public in the package settings on GitHub. Every action in
the workflows is pinned to a commit SHA.
