#!/bin/sh
# session-finder managed hook: queue new sessions without delaying the host.
# Hook stdin is intentionally ignored; host lifecycle payloads are not needed.

command -v session-finder >/dev/null 2>&1 || exit 0

# Run in a detached child so the host can exit immediately. The lock lives in
# that child, not in this short-lived wrapper, and stale owners are reclaimed
# only after their process is gone and the lock is at least ten minutes old.
nohup sh -c '
  command -v session-finder >/dev/null 2>&1 || exit 0
  umask 077
  lock_dir="${TMPDIR:-/tmp}/session-finder-extract.lock"
  lock_owner="$lock_dir/owner"
  lock_ttl=600
  now=$(date +%s 2>/dev/null) || exit 0
  reclaim_stale_lock() {
    stale_dir=$(find "$lock_dir" -prune -mmin +10 -print -quit 2>/dev/null) || return 1
    [ -n "$stale_dir" ] || return 1
    rm -f "$lock_dir"/owner "$lock_dir"/owner.* 2>/dev/null || return 1
    rmdir "$lock_dir" 2>/dev/null || return 1
    mkdir "$lock_dir" 2>/dev/null || return 1
  }
  while :; do
    if mkdir "$lock_dir" 2>/dev/null; then
      break
    fi
    if [ -r "$lock_owner" ]; then
      if ! {
        IFS= read -r holder_pid
        IFS= read -r holder_started
      } < "$lock_owner"; then
        reclaim_stale_lock || exit 0
        break
      fi
      case "$holder_pid" in
        *[!0-9]*|"")
          reclaim_stale_lock || exit 0
          break
          ;;
      esac
      [ "$holder_pid" -gt 0 ] 2>/dev/null || exit 0
      if kill -0 "$holder_pid" 2>/dev/null; then
        exit 0
      fi
      case "$holder_started" in
        *[!0-9]*|"")
          reclaim_stale_lock || exit 0
          break
          ;;
      esac
      [ "$now" -ge "$holder_started" ] 2>/dev/null || exit 0
      [ "$((now - holder_started))" -ge "$lock_ttl" ] 2>/dev/null || exit 0
      rm -f "$lock_dir"/owner "$lock_dir"/owner.* 2>/dev/null || exit 0
      rmdir "$lock_dir" 2>/dev/null || exit 0
      mkdir "$lock_dir" 2>/dev/null || exit 0
      break
    fi
    reclaim_stale_lock || exit 0
    break
  done
  owner_tmp="$lock_owner.tmp.$$"
  if ! printf "%s\n%s\n" "$$" "$now" > "$owner_tmp" 2>/dev/null; then
    rm -f "$owner_tmp" 2>/dev/null || true
    rmdir "$lock_dir" 2>/dev/null || true
    exit 0
  fi
  if ! mv "$owner_tmp" "$lock_owner" 2>/dev/null; then
    rm -f "$owner_tmp" 2>/dev/null || true
    rmdir "$lock_dir" 2>/dev/null || true
    exit 0
  fi
  cleanup() {
    rm -f "$lock_dir"/owner "$lock_dir"/owner.* 2>/dev/null || true
    rmdir "$lock_dir" 2>/dev/null || true
  }
  trap cleanup 0
  trap "exit 0" HUP INT TERM
  session-finder skill extract --pending >/dev/null 2>&1
' >/dev/null 2>&1 </dev/null &
exit 0
