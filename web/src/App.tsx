import { LogOut } from "lucide-react"
import type { ReactNode } from "react"

import { Login } from "@/auth/Login"
import { useAuth } from "@/auth/auth"
import { Logo } from "@/components/logo"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Skeleton } from "@/components/ui/skeleton"
import { href, useRoute } from "@/lib/router"
import { cn } from "@/lib/utils"
import { Errors } from "@/pages/Errors"
import { Files } from "@/pages/Files"
import { Orphans } from "@/pages/Orphans"
import { Overview } from "@/pages/Overview"
import { Pending } from "@/pages/Pending"

const nav = [
  { id: "", label: "Overview" },
  { id: "pending", label: "Pending" },
  { id: "errors", label: "Errors" },
  { id: "orphans", label: "Orphans" },
  { id: "files", label: "Files" },
]

function Page({ route }: { route: string[] }) {
  const [page, ...rest] = route
  switch (page) {
    case undefined:
      return <Overview />
    case "pending":
      return <Pending tab={rest[0]} />
    case "errors":
      return <Errors />
    case "orphans":
      return <Orphans />
    case "files":
      return <Files root={rest[0]} path={rest.slice(1).join("/")} />
  }
  return (
    <p className="text-muted-foreground">
      Unknown page. <a className="underline" href={href()}>Back to the overview</a>.
    </p>
  )
}

function Shell({ children, current }: { children: ReactNode; current: string }) {
  const { info, logout } = useAuth()
  return (
    <div className="flex min-h-svh flex-col">
      <header className="sticky top-0 z-40 border-b bg-background/90 backdrop-blur supports-[backdrop-filter]:bg-background/70">
        <div className="mx-auto flex max-w-7xl flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3">
          <a href={href()} className="flex items-center gap-2 font-semibold">
            <Logo className="size-5" />
            Media Maid
          </a>
          <nav className="order-last flex w-full flex-wrap gap-1 sm:order-none sm:w-auto" aria-label="Main">
            {nav.map((n) => (
              <a
                key={n.id}
                href={href(n.id)}
                aria-current={current === n.id ? "page" : undefined}
                className={cn(
                  "rounded-md px-2.5 py-1.5 text-sm font-medium whitespace-nowrap text-muted-foreground transition-colors hover:text-foreground sm:px-3",
                  current === n.id && "bg-secondary text-foreground",
                )}
              >
                {n.label}
              </a>
            ))}
          </nav>
          <div className="ml-auto flex items-center gap-2">
            {info?.dry_run && <Badge variant="warning">dry run</Badge>}
            {info && (
              <span className="hidden text-xs text-muted-foreground md:inline">
                {info.mode} · {info.version}
              </span>
            )}
            <Badge variant="outline" title="This UI can list, never change, anything">
              read-only
            </Badge>
            <Button variant="ghost" size="sm" onClick={logout} aria-label="Sign out">
              <LogOut />
              <span className="hidden sm:inline">Sign out</span>
            </Button>
          </div>
        </div>
      </header>
      <main className="mx-auto w-full max-w-7xl flex-1 px-4 py-6">{children}</main>
    </div>
  )
}

export function App() {
  const { token, checking } = useAuth()
  const route = useRoute()
  if (checking) {
    return (
      <div className="mx-auto flex max-w-7xl flex-col gap-4 p-6" aria-busy="true">
        <Skeleton className="h-8 w-48" />
        <Skeleton className="h-32 w-full" />
      </div>
    )
  }
  if (!token) return <Login />
  return (
    <Shell current={route[0] ?? ""}>
      <Page route={route} />
    </Shell>
  )
}
