import { useSyncExternalStore } from "react"

// A hash router: the UI is served as static files at /ui/, so every view
// lives in the fragment (#/pending, #/files/downloads/Some.Folder) and the
// server never sees it.

function subscribe(cb: () => void) {
  window.addEventListener("hashchange", cb)
  return () => window.removeEventListener("hashchange", cb)
}

function getHash() {
  return window.location.hash
}

function decode(s: string): string {
  try {
    return decodeURIComponent(s)
  } catch {
    return s
  }
}

/** parseHash splits "#/files/tv/a%20b" into ["files", "tv", "a b"]. */
export function parseHash(hash: string): string[] {
  return hash
    .replace(/^#\/?/, "")
    .split("/")
    .filter((s) => s !== "")
    .map(decode)
}

/** href builds a hash link from path segments, each one encoded. */
export function href(...segments: string[]): string {
  return "#/" + segments.filter((s) => s !== "").map(encodeURIComponent).join("/")
}

/** useRoute returns the current route segments. */
export function useRoute(): string[] {
  const hash = useSyncExternalStore(subscribe, getHash, () => "")
  return parseHash(hash)
}

export function navigate(to: string) {
  window.location.hash = to
}
