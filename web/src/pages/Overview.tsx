import { useEffect, useState, type ReactNode } from "react"
import { AlertTriangle, CircleHelp, Files, Ghost, XCircle } from "lucide-react"

import { getHealth } from "@/api/client"
import type { Health, HistoryResponse, PendingResponse, Report } from "@/api/types"
import { useApi } from "@/api/useApi"
import { useAuth } from "@/auth/auth"
import { EmptyState, ErrorState, PageHeader, RefreshButton, StatusBadge, TableSkeleton, TimeAgo, Truncated } from "@/components/common"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Skeleton } from "@/components/ui/skeleton"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { formatBytes, humanize } from "@/lib/format"
import { href } from "@/lib/router"

function StatCard({
  title,
  value,
  hint,
  icon,
  to,
  tone,
}: {
  title: string
  value: number | undefined
  hint: ReactNode
  icon: ReactNode
  to: string
  tone: "warning" | "info" | "destructive" | "neutral"
}) {
  const toneClass = {
    warning: "text-amber-600 dark:text-amber-400",
    info: "text-sky-600 dark:text-sky-400",
    destructive: "text-destructive",
    neutral: "text-muted-foreground",
  }[tone]
  return (
    <a
      href={to}
      className="rounded-xl outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
      aria-label={`${title}: ${value ?? "loading"}`}
    >
      <Card className="h-full gap-3 py-5 transition-colors hover:bg-accent/40">
        <CardHeader className="px-5">
          <CardDescription className="flex items-center gap-2">
            <span className={toneClass}>{icon}</span>
            {title}
          </CardDescription>
          <CardTitle className="text-3xl tabular-nums">
            {value === undefined ? <Skeleton className="h-8 w-12" /> : value}
          </CardTitle>
        </CardHeader>
        <CardContent className="px-5 text-xs text-muted-foreground">{hint}</CardContent>
      </Card>
    </a>
  )
}

function useHealth() {
  const [health, setHealth] = useState<Health | null>(null)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    let ctrl = new AbortController()
    const load = () => {
      ctrl.abort()
      ctrl = new AbortController()
      getHealth(ctrl.signal).then(
        (h) => {
          setHealth(h)
          setFailed(false)
        },
        () => {
          if (!ctrl.signal.aborted) setFailed(true)
        },
      )
    }
    load()
    const id = window.setInterval(() => document.visibilityState === "visible" && load(), 30_000)
    return () => {
      window.clearInterval(id)
      ctrl.abort()
    }
  }, [])
  return { health, failed }
}

function Row({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-4 py-2 text-sm">
      <dt className="text-muted-foreground">{label}</dt>
      <dd className="text-right">{children}</dd>
    </div>
  )
}

