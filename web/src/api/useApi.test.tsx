import { act, render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { expect, it } from "vitest"

import { AuthProvider } from "@/auth/auth"
import { deferred, info, mockFetch, type Reply } from "@/test/fetch"
import { useApi } from "./useApi"

function Probe() {
  const { data, reload } = useApi<{ v: string }>("/api/v1/probe", 0)
  return (
    <>
      <span data-testid="value">{data?.v ?? "none"}</span>
      <button type="button" onClick={reload}>
        reload
      </button>
    </>
  )
}

// A reply that arrives after its request was superseded must not replace
// the newer data.
it("ignores a reply to a superseded request", async () => {
  window.sessionStorage.setItem("media-maid.token", "r".repeat(32))
  const replies = [deferred<Reply>(), deferred<Reply>()]
  let n = 0
  mockFetch({
    "/api/v1/info": () => ({ body: info }),
    "/api/v1/probe": () => replies[n++].promise,
  })
  render(
    <AuthProvider>
      <Probe />
    </AuthProvider>,
  )
  await userEvent.click(screen.getByRole("button", { name: "reload" }))
  expect(n).toBe(2)

  await act(async () => replies[1].resolve({ body: { v: "new" } }))
  expect(await screen.findByText("new")).toBeInTheDocument()
  await act(async () => replies[0].resolve({ body: { v: "old" } }))
  expect(screen.getByTestId("value")).toHaveTextContent("new")
})
