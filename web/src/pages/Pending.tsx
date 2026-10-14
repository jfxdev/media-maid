import { useMemo, useState } from "react"
import { Search } from "lucide-react"

import type { PendingItem, PendingResponse } from "@/api/types"
import { useApi } from "@/api/useApi"
import {
  CopyBlock,
  EmptyState,
  ErrorState,
  PageHeader,
  RefreshButton,
  StatusBadge,
  TableSkeleton,
  TimeAgo,
  Truncated,
} from "@/components/common"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { commandsFor } from "@/lib/commands"
import { formatDateTime, humanize } from "@/lib/format"
import { href, navigate } from "@/lib/router"

export const pendingTabs = {
  all: { label: "All", statuses: ["conflict", "duplicate", "skipped", "error"] },
  conflicts: { label: "Conflicts", statuses: ["conflict", "duplicate"] },
  unidentified: { label: "Unidentified", statuses: ["skipped"] },
  errors: { label: "Errors", statuses: ["error"] },
} as const

export type PendingTab = keyof typeof pendingTabs

function isTab(s: string | undefined): s is PendingTab {
  return s !== undefined && s in pendingTabs
}

/** filterPending applies the tab, the reason filter and the text search. */
export function filterPending(items: PendingItem[], tab: PendingTab, reason: string | null, query: string): PendingItem[] {
  const statuses: readonly string[] = pendingTabs[tab].statuses
  const q = query.trim().toLowerCase()
  return items.filter(
    (i) =>
      statuses.includes(i.status) &&
      (reason === null || (i.reason ?? "") === reason) &&
      (q === "" ||
        i.source.toLowerCase().includes(q) ||
        (i.dest ?? "").toLowerCase().includes(q) ||
        (i.error ?? "").toLowerCase().includes(q)),
  )
}

