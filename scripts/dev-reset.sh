#!/usr/bin/env bash
# Wipe the dev repository, restart the dev server (:12003), and re-import the dev subset of
# both Meta exports. Takes ~10-20 s. Run after rebuilding the binary.
set -euo pipefail
DEV=/mnt/photos/timelinize/repo-dev
LOG=/mnt/photos/timelinize/logs/server-dev.log
export TLZ_ADMIN_ADDR=0.0.0.0:12003 XDG_CONFIG_HOME=/root/.config/timelinize-dev TLZ_ORIGIN=http://10.0.10.46:12003

# stop the dev server only (match on its config dir, never kill the main server)
for p in $(pgrep -x timelinize); do
  if tr '\0' '\n' </proc/$p/environ | grep -q '^XDG_CONFIG_HOME=/root/.config/timelinize-dev$'; then
    kill "$p"; while kill -0 "$p" 2>/dev/null; do sleep 0.5; done
  fi
done
# NFS keeps silly-renamed (.nfs*) files around briefly after the process exits
for i in 1 2 3 4 5 6; do rm -rf "$DEV" 2>/dev/null && break; sleep 2; done
mkdir -p "$DEV"
: > "$LOG"
nohup timelinize serve >>"$LOG" 2>&1 &
for i in $(seq 1 30); do curl -sf http://127.0.0.1:12003/api/open-repositories >/dev/null 2>&1 && break; sleep 0.5; done
export TLZ_ADMIN_ADDR=127.0.0.1:12003
timelinize open-repository --repo_path "$DEV" --create true >/dev/null
exec "$(dirname "$0")/dev-import.sh" "$@"
