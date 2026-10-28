#!/usr/bin/env bash
# issue_done.sh: security gate of the issue-to-merge loop (state check_done).
# An issue is worked on only if it is open, accepted, its author has write/admin
# permission, and it is unassigned or assigned to the account running this run.
#
# Env in:  TYCI_REPO (owner/name), TYCI_ISSUE, optional TYCI_ACCEPT_LABEL (default: accepted).
# Keys:    go        work on the issue
#          go-risky  work on the issue after its plan is approved: the issue has the label "risky"
#          skip      do not work on the issue (including assignment to another account)
# Errors:  any gh failure other than HTTP 404 exits non-zero WITHOUT a key; stderr then has a
#          describe.sh block (RESULT: error) that says what failed.
# Idempotent. Assignment is retained after the run ends and must be released manually. Never prints tokens.
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

label="${TYCI_ACCEPT_LABEL:-accepted}"

info=$(gh issue view "$TYCI_ISSUE" -R "$TYCI_REPO" --json state,labels,author,assignees)

state=$(jq -r '.state' <<<"$info")
has_label=$(jq -r --arg l "$label" '[.labels[].name] | index($l) != null' <<<"$info")
if [ "$state" != "OPEN" ] || [ "$has_label" != "true" ]; then
    echo skip
    exit 0
fi
has_risky=$(jq -r '[.labels[].name] | index("risky") != null' <<<"$info")

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
    if [ "$perm" != "admin" ] && [ "$perm" != "write" ]; then
        echo skip
        exit 0
    fi

    if ! login=$(gh api user --jq .login); then
        describe error "could not read the authenticated GitHub account" \
            "gh is not logged in or the GitHub API is unavailable" \
            "check 'gh auth status'; when the API answers, answer retry so the gate runs again"
        exit 1
    fi
    assignees=$(jq -r '[.assignees[]?.login] | .[]' <<<"$info")
    if [ -n "$assignees" ] && grep -Fvxq -- "$login" <<<"$assignees"; then
        echo skip
        exit 0
    fi

    # Add the current account when unassigned; adding it again is harmless if
    # the issue was already assigned to this account.
    if ! gh issue edit "$TYCI_ISSUE" -R "$TYCI_REPO" --add-assignee "$login"; then
        describe error "could not assign issue #$TYCI_ISSUE to '$login'" \
            "the account may lack permission to assign issues, or the GitHub API may be unavailable" \
            "check the issue permissions and 'gh auth status'; when assignment succeeds, answer retry so the gate runs again"
        exit 1
    fi
    if [ "$has_risky" = "true" ]; then
        echo go-risky
    else
        echo go
    fi
    ;;
404)
    echo skip
    ;;
*)
    echo "issue_done: unexpected response from permission API: ${status:-none}" >&2
    describe error "the GitHub permission API returned HTTP ${status:-none} for issue author '$author' of #$TYCI_ISSUE" \
        "a GitHub outage, a rate limit, or gh is not logged in" \
        "check 'gh auth status' and 'gh api repos/$TYCI_REPO/collaborators/$author/permission'; when it answers 200 or 404, answer retry so the gate runs again"
    exit 1
    ;;
esac
