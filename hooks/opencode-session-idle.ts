// session-finder managed plugin: session.idle -> incremental extraction
import { spawn } from "node:child_process"

const extractionCommand = 'command -v session-finder >/dev/null 2>&1 || exit 0; lock_dir="${TMPDIR:-/tmp}/session-finder-extract.lock"; mkdir "$lock_dir" 2>/dev/null || exit 0; (session-finder skill extract --pending >/dev/null 2>&1; rmdir "$lock_dir" 2>/dev/null || true) &'

export const SessionFinderPlugin = async () => ({
  event: async ({ event }: { event?: { type?: string } }) => {
    if (event?.type !== "session.idle") return
    try {
      const child = spawn("sh", ["-c", extractionCommand], {
        detached: true,
        stdio: "ignore",
      })
      child.on("error", () => {})
      child.unref()
    } catch {
      // Hooks are observational and fail open when the binary is unavailable.
    }
  },
})

export default SessionFinderPlugin
