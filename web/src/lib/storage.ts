// Browser storage can be missing or throw (private windows, blocked site
// data): every access is guarded and the app works without it.

export function readStorage(store: "session" | "local", key: string): string | null {
  try {
    return (store === "session" ? window.sessionStorage : window.localStorage).getItem(key)
  } catch {
    return null
  }
}

export function writeStorage(store: "session" | "local", key: string, value: string | null) {
  try {
    const s = store === "session" ? window.sessionStorage : window.localStorage
    if (value === null) s.removeItem(key)
    else s.setItem(key, value)
  } catch {
    // Not persisted; the value still lives in memory.
  }
}
