#!/usr/bin/env bash
# lock.sh: takes the merge lock of the repository (state lock). Only one run per repository
# goes through push, CI and merge at a time; the other runs wait here. The lock is released
# by itself: it counts as free when its holder process is dead, or when the state.json of the
# holder run is no longer in a merge-phase state (for example ask, code, findings or end).
# The same run takes its own lock again without waiting.
#
# Env in:  TYCI_REPO, TYCI_RUN_DIR, HOME, TYCI_LOCK_POLL_SEC (default 5).
# Keys:    ok  the lock is held by this run
# The wait is unbounded: the state timeout_sec stops it (key timeout).
set -euo pipefail

dir="$HOME/.tyci/locks/$(printf '%s' "${TYCI_REPO:-}" | tr '/' '_').merge"
poll="${TYCI_LOCK_POLL_SEC:-5}"
phase=" lock push post_review ci comments merge rebase merge_decision "

stale() {
    local pid run cur
    { read -r pid && read -r run; } <"$dir/owner" 2>/dev/null || return 0
    kill -0 "$pid" 2>/dev/null || return 0
    [ "$run" = "$TYCI_RUN_DIR" ] && return 0
    cur=$(jq -r '.current // ""' "$run/state.json" 2>/dev/null) || return 0
    case "$phase" in *" $cur "*) return 1 ;; esac
    return 0
}

mkdir -p "$(dirname "$dir")"
while :; do
    if mkdir "$dir" 2>/dev/null; then
        printf '%s\n%s\n' "$PPID" "$TYCI_RUN_DIR" >"$dir/owner"
        echo ok
        exit 0
    fi
    if stale; then
        rm -rf "$dir"
        continue
    fi
    echo "lock.sh: another run holds the merge lock, waiting" >&2
    sleep "$poll"
done
