import { expect, it } from "vitest"

import { href, parseHash } from "./router"

it("round-trips segments with special characters", () => {
  const segs = ["files", "downloads", "Show S01 [1080p]", "100% #1?.mkv"]
  expect(parseHash(href(...segs))).toEqual(segs)
})

it("parses empty and malformed hashes", () => {
  expect(parseHash("")).toEqual([])
  expect(parseHash("#/")).toEqual([])
  expect(parseHash("#/pending/conflicts")).toEqual(["pending", "conflicts"])
  expect(parseHash("#/files/%E0%A4%A")).toEqual(["files", "%E0%A4%A"])
})
