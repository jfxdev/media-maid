import { render, screen, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import { AuthProvider } from "@/auth/auth"
import { info, mockFetch } from "@/test/fetch"
import { Files, filesQuery } from "./Files"

const t = "2026-10-14T10:00:00Z"

function listing(url: URL) {
  const path = url.searchParams.get("path") ?? ""
  const offset = Number(url.searchParams.get("offset"))
  const all = [
    { name: ".incomplete", type: "dir", hidden: true, modified: t },
    { name: "Show S01", type: "dir", hidden: false, modified: t },
    { name: "Movie.2010.mkv", type: "file", size: 2 * 1024 ** 3, links: 1, media: "video", hidden: false, modified: t },
    { name: ".DS_Store", type: "file", size: 10, links: 1, hidden: true, modified: t },
  ]
  const page = path === "" ? all.slice(offset, offset + 3) : []
  return { body: { root: url.searchParams.get("root"), path, entries: page, total: path === "" ? 4 : 0, truncated: false } }
}

describe("filesQuery", () => {
  it("encodes the folder", () => {
    expect(filesQuery("tv", "Show S01/Extras & more", 200)).toBe(
      "/api/v1/files?root=tv&offset=200&limit=200&path=Show+S01%2FExtras+%26+more",
    )
    expect(filesQuery("downloads", "", 0)).toBe("/api/v1/files?root=downloads&offset=0&limit=200")
  })
})

describe("Files", () => {
  function setup(path = "") {
    window.sessionStorage.setItem("media-maid.token", "r".repeat(32))
    const calls = mockFetch({ "/api/v1/info": () => ({ body: info }), "/api/v1/files": listing })
    render(
      <AuthProvider>
        <Files root="downloads" path={path} />
      </AuthProvider>,
    )
    return calls
  }

  it("lists hidden entries, links folders only and loads more", async () => {
    const calls = setup()
    const table = await screen.findByRole("table")
    expect(within(table).getByText(".incomplete")).toBeInTheDocument()
    expect(within(table).getByRole("link", { name: "Show S01" })).toHaveAttribute("href", "#/files/downloads/Show%20S01")
    // A file is plain text: nothing to open or download.
    const file = within(table).getByText("Movie.2010.mkv")
    expect(file.closest("a")).toBeNull()
    expect(within(table).getByText("2.0 GB")).toBeInTheDocument()

    await userEvent.click(screen.getByRole("button", { name: /load more/i }))
    expect(await within(table).findByText(".DS_Store")).toBeInTheDocument()
    const offsets = calls.filter((c) => c.url.pathname === "/api/v1/files").map((c) => c.url.searchParams.get("offset"))
    expect(offsets).toEqual(["0", "3"])
  })

  it("hides hidden entries on request and remembers it", async () => {
    setup()
    const table = await screen.findByRole("table")
    await userEvent.click(screen.getByRole("switch", { name: /show hidden/i }))
    expect(within(table).queryByText(".incomplete")).not.toBeInTheDocument()
    expect(within(table).getByText("Show S01")).toBeInTheDocument()
    expect(window.localStorage.getItem("media-maid.show-hidden")).toBe("false")
  })

  it("builds breadcrumbs and the parent link for nested folders", async () => {
    setup("Show S01/Season 1")
    expect(await screen.findByRole("link", { name: "Show S01" })).toHaveAttribute("href", "#/files/downloads/Show%20S01")
    expect(screen.getByRole("link", { name: /parent folder/i })).toHaveAttribute("href", "#/files/downloads/Show%20S01")
    expect(screen.getByText("Season 1")).toHaveAttribute("aria-current", "page")
  })
})
