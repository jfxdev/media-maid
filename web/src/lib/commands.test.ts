import { execFileSync } from "node:child_process"
import { describe, expect, it } from "vitest"

import type { PendingItem } from "@/api/types"
import { commandsFor } from "./commands"

const base: PendingItem = {
  source: `Weird 'name' $(touch /tmp/pwned) "quoted".mkv`,
  status: "conflict",
  since: "2026-10-14T10:00:00Z",
  updated: "2026-10-14T10:00:00Z",
}

/** dataOf evaluates the --data argument with a real shell, as a user would. */
function dataOf(command: string, env: Record<string, string> = {}): string {
  const data = command.slice(command.indexOf("--data ") + "--data ".length)
  return execFileSync("sh", ["-c", `printf '%s' ${data}`], { env: { PATH: process.env.PATH, ...env } }).toString()
}

describe("commandsFor", () => {
  it("never embeds a token", () => {
    for (const status of ["conflict", "duplicate", "skipped", "error"] as const) {
      for (const c of commandsFor({ ...base, status }, "http://nas:8080")) {
        expect(c.command).toContain('"Authorization: Bearer $API_TOKEN"')
        expect(c.command).not.toMatch(/Bearer [A-Za-z0-9]{16}/)
      }
    }
  })

  it("quotes untrusted names so the shell passes them through", () => {
    const [preview, replace] = commandsFor(base, "http://nas:8080")
    expect(preview.command).toContain("'http://nas:8080/api/v1/preview'")
    expect(JSON.parse(dataOf(preview.command))).toEqual({ path: base.source })
    expect(replace.label).toBe("Replace the library file")
    expect(JSON.parse(dataOf(replace.command))).toEqual({ path: base.source, overwrite: true })
  })

  it("takes the TMDB id from $TMDB_ID and is invalid JSON without it", () => {
    const item: PendingItem = { ...base, status: "skipped", reason: "ambiguous_title", kind: "episode" }
    const identify = commandsFor(item, "http://nas:8080").find((c) => c.label === "Identify by TMDB id")!
    expect(JSON.parse(dataOf(identify.command, { TMDB_ID: "1396" }))).toEqual({
      path: base.source,
      kind: "episode",
      tmdb_id: 1396,
    })
    expect(() => JSON.parse(dataOf(identify.command))).toThrow()
  })

  it("offers a retry when TMDB was unavailable", () => {
    const labels = commandsFor({ ...base, status: "skipped", reason: "metadata_unavailable" }, "").map((c) => c.label)
    expect(labels).toEqual(["Preview", "Retry now"])
  })

  it("explains archives", () => {
    const labels = commandsFor({ ...base, status: "error", reason: "archive" }, "").map((c) => c.label)
    expect(labels).toEqual(["Preview", "Organize again (after extracting)"])
  })
})
