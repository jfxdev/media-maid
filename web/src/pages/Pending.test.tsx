import { act, render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import type { PendingItem } from "@/api/types"
import { AuthProvider } from "@/auth/auth"
import { info, mockFetch } from "@/test/fetch"
import { filterPending, Pending } from "./Pending"

const t = "2026-10-14T10:00:00Z"
const items: PendingItem[] = [
  { source: "Conflict.2010.mkv", status: "conflict", dest: "movies/Conflict (2010)/Conflict (2010).mkv", since: t, updated: t },
  { source: "Dup.2011.mkv", status: "duplicate", since: t, updated: t },
  { source: "Tied.Title.mkv", status: "skipped", reason: "ambiguous_title", error: "ambiguous title: A or B", since: t, updated: t },
  { source: "Unknown.mkv", status: "skipped", reason: "unidentified_title", since: t, updated: t },
  { source: "Broken.rar", status: "error", reason: "archive", error: "extract first", since: t, updated: t },
]

describe("filterPending", () => {
  it.each([
    ["all", null, "", 5],
    ["conflicts", null, "", 2],
    ["unidentified", null, "", 2],
    ["unidentified", "ambiguous_title", "", 1],
    ["errors", null, "", 1],
    ["all", null, "a or b", 1],
    ["all", null, "CONFLICT (2010)", 1],
  ] as const)("%s / %s / %s", (tab, reason, q, n) => {
    expect(filterPending(items, tab, reason, q)).toHaveLength(n)
  })
})

describe("Pending", () => {
  function setup(tab?: string) {
    window.sessionStorage.setItem("media-maid.token", "r".repeat(32))
    mockFetch({
      "/api/v1/info": () => ({ body: info }),
      "/api/v1/pending": () => ({ body: { items } }),
    })
    render(
      <AuthProvider>
        <Pending tab={tab} />
      </AuthProvider>,
    )
  }

  it("shows a tab and its reasons", async () => {
    setup("unidentified")
    expect(await screen.findByText("Tied.Title.mkv")).toBeInTheDocument()
    expect(screen.queryByText("Conflict.2010.mkv")).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole("button", { name: "unidentified title" }))
    expect(screen.queryByText("Tied.Title.mkv")).not.toBeInTheDocument()
    expect(screen.getByText("Unknown.mkv")).toBeInTheDocument()
  })

  it("opens the details with commands that hold no token", async () => {
    setup("conflicts")
    await userEvent.click(await screen.findByText("Conflict.2010.mkv"))
    const dialog = await screen.findByRole("dialog")
    expect(within(dialog).getByText("Replace the library file")).toBeInTheDocument()
    const text = dialog.textContent ?? ""
    expect(text).toContain("$API_TOKEN")
    expect(text).not.toContain("r".repeat(32))
    expect(text).toContain('"overwrite":true')
  })

  it("drops a reason filter whose reason is gone after a refresh", async () => {
    window.sessionStorage.setItem("media-maid.token", "r".repeat(32))
    let calls = 0
    mockFetch({
      "/api/v1/info": () => ({ body: info }),
      // The ambiguous item is resolved between the two loads.
      "/api/v1/pending": () => ({ body: { items: calls++ === 0 ? items : items.filter((i) => i.reason !== "ambiguous_title") } }),
    })
    render(
      <AuthProvider>
        <Pending tab="unidentified" />
      </AuthProvider>,
    )
    await userEvent.click(await screen.findByRole("button", { name: "ambiguous title" }))
    expect(screen.queryByText("Unknown.mkv")).not.toBeInTheDocument()
    await act(async () => userEvent.click(screen.getByRole("button", { name: "Refresh" })))
    expect(await screen.findByText("Unknown.mkv")).toBeInTheDocument()
    expect(screen.queryByText("No match")).not.toBeInTheDocument()
  })
})
