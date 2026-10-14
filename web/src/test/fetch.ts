import { vi } from "vitest"

export type Handler = (url: URL, init?: RequestInit) => { status?: number; body: unknown }

/** mockFetch routes fetch calls by pathname and records them. */
export function mockFetch(routes: Record<string, Handler>) {
  const calls: { url: URL; init?: RequestInit }[] = []
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://media-maid.test")
    calls.push({ url, init })
    const h = routes[url.pathname]
    const res = h ? h(url, init) : { status: 404, body: { error: "not_found" } }
    return new Response(JSON.stringify(res.body), {
      status: res.status ?? 200,
      headers: { "Content-Type": "application/json" },
    })
  })
  vi.stubGlobal("fetch", fn)
  return calls
}

export const info = {
  version: "1.2.3",
  mode: "jellyfin",
  dry_run: false,
  organize_trigger: "watch",
  sync_enabled: true,
  tmdb_enabled: true,
  llm_enabled: false,
  roots: ["downloads", "movies", "tv"],
}
