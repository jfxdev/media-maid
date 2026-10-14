import { useCallback, useEffect, useState } from "react"

import { useAuth } from "@/auth/auth"
import { ApiError, getJSON } from "./client"

export interface ApiState<T> {
  data?: T
  error?: unknown
  loading: boolean
  reload: () => void
}

/**
 * useApi GETs path (null: nothing) and refreshes it every interval ms while
 * the tab is visible, and when it becomes visible again. A 401 signs out.
 */
export function useApi<T>(path: string | null, interval = 30_000): ApiState<T> {
  const { token, logout } = useAuth()
  const [state, setState] = useState<{ path: string | null; data?: T; error?: unknown; loading: boolean }>({
    path,
    loading: path !== null,
  })
  const [tick, setTick] = useState(0)
  const reload = useCallback(() => setTick((t) => t + 1), [])

  useEffect(() => {
    if (path === null) return
    const ctrl = new AbortController()
    // A new path starts empty; a refresh keeps showing the previous data.
    setState((s) => (s.path === path ? { ...s, loading: true } : { path, loading: true }))
    getJSON<T>(path, token, ctrl.signal).then(
      (data) => setState({ path, data, loading: false }),
      (error: unknown) => {
        if (ctrl.signal.aborted) return
        if (error instanceof ApiError && error.status === 401) logout()
        setState((s) => ({ path, data: s.path === path ? s.data : undefined, error, loading: false }))
      },
    )
    return () => ctrl.abort()
  }, [path, token, tick, logout])

  useEffect(() => {
    if (path === null || interval <= 0) return
    const refresh = () => {
      if (document.visibilityState === "visible") reload()
    }
    const id = window.setInterval(refresh, interval)
    document.addEventListener("visibilitychange", refresh)
    return () => {
      window.clearInterval(id)
      document.removeEventListener("visibilitychange", refresh)
    }
  }, [path, interval, reload])

  if (state.path !== path) return { loading: path !== null, reload }
  return { data: state.data, error: state.error, loading: state.loading, reload }
}
