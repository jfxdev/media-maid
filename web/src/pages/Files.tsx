import { useCallback, useEffect, useState } from "react"
import { ArrowUp, File, FileVideo, Folder, Link2, Loader2, Subtitles, TriangleAlert } from "lucide-react"

import { ApiError, getJSON } from "@/api/client"
import type { FileEntry, Listing, RootName } from "@/api/types"
import { useAuth } from "@/auth/auth"
import { EmptyState, ErrorState, PageHeader, RefreshButton, TableSkeleton, TimeAgo } from "@/components/common"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Badge } from "@/components/ui/badge"
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from "@/components/ui/breadcrumb"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { formatBytes } from "@/lib/format"
import { href, navigate } from "@/lib/router"
import { readStorage, writeStorage } from "@/lib/storage"

const PAGE = 200
const HIDDEN_KEY = "media-maid.show-hidden"

const rootLabels: Record<RootName, string> = { downloads: "Downloads", movies: "Movies", tv: "TV" }

/** filesQuery builds the listing URL for one page of a folder. */
export function filesQuery(root: string, path: string, offset: number, limit = PAGE): string {
  const q = new URLSearchParams({ root, offset: String(offset), limit: String(limit) })
  if (path) q.set("path", path)
  return `/api/v1/files?${q}`
}

function EntryIcon({ entry }: { entry: FileEntry }) {
  const cls = "size-4 shrink-0 text-muted-foreground"
  if (entry.type === "dir") return <Folder className="size-4 shrink-0 text-sky-600 dark:text-sky-400" />
  if (entry.type === "symlink") return <Link2 className={cls} />
  if (entry.media === "video") return <FileVideo className={cls} />
  if (entry.media === "subtitle") return <Subtitles className={cls} />
  return <File className={cls} />
}

function useShowHidden(): [boolean, (v: boolean) => void] {
  const [show, setShow] = useState(() => readStorage("local", HIDDEN_KEY) !== "false")
  const set = useCallback((v: boolean) => {
    setShow(v)
    writeStorage("local", HIDDEN_KEY, v ? "true" : "false")
  }, [])
  return [show, set]
}

