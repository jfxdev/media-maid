import { useMemo, useState } from "react"
import { Search } from "lucide-react"

import { ApiError } from "@/api/client"
import type { Orphan, Report } from "@/api/types"
import { useApi } from "@/api/useApi"
import { useAuth } from "@/auth/auth"
import { EmptyState, ErrorState, PageHeader, RefreshButton, TableSkeleton, TimeAgo, Truncated } from "@/components/common"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { formatBytes } from "@/lib/format"

const types: Record<Orphan["type"], { label: string; help: string }> = {
  server_item_missing_file: {
    label: "Server items without files",
    help: "The media server lists them, but none of their files exist.",
  },
  orphan_directory: {
    label: "Empty folders",
    help: "Library folders that are empty or hold only artwork, nfo or subtitles.",
  },
  unlinked_download: {
    label: "Unlinked downloads",
    help: "Videos in the downloads that are not in the library but use disk space. Remove them in the torrent client.",
  },
}

type Filter = "all" | Orphan["type"]

export function Orphans() {
  const { info } = useAuth()
  const { data, error, loading, reload } = useApi<Report>("/api/v1/orphans", 60_000)
  const [filter, setFilter] = useState<Filter>("all")
  const [query, setQuery] = useState("")
  const noReport = error instanceof ApiError && error.code === "no_report"

  const orphans = useMemo(() => data?.orphans ?? [], [data])
  const rows = useMemo(() => {
    const q = query.trim().toLowerCase()
    return orphans.filter(
      (o) =>
        (filter === "all" || o.type === filter) &&
        (q === "" || o.path.toLowerCase().includes(q) || (o.title ?? "").toLowerCase().includes(q)),
    )
  }, [orphans, filter, query])

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Orphans"
        description={
          data ? (
            <>
              Report generated <TimeAgo time={data.generated_at} />. Nothing is removed automatically; removal is an
              explicit API call that checks each entry again.
            </>
          ) : (
            "Library entries that no longer match the filesystem."
          )
        }
        actions={<RefreshButton onClick={reload} loading={loading} />}
      />

      {noReport && (
        <EmptyState title="No orphan report yet">
          Set <code className="font-mono">ORPHAN_SCAN_INTERVAL</code> (e.g. <code className="font-mono">24h</code>) or
          call <code className="font-mono">POST /api/v1/orphans/scan</code> with the API token.
        </EmptyState>
      )}
      {error !== undefined && !noReport && <ErrorState error={error} />}
      {data === undefined && error === undefined && <TableSkeleton />}

      {data && (
        <>
          {data.server_check === "error" && (
            <Alert variant="warning">
              <AlertTitle>The media server could not be checked</AlertTitle>
              <AlertDescription>Server items were not compared with the files; see the errors below.</AlertDescription>
            </Alert>
          )}
          {data.server_check === "disabled" && info && !info.sync_enabled && (
            <p className="text-sm text-muted-foreground">
              No media server is configured: only folders and downloads are checked.
            </p>
          )}

          <div className="grid gap-4 sm:grid-cols-3">
            {(Object.keys(types) as Orphan["type"][]).map((t) => {
              const list = orphans.filter((o) => o.type === t)
              const bytes = list.reduce((n, o) => n + (o.size ?? 0), 0)
              return (
                <Card key={t} className="gap-2 py-5">
                  <CardHeader className="px-5">
                    <CardDescription>{types[t].label}</CardDescription>
                    <CardTitle className="text-2xl tabular-nums">
                      {list.length}
                      {t === "unlinked_download" && bytes > 0 && (
                        <span className="ml-2 text-sm font-normal text-muted-foreground">{formatBytes(bytes)}</span>
                      )}
                    </CardTitle>
                  </CardHeader>
                </Card>
              )
            })}
          </div>

          <div className="flex flex-wrap items-center gap-2">
            <Tabs value={filter} onValueChange={(v) => setFilter(v as Filter)}>
              <TabsList>
                <TabsTrigger value="all">All</TabsTrigger>
                <TabsTrigger value="server_item_missing_file">Server items</TabsTrigger>
                <TabsTrigger value="orphan_directory">Folders</TabsTrigger>
                <TabsTrigger value="unlinked_download">Downloads</TabsTrigger>
              </TabsList>
            </Tabs>
            <div className="relative w-full sm:w-72">
              <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
              <Input
                placeholder="Search paths and titles"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                className="pl-8"
                aria-label="Search"
              />
            </div>
          </div>
          {filter !== "all" && <p className="text-sm text-muted-foreground">{types[filter].help}</p>}

          {rows.length === 0 ? (
            <EmptyState title={orphans.length === 0 ? "No orphans" : "No match"} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Type</TableHead>
                  <TableHead>Path</TableHead>
                  <TableHead>Title</TableHead>
                  <TableHead className="text-right">Size</TableHead>
                  <TableHead>Detected</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((o) => (
                  <TableRow key={o.id}>
                    <TableCell>
                      <Badge variant={o.type === "unlinked_download" ? "warning" : "secondary"}>
                        {types[o.type].label}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <Truncated text={o.path} className="font-mono text-xs" />
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      {o.title ? <Truncated text={o.title} className="max-w-[14rem]" /> : "—"}
                    </TableCell>
                    <TableCell className="text-right tabular-nums text-muted-foreground">
                      {o.size ? formatBytes(o.size) : "—"}
                    </TableCell>
                    <TableCell className="text-muted-foreground">
                      <TimeAgo time={o.detected_at} />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}

          {data.errors && data.errors.length > 0 && (
            <div className="flex flex-col gap-2">
              <h2 className="text-lg font-semibold">Scan errors</h2>
              <ul className="flex flex-col gap-1 text-sm">
                {data.errors.map((e, i) => (
                  <li key={i} className="rounded-md border px-3 py-2">
                    {(e.title || e.server_item_id) && <span className="font-medium">{e.title || e.server_item_id}: </span>}
                    <span className="text-muted-foreground">{e.error}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </>
      )}
    </div>
  )
}
