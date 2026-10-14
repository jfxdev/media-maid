import type { PendingItem } from "@/api/types"
import { shellQuote } from "./format"

// The UI is read-only: to act on a pending item it offers the API call to
// run elsewhere. The token is never put in a command, only $API_TOKEN.

export interface Command {
  label: string
  description: string
  command: string
}

function curl(origin: string, route: string, data: string): string {
  return [
    `curl -sS -X POST ${shellQuote(origin + route)} \\`,
    `  -H "Authorization: Bearer $API_TOKEN" \\`,
    `  -H 'Content-Type: application/json' \\`,
    `  --data ${data}`,
  ].join("\n")
}

function json(body: Record<string, unknown>): string {
  return shellQuote(JSON.stringify(body))
}

/**
 * withTMDBId builds the body with "tmdb_id" taken from $TMDB_ID. While the
 * variable is unset the body is invalid JSON, so the API refuses the request
 * (400) instead of identifying the file with a placeholder.
 */
function withTMDBId(body: Record<string, unknown>): string {
  const head = JSON.stringify(body).slice(0, -1) + ',"tmdb_id":'
  return `${shellQuote(head)}"$TMDB_ID"${shellQuote("}")}`
}

/** commandsFor lists the API calls that settle a pending item. */
export function commandsFor(item: PendingItem, origin: string): Command[] {
  const path = item.source
  const out: Command[] = [
    {
      label: "Preview",
      description: "Shows where the file would go. Changes nothing.",
      command: curl(origin, "/api/v1/preview", json({ path })),
    },
  ]
  switch (item.status) {
    case "conflict":
    case "duplicate":
      out.push({
        label: "Replace the library file",
        description:
          "Atomically replaces what is at the destination (or the same title under another extension) with this release.",
        command: curl(origin, "/api/v1/organize", json({ path, overwrite: true })),
      })
      break
    case "skipped":
      if (item.reason === "metadata_unavailable") {
        out.push({
          label: "Retry now",
          description: "TMDB could not be reached; this is also retried automatically every 10 minutes.",
          command: curl(origin, "/api/v1/organize", json({ path })),
        })
        break
      }
      out.push({
        label: "Identify by TMDB id",
        description: `Set TMDB_ID first (the id from themoviedb.org). Use "kind": "${
          item.kind === "episode" ? "movie" : "episode"
        }" if this is a ${item.kind === "episode" ? "movie" : "series"}.`,
        command: curl(origin, "/api/v1/organize", withTMDBId({ path, kind: item.kind ?? "movie" })),
      })
      break
    case "error":
      out.push({
        label: item.reason === "archive" ? "Organize again (after extracting)" : "Organize again",
        description:
          item.reason === "archive"
            ? "Extract the archive next to it first, then organize the folder again."
            : "Runs the organizer again on this file.",
        command: curl(origin, "/api/v1/organize", json({ path })),
      })
      break
  }
  return out
}