export function Files({ root: rootParam, path }: { root?: string; path: string }) {
  const { token, info, logout } = useAuth()
  const roots = (info?.roots?.length ? info.roots : ["downloads", "movies", "tv"]) as RootName[]
  const root = roots.includes(rootParam as RootName) ? (rootParam as RootName) : roots[0]
  const [showHidden, setShowHidden] = useShowHidden()
  const [listing, setListing] = useState<Listing | null>(null)
  const [error, setError] = useState<unknown>(undefined)
  const [loading, setLoading] = useState(false)
  const [tick, setTick] = useState(0)

  // First page of the folder; reset whenever the folder changes.
  useEffect(() => {
    if (!root) return
    const ctrl = new AbortController()
    setLoading(true)
    setError(undefined)
    setListing(null)
    getJSON<Listing>(filesQuery(root, path, 0), token, ctrl.signal).then(
      (l) => {
        setListing(l)
        setLoading(false)
      },
      (err: unknown) => {
        if (ctrl.signal.aborted) return
        if (err instanceof ApiError && err.status === 401) logout()
        setError(err)
        setLoading(false)
      },
    )
    return () => ctrl.abort()
  }, [root, path, token, logout, tick])

  async function loadMore() {
    if (!listing) return
    setLoading(true)
    try {
      const next = await getJSON<Listing>(filesQuery(root, path, listing.entries.length), token)
      setListing({ ...next, entries: [...listing.entries, ...next.entries] })
    } catch (err) {
      setError(err)
    } finally {
      setLoading(false)
    }
  }

  const segments = path ? path.split("/") : []
  const entries = listing?.entries.filter((e) => showHidden || !e.hidden) ?? []
  const hiddenCount = listing ? listing.entries.length - listing.entries.filter((e) => !e.hidden).length : 0
  const more = listing ? listing.entries.length < listing.total : false

  return (
    <div className="flex flex-col gap-5">
      <PageHeader
        title="Files"
        description="Names and sizes only: files cannot be opened or downloaded here."
        actions={<RefreshButton onClick={() => setTick((t) => t + 1)} loading={loading} />}
      />

      <div className="flex flex-wrap items-center justify-between gap-3">
        <Tabs value={root} onValueChange={(v) => navigate(href("files", v))}>
          <TabsList>
            {roots.map((r) => (
              <TabsTrigger key={r} value={r}>
                {rootLabels[r] ?? r}
              </TabsTrigger>
            ))}
          </TabsList>
        </Tabs>
        <div className="flex items-center gap-2">
          <Switch id="show-hidden" checked={showHidden} onCheckedChange={setShowHidden} />
          <Label htmlFor="show-hidden">Show hidden{hiddenCount > 0 && ` (${hiddenCount})`}</Label>
        </div>
      </div>

      <Breadcrumb>
        <BreadcrumbList>
          <BreadcrumbItem>
            {segments.length === 0 ? (
              <BreadcrumbPage>{rootLabels[root] ?? root}</BreadcrumbPage>
            ) : (
              <BreadcrumbLink href={href("files", root)}>{rootLabels[root] ?? root}</BreadcrumbLink>
            )}
          </BreadcrumbItem>
          {segments.map((s, i) => (
            <span key={i} className="contents">
              <BreadcrumbSeparator />
              <BreadcrumbItem>
                {i === segments.length - 1 ? (
                  <BreadcrumbPage className="break-all">{s}</BreadcrumbPage>
                ) : (
                  <BreadcrumbLink className="break-all" href={href("files", root, ...segments.slice(0, i + 1))}>
                    {s}
                  </BreadcrumbLink>
                )}
              </BreadcrumbItem>
            </span>
          ))}
        </BreadcrumbList>
      </Breadcrumb>

      {error !== undefined && <ErrorState error={error} />}
      {listing === null && error === undefined && <TableSkeleton />}
      {listing?.truncated && (
        <Alert variant="warning">
          <TriangleAlert />
          <AlertDescription>
            This folder holds more than {listing.total.toLocaleString()} entries; only the first ones read are listed.
          </AlertDescription>
        </Alert>
      )}
      {listing && entries.length === 0 && segments.length === 0 && (
        <EmptyState title={listing.entries.length === 0 ? "Empty folder" : "Only hidden entries"} />
      )}
      {listing && (entries.length > 0 || segments.length > 0) && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead className="text-right">Size</TableHead>
              <TableHead>Modified</TableHead>
              <TableHead className="text-right" title="Hard links: 1 means no other copy of the file">
                Links
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {segments.length > 0 && (
              <TableRow>
                <TableCell colSpan={4}>
                  <a
                    href={href("files", root, ...segments.slice(0, -1))}
                    className="flex items-center gap-2 text-muted-foreground hover:text-foreground"
                  >
                    <ArrowUp className="size-4" /> Parent folder
                  </a>
                </TableCell>
              </TableRow>
            )}
            {entries.map((e) => (
              <TableRow key={e.name} data-type={e.type}>
                <TableCell className="max-w-[36rem]">
                  <div className="flex min-w-0 items-center gap-2">
                    <EntryIcon entry={e} />
                    {e.type === "dir" ? (
                      <a
                        href={href("files", root, ...segments, e.name)}
                        className="truncate font-medium hover:underline"
                        title={e.name}
                      >
                        {e.name}
                      </a>
                    ) : (
                      // Files are not links: nothing can be opened or downloaded.
                      <span className="truncate" title={e.name}>
                        {e.name}
                      </span>
                    )}
                    {e.hidden && <Badge variant="outline">hidden</Badge>}
                    {e.type === "symlink" && <Badge variant="outline">symlink</Badge>}
                    {e.type === "other" && <Badge variant="outline">special</Badge>}
                  </div>
                </TableCell>
                <TableCell className="text-right tabular-nums text-muted-foreground">
                  {e.type === "file" ? formatBytes(e.size ?? 0) : ""}
                </TableCell>
                <TableCell className="text-muted-foreground">
                  <TimeAgo time={e.modified} />
                </TableCell>
                <TableCell className="text-right tabular-nums text-muted-foreground">
                  {e.type === "file" ? (e.links ?? "—") : ""}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
      {more && (
        <div className="flex items-center justify-center gap-3">
          <Button variant="outline" onClick={loadMore} disabled={loading}>
            {loading && <Loader2 className="animate-spin" />}
            Load more
          </Button>
          <span className="text-sm text-muted-foreground">
            {listing!.entries.length.toLocaleString()} of {listing!.total.toLocaleString()}
          </span>
        </div>
      )}
    </div>
  )
}
