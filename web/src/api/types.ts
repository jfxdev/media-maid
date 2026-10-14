import type { components, paths } from "./schema"

// Types come from internal/api/openapi.yaml (npm run gen:api).
type Schemas = components["schemas"]

type GetJSON<P extends keyof paths> = paths[P] extends {
  get: { responses: { 200: { content: { "application/json": infer T } } } }
}
  ? T
  : never

export type PendingItem = Schemas["PendingItem"]
export type HistoryEntry = Schemas["HistoryEntry"]
export type SyncStatus = Schemas["SyncStatus"]
export type Report = Schemas["Report"]
export type Orphan = Schemas["Orphan"]
export type ReportError = Schemas["ReportError"]
export type Info = Schemas["Info"]
export type Listing = Schemas["Listing"]
export type FileEntry = Schemas["FileEntry"]
export type Reason = Schemas["Reason"]
export type ErrorCode = Schemas["Error"]["error"]
export type RootName = Listing["root"]

export type PendingResponse = GetJSON<"/api/v1/pending">
export type HistoryResponse = GetJSON<"/api/v1/history">
export type Health = GetJSON<"/healthz"> | { status: "unhealthy"; watcher: "stopped" }
