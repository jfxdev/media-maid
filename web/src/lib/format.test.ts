import { describe, expect, it } from "vitest"

import { formatBytes, formatRelative, humanize, parseTime, shellQuote } from "./format"

describe("shellQuote", () => {
  it.each([
    ["plain", "'plain'"],
    ["with space", "'with space'"],
    ["it's", `'it'\\''s'`],
    ["$HOME `id` $(rm -rf /)", "'$HOME `id` $(rm -rf /)'"],
    ["", "''"],
  ])("%s", (input, want) => {
    expect(shellQuote(input)).toBe(want)
  })
})

describe("formatBytes", () => {
  it.each([
    [0, "0 B"],
    [1023, "1023 B"],
    [1536, "1.5 KB"],
    [5 * 1024 ** 3, "5.0 GB"],
    [42 * 1024 ** 3, "42 GB"],
    [undefined, "—"],
    [-1, "—"],
  ])("%s", (n, want) => {
    expect(formatBytes(n)).toBe(want)
  })
})

describe("times", () => {
  const now = Date.parse("2026-10-14T12:00:00Z")
  it("treats Go's zero time as missing", () => {
    expect(parseTime("0001-01-01T00:00:00Z")).toBeNull()
    expect(formatRelative("0001-01-01T00:00:00Z", now)).toBe("—")
  })
  it("is relative", () => {
    expect(formatRelative("2026-10-14T11:59:50Z", now)).toBe("just now")
    expect(formatRelative("2026-10-14T11:55:00Z", now)).toBe("5 minutes ago")
    expect(formatRelative("2026-10-14T09:00:00Z", now)).toBe("3 hours ago")
    expect(formatRelative("2026-10-13T12:00:00Z", now)).toBe("yesterday")
  })
})

it("humanizes codes", () => {
  expect(humanize("ambiguous_title")).toBe("ambiguous title")
  expect(humanize(undefined)).toBe("")
})