function Detail({ item, onClose }: { item: PendingItem | null; onClose: () => void }) {
  const origin = window.location.origin
  return (
    <Sheet open={item !== null} onOpenChange={(open) => !open && onClose()}>
      <SheetContent className="overflow-y-auto">
        {item && (
          <>
            <SheetHeader>
              <SheetTitle className="pr-6 break-all">{item.source}</SheetTitle>
              <SheetDescription>
                Pending since {formatDateTime(item.since)}; last change {formatDateTime(item.updated)}.
              </SheetDescription>
            </SheetHeader>
            <div className="flex flex-col gap-5 px-4 pb-6">
              <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
                <dt className="text-muted-foreground">Status</dt>
                <dd>
                  <StatusBadge status={item.status} />
                </dd>
                {item.reason && (
                  <>
                    <dt className="text-muted-foreground">Reason</dt>
                    <dd>{humanize(item.reason)}</dd>
                  </>
                )}
                {item.kind && (
                  <>
                    <dt className="text-muted-foreground">Kind</dt>
                    <dd>{item.kind}</dd>
                  </>
                )}
                {item.dest && (
                  <>
                    <dt className="text-muted-foreground">Destination</dt>
                    <dd className="break-all font-mono text-xs">{item.dest}</dd>
                  </>
                )}
              </dl>
              {item.error && (
                <div>
                  <h3 className="mb-1 text-sm font-medium">Message</h3>
                  <p className="rounded-md border bg-muted/40 p-3 text-sm break-words whitespace-pre-wrap">{item.error}</p>
                </div>
              )}
              <div className="flex flex-col gap-3">
                <div>
                  <h3 className="text-sm font-medium">Resolve it through the API</h3>
                  <p className="text-xs text-muted-foreground">
                    This page is read-only. Run these with <code className="font-mono">API_TOKEN</code> set in your shell;
                    the UI never sees that token.
                  </p>
                </div>
                {commandsFor(item, origin).map((c) => (
                  <div key={c.label} className="flex flex-col gap-1.5">
                    <div className="text-sm font-medium">{c.label}</div>
                    <p className="text-xs text-muted-foreground">{c.description}</p>
                    <CopyBlock text={c.command} label={c.label} />
                  </div>
                ))}
              </div>
            </div>
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

export function Pending({ tab: tabParam }: { tab?: string }) {
  const tab: PendingTab = isTab(tabParam) ? tabParam : "all"
  const { data, error, loading, reload } = useApi<PendingResponse>("/api/v1/pending")
  const [query, setQuery] = useState("")
  const [reason, setReason] = useState<string | null>(null)
  const [newestFirst, setNewestFirst] = useState(true)
  const [selected, setSelected] = useState<PendingItem | null>(null)

  const items = useMemo(() => data?.items ?? [], [data])
  const inTab = useMemo(() => filterPending(items, tab, null, ""), [items, tab])
  const reasons = useMemo(() => [...new Set(inTab.map((i) => i.reason ?? ""))].sort(), [inTab])
  const rows = useMemo(() => {
    const out = filterPending(items, tab, reason, query)
    out.sort((a, b) => (newestFirst ? b.updated.localeCompare(a.updated) : a.updated.localeCompare(b.updated)))
    return out
  }, [items, tab, reason, query, newestFirst])

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Pending"
        description="Downloads that are not in the library and need a decision. They leave this list once placed or deleted."
        actions={<RefreshButton onClick={reload} loading={loading} />}
      />
      <Tabs
        value={tab}
        onValueChange={(v) => {
          setReason(null)
          navigate(href("pending", v === "all" ? "" : v))
        }}
      >
        <TabsList>
          {(Object.keys(pendingTabs) as PendingTab[]).map((t) => (
            <TabsTrigger key={t} value={t}>
              {pendingTabs[t].label}
              {data && (
                <Badge variant="secondary" className="ml-1 px-1.5 tabular-nums">
                  {filterPending(items, t, null, "").length}
                </Badge>
              )}
            </TabsTrigger>
          ))}
        </TabsList>
      </Tabs>

      <div className="flex flex-wrap items-center gap-2">
        <div className="relative w-full sm:w-80">
          <Search className="pointer-events-none absolute top-2.5 left-2.5 size-4 text-muted-foreground" />
          <Input
            placeholder="Search names and messages"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="pl-8"
            aria-label="Search"
          />
        </div>
        {reasons.length > 1 && (
          <div className="flex flex-wrap gap-1.5" role="group" aria-label="Filter by reason">
            <Button size="sm" variant={reason === null ? "secondary" : "ghost"} onClick={() => setReason(null)}>
              Any reason
            </Button>
            {reasons.map((r) => (
              <Button key={r || "none"} size="sm" variant={reason === r ? "secondary" : "ghost"} onClick={() => setReason(r)}>
                {r ? humanize(r) : "no reason"}
              </Button>
            ))}
          </div>
        )}
        <Button size="sm" variant="ghost" className="ml-auto" onClick={() => setNewestFirst((v) => !v)}>
          {newestFirst ? "Newest first" : "Oldest first"}
        </Button>
      </div>

      {error !== undefined && <ErrorState error={error} />}
      {data === undefined && error === undefined && <TableSkeleton />}
      {data && rows.length === 0 && (
        <EmptyState title={items.length === 0 ? "Nothing pending" : "No match"}>
          {items.length === 0 ? "Every download Media Maid saw is in the library or left alone." : "Change the filters."}
        </EmptyState>
      )}
      {rows.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Download</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Reason</TableHead>
              <TableHead>Kind</TableHead>
              <TableHead>Updated</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((item) => (
              <TableRow key={item.source} className="cursor-pointer" onClick={() => setSelected(item)}>
                <TableCell>
                  <button
                    type="button"
                    className="text-left outline-none focus-visible:underline"
                    onClick={(e) => {
                      e.stopPropagation()
                      setSelected(item)
                    }}
                  >
                    <Truncated text={item.source} className="font-medium" />
                    {item.error && <Truncated text={item.error} className="text-xs text-muted-foreground" />}
                  </button>
                </TableCell>
                <TableCell>
                  <StatusBadge status={item.status} />
                </TableCell>
                <TableCell className="text-muted-foreground">{humanize(item.reason) || "—"}</TableCell>
                <TableCell className="text-muted-foreground">{item.kind ?? "—"}</TableCell>
                <TableCell className="text-muted-foreground">
                  <TimeAgo time={item.updated} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      <Detail item={selected} onClose={() => setSelected(null)} />
    </div>
  )
}
