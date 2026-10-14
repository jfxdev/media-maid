import { describe, expect, it } from "vitest"

import { mockFetch } from "@/test/fetch"
import { ApiError, errorMessage, getJSON } from "./client"

describe("getJSON", () => {
  it("sends a GET with the bearer token and no cookies", async () => {
    const calls = mockFetch({ "/api/v1/pending": () => ({ body: { items: [] } }) })
    await expect(getJSON("/api/v1/pending", "tok")).resolves.toEqual({ items: [] })
    const init = calls[0].init!
    expect(init.method).toBe("GET")
    expect(init.credentials).toBe("omit")
    expect((init.headers as Record<string, string>).Authorization).toBe("Bearer tok")
  })

  it("throws the API error code", async () => {
    mockFetch({ "/api/v1/files": () => ({ status: 400, body: { error: "not_a_directory" } }) })
    const err = await getJSON("/api/v1/files?root=tv&path=x.mkv", "tok").catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(400)
    expect((err as ApiError).code).toBe("not_a_directory")
    expect(errorMessage(err)).toMatch(/only folders/)
  })
})
