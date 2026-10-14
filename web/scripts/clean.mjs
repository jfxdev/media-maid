// Removes the previous build from internal/ui/dist, keeping the versioned
// placeholder (.keep) that lets go build work without Node.
import { rmSync } from "node:fs"
import { fileURLToPath } from "node:url"

const dist = fileURLToPath(new URL("../../internal/ui/dist/", import.meta.url))
for (const name of ["index.html", "assets", "favicon.svg"]) {
  rmSync(dist + name, { recursive: true, force: true })
}
