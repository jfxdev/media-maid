import { useMemo } from "react"

import type { HistoryResponse, PendingResponse, Report } from "@/api/types"
import { ApiError } from "@/api/client"
import { useApi } from "@/api/useApi"
import { EmptyState, ErrorState, PageHeader, RefreshButton, TableSkeleton, TimeAgo, Truncated } from "@/components/common"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { humanize } from "@/lib/format"
import { href } from "@/lib/router"

export interface ErrorRow {
  key: string
  time: string
  origin: string
  subject: string
  message: string
  link?: string
}

/** currentErrors gathers what is failing now: pending errors, the last sync and the orphan report. */
export function currentErrors(pending?: PendingResponse, history?: HistoryResponse, report?: Report): ErrorRow[] {
  const rows: ErrorRow[] = []
  for (const i of pending?.items ?? []) {
    if (i.status !== "error") continue
    rows.push({
      key: `pending:${i.source}`,
      time: i.updated,
      origin: i.reason === "archive" ? "Organize (archive)" : "Organize",
      subject: i.source,
      message: i.error ?? "",
      link: href("pending", "errors"),
    })
  }
  const sync = history?.last_sync
  if (sync?.status === "error") {
    rows.push({ key: "sync", time: sync.time, origin: "Library sync", subject: "Media server refresh", message: sync.error ?? "" })
  }
  for (const [n, e] of (report?.errors ?? []).entries()) {
    rows.push({
      key: `orphans:${n}`,
      time: report!.generated_at,
      origin: "Orphan scan",
      subject: e.title || e.server_item_id || "Scan",
      message: e.error,
      link: href("orphans"),
    })
  }
  return rows.sort((a, b) => b.time.localeCompare(a.time))
}

export function Errors() {
  const pending = useApi<PendingResponse>("/api/v1/pending")
  const history = useApi<HistoryResponse>("/api/v1/history")
  const orphans = useApi<Report>("/api/v1/orphans", 60_000)
  const noReport = orphans.error instanceof ApiError && orphans.error.code === "no_report"

  const now = useMemo(() => currentErrors(pending.data, history.data, orphans.data), [pending.data, history.data, orphans.data])
  const failures = useMemo(() => history.data?.entries.filter((e) => e.status === "error") ?? [], [history.data])
  const loading = pending.data === undefined || history.data === undefined

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Errors"
        description="What is failing now, and the failures recorded in the recent history."
        actions={
          <RefreshButton
            loading={pending.loading || history.loading}
            onClick={() => {
              pending.reload()
              history.reload()
              orphans.reload()
            }}
          />
        }
      />
      {pending.error !== undefined && <ErrorState error={pending.error} title="Could not load pending items" />}
      {history.error !== undefined && <ErrorState error={history.error} title="Could not load the history" />}
      {orphans.error !== undefined && !noReport && <ErrorState error={orphans.error} title="Could not load the orphan report" />}

      <Card className="gap-3">
        <CardHeader>
          <CardTitle>Failing now</CardTitle>
          <CardDescription>Downloads that failed to organize, the last library sync and the last orphan scan.</CardDescription>
        </CardHeader>
        <CardContent>
          {loading && pending.error === undefined && history.error === undefined && <TableSkeleton rows={3} />}
          {!loading && now.length === 0 && <EmptyState title="No current errors" />}
          {now.length > 0 && (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>When</TableHead>
                  <TableHead>Where</TableHead>
                  <TableHead>Item</TableHead>
                  <TableHead>Message</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {now.map((r) => (
                  <TableRow key={r.key}>
                    <TableCell className="text-muted-foreground">
                      <TimeAgo time={r.time} />
                    </TableCell>
                    <TableCell>
                      {r.link ? (
                        <a href={r.link} className="underline-offset-4 hover:underline">
                          <Badge variant="destructive">{r.origin}</Badge>
                        </a>
                      ) : (
                        <Badge variant="destructive">{r.origin}</Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <Truncated text={r.subject} className="max-w-[18rem] font-medium" />
                    </TableCell>
                    <TableCell className="whitespace-normal break-words text-muted-foreground">{r.message}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Card className="gap-3">
        <CardHeader>
          <CardTitle>Recent failures</CardTitle>
          <CardDescription>History entries with status error (organize, sync, orphan removal), newest first.</CardDescription>
        </CardHeader>
        <CardContent>
          {history.data === undefined && history.error === undefined && <TableSkeleton rows={3} />}
          {history.data && failures.length === 0 && <EmptyState title="No failures in the history" />}
          {failures.length > 0 && (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>When</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Item</TableHead>
                  <TableHead>Message</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {failures.map((e, i) => (
                  <TableRow key={`${e.time}-${i}`}>
                    <TableCell className="text-muted-foreground">
                      <TimeAgo time={e.time} />
                    </TableCell>
                    <TableCell>{humanize(e.action)}</TableCell>
                    <TableCell>
                      <Truncated text={e.source || e.dest || "—"} className="max-w-[18rem]" />
                    </TableCell>
                    <TableCell className="whitespace-normal break-words text-muted-foreground">{e.error ?? ""}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
