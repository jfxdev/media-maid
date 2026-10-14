import { useState, type ReactNode } from "react"
import { AlertCircle, Check, Copy, Inbox, RefreshCw } from "lucide-react"

import { errorMessage } from "@/api/client"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { formatDateTime, formatRelative, humanize } from "@/lib/format"
import { cn } from "@/lib/utils"

type BadgeVariant = "default" | "secondary" | "destructive" | "outline" | "warning" | "success" | "info"

const statusVariant: Record<string, BadgeVariant> = {
  conflict: "warning",
  duplicate: "secondary",
  skipped: "info",
  error: "destructive",
  linked: "success",
  copied: "success",
  replaced: "success",
  upgraded: "success",
  exists: "secondary",
  removed: "success",
  ok: "success",
  stale: "secondary",
  planned: "outline",
  dry_run: "outline",
  disabled: "outline",
}

/** StatusBadge colors a history, pending or removal status. */
export function StatusBadge({ status }: { status: string }) {
  return <Badge variant={statusVariant[status] ?? "outline"}>{humanize(status)}</Badge>
}

/** TimeAgo shows a relative time with the full date on hover. */
export function TimeAgo({ time }: { time: string | undefined | null }) {
  return (
    <time dateTime={time ?? undefined} title={formatDateTime(time)} className="tabular-nums">
      {formatRelative(time)}
    </time>
  )
}

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: string
  description?: ReactNode
  actions?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {description && <p className="mt-1 text-sm text-muted-foreground">{description}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  )
}

export function RefreshButton({ onClick, loading }: { onClick: () => void; loading?: boolean }) {
  return (
    <Button variant="outline" size="sm" onClick={onClick} disabled={loading} aria-label="Refresh">
      <RefreshCw className={cn(loading && "animate-spin")} />
      Refresh
    </Button>
  )
}

export function ErrorState({ error, title = "Could not load" }: { error: unknown; title?: string }) {
  return (
    <Alert variant="destructive">
      <AlertCircle />
      <AlertTitle>{title}</AlertTitle>
      <AlertDescription>{errorMessage(error)}</AlertDescription>
    </Alert>
  )
}

export function EmptyState({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center gap-2 rounded-lg border border-dashed px-6 py-10 text-center">
      <Inbox className="size-8 text-muted-foreground" aria-hidden="true" />
      <p className="font-medium">{title}</p>
      {children && <div className="max-w-md text-sm text-muted-foreground">{children}</div>}
    </div>
  )
}

export function TableSkeleton({ rows = 5 }: { rows?: number }) {
  return (
    <div className="flex flex-col gap-2" aria-busy="true" aria-label="Loading">
      {Array.from({ length: rows }, (_, i) => (
        <Skeleton key={i} className="h-9 w-full" />
      ))}
    </div>
  )
}

/**
 * CopyBlock shows a command to copy. The clipboard API needs a secure
 * context (HTTPS or localhost); elsewhere the text is selected instead.
 */
export function CopyBlock({ text, label }: { text: string; label: string }) {
  const [copied, setCopied] = useState<"yes" | "select" | null>(null)
  const id = `copy-${label.replace(/\W+/g, "-")}`

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied("yes")
    } catch {
      const el = document.getElementById(id)
      const sel = window.getSelection()
      if (el && sel) {
        const range = document.createRange()
        range.selectNodeContents(el)
        sel.removeAllRanges()
        sel.addRange(range)
      }
      setCopied("select")
    }
    window.setTimeout(() => setCopied(null), 2500)
  }

  return (
    <div className="relative rounded-md border bg-muted/50">
      <pre id={id} className="overflow-x-auto p-3 pr-12 font-mono text-xs leading-relaxed whitespace-pre">
        {text}
      </pre>
      <Button
        variant="ghost"
        size="icon"
        className="absolute top-1 right-1 size-8"
        onClick={copy}
        aria-label={`Copy: ${label}`}
        title={copied === "select" ? "Selected: press Ctrl+C" : "Copy"}
      >
        {copied === "yes" ? <Check /> : <Copy />}
      </Button>
    </div>
  )
}

/** Truncated shows a long path on one line, in full on hover. */
export function Truncated({ text, className }: { text: string; className?: string }) {
  return (
    <span className={cn("block max-w-[28rem] truncate", className)} title={text}>
      {text}
    </span>
  )
}
