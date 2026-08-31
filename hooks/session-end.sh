#!/bin/sh
# session-finder managed hook: queue new sessions without delaying the host.
# Hook stdin is intentionally ignored; host lifecycle payloads are not needed.

command -v session-finder >/dev/null 2>&1 || exit 0

# Run in a detached child so the host can exit immediately. The lock lives in
# that child, not in this short-lived wrapper, and prevents overlapping scans.
nohup sh -c '
  lock_dir="${TMPDIR:-/tmp}/session-finder-extract.lock"
  mkdir "$lock_dir" 2>/dev/null || exit 0
  trap '\''rmdir "$lock_dir" 2>/dev/null || true'\'' EXIT HUP INT TERM
  session-finder skill extract --pending >/dev/null 2>&1
' >/dev/null 2>&1 </dev/null &
exit 0
