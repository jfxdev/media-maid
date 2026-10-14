import { useState, type FormEvent } from "react"
import { KeyRound, Loader2 } from "lucide-react"

import { errorMessage } from "@/api/client"
import { Alert, AlertDescription } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Logo } from "@/components/logo"
import { useAuth } from "./auth"

export function Login() {
  const { login } = useAuth()
  const [token, setToken] = useState("")
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    if (!token.trim()) return
    setBusy(true)
    setError(null)
    try {
      await login(token)
    } catch (err) {
      setError(errorMessage(err))
      setBusy(false)
    }
  }

  return (
    <main className="flex min-h-svh items-center justify-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <div className="mb-2 flex items-center gap-2 text-lg font-semibold">
            <Logo className="size-6" /> Media Maid
          </div>
          <CardTitle>Sign in</CardTitle>
          <CardDescription>
            Enter the read-only <code className="font-mono">UI_TOKEN</code>. It is kept in this tab only and can list,
            never change, anything.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="flex flex-col gap-4" noValidate>
            <div className="flex flex-col gap-2">
              <Label htmlFor="token">Token</Label>
              <Input
                id="token"
                type="password"
                autoComplete="off"
                autoFocus
                spellCheck={false}
                value={token}
                onChange={(e) => setToken(e.target.value)}
                aria-invalid={error !== null}
              />
            </div>
            {error && (
              <Alert variant="destructive">
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
            <Button type="submit" disabled={busy || !token.trim()}>
              {busy ? <Loader2 className="animate-spin" /> : <KeyRound />}
              Sign in
            </Button>
          </form>
        </CardContent>
      </Card>
    </main>
  )
}
