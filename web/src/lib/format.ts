const units = ["B", "KB", "MB", "GB", "TB", "PB"]

/** formatBytes renders a size with binary units: 1536 → "1.5 KB". */
export function formatBytes(n: number | undefined): string {
  if (n === undefined || !Number.isFinite(n) || n < 0) return "—"
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${i === 0 ? v : v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`
}

/** parseTime returns the time in ms, or null for a missing or zero Go time. */
export function parseTime(iso: string | undefined | null): number | null {
  if (!iso || iso.startsWith("0001-01-01")) return null
  const t = Date.parse(iso)
  return Number.isNaN(t) ? null : t
}

const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" })

/** formatRelative renders "5 minutes ago", "yesterday", or a date when old. */
export function formatRelative(iso: string | undefined | null, now = Date.now()): string {
  const t = parseTime(iso)
  if (t === null) return "—"
  const s = Math.round((t - now) / 1000)
  const abs = Math.abs(s)
  if (abs < 45) return "just now"
  if (abs < 3600) return rtf.format(Math.round(s / 60), "minute")
  if (abs < 86400) return rtf.format(Math.round(s / 3600), "hour")
  if (abs < 86400 * 30) return rtf.format(Math.round(s / 86400), "day")
  return new Date(t).toLocaleDateString()
}

/** formatDateTime renders a full local date and time. */
export function formatDateTime(iso: string | undefined | null): string {
  const t = parseTime(iso)
  return t === null ? "—" : new Date(t).toLocaleString()
}

/**
 * shellQuote wraps s in single quotes for a POSIX shell. Nothing inside
 * single quotes is expanded; a quote is closed, escaped and reopened.
 */
export function shellQuote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`
}

/** humanize turns a snake_case code into words: "ambiguous_title" → "ambiguous title". */
export function humanize(code: string | undefined): string {
  return code ? code.replace(/_/g, " ") : ""
}
