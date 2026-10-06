#!/usr/bin/env bash
# issue_done.sh: security gate of the issue-to-merge loop (state check_done).
# An issue is worked on only if it is open, has the accept label, AND its author
# has write or admin permission on the repository.
#
# Env in:  TYCI_REPO (owner/name), TYCI_ISSUE, optional TYCI_ACCEPT_LABEL (default: accepted).
# Keys:    go    work on the issue
#          skip  do not work on the issue
# Errors:  any gh failure other than HTTP 404 exits non-zero WITHOUT a key.
# Idempotent, read-only. Never prints tokens.
set -euo pipefail

label="${TYCI_ACCEPT_LABEL:-accepted}"

info=$(gh issue view "$TYCI_ISSUE" -R "$TYCI_REPO" --json state,labels,author)

state=$(jq -r '.state' <<<"$info")
has_label=$(jq -r --arg l "$label" '[.labels[].name] | index($l) != null' <<<"$info")
if [ "$state" != "OPEN" ] || [ "$has_label" != "true" ]; then
    echo skip
    exit 0
fi

author=$(jq -r '.author.login // ""' <<<"$info")
author="${author#app/}"
if ! [[ "$author" =~ ^[A-Za-z0-9][A-Za-z0-9-]*(\[bot\])?$ ]]; then
    echo skip
    exit 0
fi

# gh api exits non-zero on HTTP errors, so judge by the status line, not the exit code.
resp=$(gh api -i "repos/$TYCI_REPO/collaborators/$author/permission" 2>/dev/null) || true
status=$(head -n 1 <<<"$resp" | tr -d '\r' | awk '{print $2}')

case "$status" in
200)
    perm=$(sed -n '/^\r\{0,1\}$/,$p' <<<"$resp" | jq -r '.permission // ""')
    if [ "$perm" = "admin" ] || [ "$perm" = "write" ]; then
        echo go
    else
        echo skip
    fi
    ;;
404)
    echo skip
    ;;
*)
    echo "issue_done: unexpected response from permission API: ${status:-none}" >&2
    exit 1
    ;;
esac
