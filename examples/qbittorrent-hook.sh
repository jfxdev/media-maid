#!/bin/sh
# qBittorrent "Run external program on torrent finished" hook for Media Maid:
#
#   /scripts/qbittorrent-hook.sh "%F"
#
# %F is the content path (file or folder). The downloads folder must be
# mounted at the same path in qBittorrent and Media Maid. The path is
# JSON-encoded with jq, never pasted into a JSON template.
#
# Needs: sh, curl, jq. Set MEDIA_MAID_URL and MEDIA_MAID_API_TOKEN in the
# qBittorrent container environment.
set -eu
: "${MEDIA_MAID_URL:=http://media-maid:8080}"
: "${MEDIA_MAID_API_TOKEN:?MEDIA_MAID_API_TOKEN is not set}"
[ $# -eq 1 ] || { echo "usage: $0 <content path>" >&2; exit 2; }

jq -n --arg path "$1" '{path: $path}' |
	curl -fsS -X POST \
		-H "Authorization: Bearer $MEDIA_MAID_API_TOKEN" \
		-H "Content-Type: application/json" \
		--data-binary @- \
		"$MEDIA_MAID_URL/api/v1/organize" >/dev/null
