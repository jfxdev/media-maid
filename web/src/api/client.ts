import type { Health } from "./types"

/** ApiError carries the HTTP status and the API's stable error code. */
export class ApiError extends Error {
  readonly status: number
  readonly code: string

  constructor(status: number, code: string) {
    super(code)
    this.name = "ApiError"
    this.status = status
    this.code = code
  }
}

/**
 * getJSON fetches a read-only API route. The UI only ever sends GET requests;
 * the token goes in the Authorization header to this same origin (relative
 * URL, no cookies, no referrer).
 */
export async function getJSON<T>(path: string, token: string | null, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" }
  if (token) headers.Authorization = `Bearer ${token}`
  const res = await fetch(path, {
    method: "GET",
    headers,
    signal,
    credentials: "omit",
    cache: "no-store",
    referrerPolicy: "no-referrer",
  })
  if (!res.ok) {
    let code = `http_${res.status}`
    try {
      const body: unknown = await res.json()
      if (body && typeof body === "object" && "error" in body && typeof body.error === "string") code = body.error
    } catch {
      // Not JSON: keep the HTTP code.
    }
    throw new ApiError(res.status, code)
  }
  return (await res.json()) as T
}

/** getHealth reads the public liveness probe; 503 still carries a body. */
export async function getHealth(signal?: AbortSignal): Promise<Health> {
  const res = await fetch("/healthz", { signal, credentials: "omit", cache: "no-store" })
  if (res.status !== 200 && res.status !== 503) throw new ApiError(res.status, `http_${res.status}`)
  return (await res.json()) as Health
}

/** errorMessage turns an error into a sentence for people. */
export function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    switch (err.code) {
      case "unauthorized":
        return "The token was rejected."
      case "not_found":
        return "Not found."
      case "not_a_directory":
        return "That is not a folder: only folders can be listed."
      case "invalid_path":
        return "Invalid path."
      case "unauthorized_volume":
        return "That path leads outside the folder (symlink)."
      case "permission_denied":
        return "Media Maid cannot read that folder (permissions)."
      case "no_report":
        return "No orphan report yet."
      default:
        return `Request failed (${err.code}).`
    }
  }
  if (err instanceof Error && err.name === "AbortError") return "Cancelled."
  return "Cannot reach Media Maid."
}