export function Overview() {
  const { info } = useAuth()
  const pending = useApi<PendingResponse>("/api/v1/pending")
  const history = useApi<HistoryResponse>("/api/v1/history")
  const orphans = useApi<Report>("/api/v1/orphans", 60_000)
  const { health, failed } = useHealth()

  const items = pending.data?.items
  const count = (...statuses: string[]) => items?.filter((i) => statuses.includes(i.status)).length
  const report = orphans.data
  const noReport = orphans.error !== undefined && (orphans.error as { code?: string }).code === "no_report"
  const unlinkedBytes = report?.orphans
    .filter((o) => o.type === "unlinked_download")
    .reduce((n, o) => n + (o.size ?? 0), 0)
  const lastSync = history.data?.last_sync
  const recent = history.data?.entries.slice(0, 10)

  let watcher: ReactNode = <Skeleton className="h-5 w-20" />
  if (failed) watcher = <Badge variant="destructive">unreachable</Badge>
  else if (health?.status === "unhealthy") watcher = <Badge variant="destructive">stopped</Badge>
  else if (info?.organize_trigger === "hook") watcher = <Badge variant="outline">off (hook mode)</Badge>
  else if (health?.watcher === "degraded") watcher = <Badge variant="warning">degraded</Badge>
  else if (health) watcher = <Badge variant="success">running</Badge>

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Overview"
        description="What needs a decision and how Media Maid is doing. Refreshes every 30 seconds."
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

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          title="Conflicts"
          value={count("conflict", "duplicate")}
          hint="Conflicts and duplicates with the library"
          icon={<AlertTriangle className="size-4" />}
          to={href("pending", "conflicts")}
          tone="warning"
        />
        <StatCard
          title="Unidentified"
          value={count("skipped")}
          hint="Ambiguous or unknown titles, or TMDB unreachable"
          icon={<CircleHelp className="size-4" />}
          to={href("pending", "unidentified")}
          tone="info"
        />
        <StatCard
          title="Errors"
          value={items === undefined ? undefined : (count("error") ?? 0) + (lastSync?.status === "error" ? 1 : 0)}
          hint={lastSync?.status === "error" ? "Including the last library sync" : "Downloads that failed to organize"}
          icon={<XCircle className="size-4" />}
          to={href("errors")}
          tone="destructive"
        />
        <StatCard
          title="Orphans"
          value={noReport ? 0 : report?.orphans.length}
          hint={
            noReport
              ? "No report yet"
              : unlinkedBytes
                ? `${formatBytes(unlinkedBytes)} in downloads not in the library`
                : "Stale server items and empty folders"
          }
          icon={<Ghost className="size-4" />}
          to={href("orphans")}
          tone="neutral"
        />
      </div>

      <div className="grid grid-cols-1 gap-4 lg:grid-cols-3">
        <Card className="min-w-0 gap-2">
          <CardHeader>
            <CardTitle>Status</CardTitle>
          </CardHeader>
          <CardContent>
            <dl className="divide-y">
              <Row label="Watcher">{watcher}</Row>
              <Row label="Media server">
                {info?.sync_enabled ? <span className="capitalize">{info.mode}</span> : <Badge variant="outline">not configured</Badge>}
              </Row>
              <Row label="Last sync">
                {lastSync ? (
                  <span className="flex items-center gap-2">
                    <StatusBadge status={lastSync.status} />
                    <TimeAgo time={lastSync.time} />
                  </span>
                ) : (
                  <span className="text-muted-foreground">never</span>
                )}
              </Row>
              <Row label="Orphan report">
                {report ? <TimeAgo time={report.generated_at} /> : <span className="text-muted-foreground">never</span>}
              </Row>
              <Row label="Organize trigger">{info?.organize_trigger ?? "—"}</Row>
              <Row label="TMDB / LLM">
                {info ? `${info.tmdb_enabled ? "on" : "off"} / ${info.llm_enabled ? "on" : "off"}` : "—"}
              </Row>
            </dl>
          </CardContent>
        </Card>

        <Card className="min-w-0 gap-2 lg:col-span-2">
          <CardHeader>
            <CardTitle>Recent activity</CardTitle>
            <CardDescription>The last actions recorded in the history.</CardDescription>
          </CardHeader>
          <CardContent>
            {history.error !== undefined && <ErrorState error={history.error} />}
            {recent === undefined && history.error === undefined && <TableSkeleton rows={4} />}
            {recent?.length === 0 && <EmptyState title="Nothing yet" />}
            {recent && recent.length > 0 && (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>When</TableHead>
                    <TableHead>Action</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>Item</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {recent.map((e, i) => (
                    <TableRow key={`${e.time}-${i}`}>
                      <TableCell className="text-muted-foreground">
                        <TimeAgo time={e.time} />
                      </TableCell>
                      <TableCell>{humanize(e.action)}</TableCell>
                      <TableCell>
                        <StatusBadge status={e.status} />
                      </TableCell>
                      <TableCell>
                        <Truncated text={e.dest || e.source || e.error || ""} className="max-w-[22rem]" />
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
          </CardContent>
        </Card>
      </div>

      <p className="flex items-center gap-2 text-xs text-muted-foreground">
        <Files className="size-3.5" />
        <span>
          Browse the downloads and libraries under{" "}
          <a className="underline underline-offset-4" href={href("files")}>
            Files
          </a>
          .
        </span>
      </p>
    </div>
  )
}
