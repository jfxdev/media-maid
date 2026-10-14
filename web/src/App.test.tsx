import { render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { describe, expect, it } from "vitest"

import { App } from "./App"
import { AuthProvider } from "./auth/auth"
import { info, mockFetch } from "./test/fetch"

const good = "r".repeat(32)

function api() {
  return mockFetch({
    "/api/v1/info": (_url, init) =>
      (init?.headers as Record<string, string>).Authorization === `Bearer ${good}`
        ? { body: info }
        : { status: 401, body: { error: "unauthorized" } },
    "/api/v1/pending": () => ({
      body: {
        items: [
          { source: "A.mkv", status: "conflict", since: "2026-10-14T10:00:00Z", updated: "2026-10-14T10:00:00Z" },
          { source: "B.mkv", status: "error", since: "2026-10-14T10:00:00Z", updated: "2026-10-14T10:00:00Z" },
        ],
      },
    }),
    "/api/v1/history": () => ({ body: { entries: [], last_sync: null } }),
    "/api/v1/orphans": () => ({ status: 404, body: { error: "no_report" } }),
    "/healthz": () => ({ body: { status: "ok" } }),
  })
}

function renderApp() {
  return render(
    <AuthProvider>
      <App />
    </AuthProvider>,
  )
}

describe("login", () => {
  it("rejects a wrong token and keeps nothing", async () => {
    api()
    renderApp()
    await userEvent.type(screen.getByLabelText("Token"), "wrong-token")
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }))
    expect(await screen.findByText("The token was rejected.")).toBeInTheDocument()
    expect(window.sessionStorage.getItem("media-maid.token")).toBeNull()
  })

  it("signs in, shows the overview and signs out", async () => {
    const calls = api()
    renderApp()
    await userEvent.type(screen.getByLabelText("Token"), good)
    await userEvent.click(screen.getByRole("button", { name: /sign in/i }))
    expect(await screen.findByRole("heading", { name: "Overview" })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByLabelText("Conflicts: 1")).toBeInTheDocument())
    expect(screen.getByLabelText("Errors: 1")).toBeInTheDocument()
    expect(window.sessionStorage.getItem("media-maid.token")).toBe(good)
    // The UI only reads.
    expect(calls.every((c) => (c.init?.method ?? "GET") === "GET")).toBe(true)

    await userEvent.click(screen.getByRole("button", { name: "Sign out" }))
    expect(await screen.findByLabelText("Token")).toBeInTheDocument()
    expect(window.sessionStorage.getItem("media-maid.token")).toBeNull()
  })

  it("signs out when the API answers 401", async () => {
    window.sessionStorage.setItem("media-maid.token", "revoked")
    api()
    renderApp()
    expect(await screen.findByLabelText("Token")).toBeInTheDocument()
    expect(window.sessionStorage.getItem("media-maid.token")).toBeNull()
  })
})
