import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react"

import { ApiError, getJSON } from "@/api/client"
import type { Info } from "@/api/types"
import { readStorage, writeStorage } from "@/lib/storage"

// The read-only UI_TOKEN lives in sessionStorage: it is gone when the tab
// closes, and it is only ever sent to this origin's API.
const TOKEN_KEY = "media-maid.token"

interface AuthState {
  token: string | null
  info: Info | null
  /** checking: a saved token is being validated. */
  checking: boolean
  login: (token: string) => Promise<void>
  logout: () => void
}

const AuthContext = createContext<AuthState | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [token, setToken] = useState<string | null>(() => readStorage("session", TOKEN_KEY))
  const [info, setInfo] = useState<Info | null>(null)
  const [checking, setChecking] = useState(token !== null)

  const logout = useCallback(() => {
    writeStorage("session", TOKEN_KEY, null)
    setToken(null)
    setInfo(null)
  }, [])

  const login = useCallback(async (candidate: string) => {
    const t = candidate.trim()
    // Validate with a read-only call before keeping the token.
    const i = await getJSON<Info>("/api/v1/info", t)
    writeStorage("session", TOKEN_KEY, t)
    setToken(t)
    setInfo(i)
  }, [])

  // A token saved earlier in this tab is checked once on load.
  useEffect(() => {
    if (token === null || info !== null) {
      setChecking(false)
      return
    }
    const ctrl = new AbortController()
    getJSON<Info>("/api/v1/info", token, ctrl.signal).then(
      (i) => {
        setInfo(i)
        setChecking(false)
      },
      (err: unknown) => {
        if (ctrl.signal.aborted) return
        if (err instanceof ApiError && err.status === 401) logout()
        setChecking(false)
      },
    )
    return () => ctrl.abort()
  }, [token, info, logout])

  const value = useMemo(() => ({ token, info, checking, login, logout }), [token, info, checking, login, logout])
  return <AuthContext value={value}>{children}</AuthContext>
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error("useAuth outside AuthProvider")
  return ctx
}
