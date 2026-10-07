#!/usr/bin/env bash
# ci_wait.sh: waits until the required check `ci-ok` of a pull request is conclusive.
# Read-only, idempotent, safe to re-run after a restart. Other checks never decide.
#
# Env in:  TYCI_PR (required), TYCI_REPO, TYCI_CI_POLL_SEC (default 20),
#          TYCI_CI_APPEAR_SEC (default 300).
# Keys:    green  ci-ok passed
#          red    ci-ok failed or was cancelled
#          conflict  the PR conflicts with the default branch (no CI runs for it)
#          behind    ci-ok is missing and the PR is behind the default branch
#          fail   no PR number, ci-ok never appeared, or 3 consecutive gh failures
# The loop is otherwise unbounded: the state timeout_sec stops it. Progress goes to stderr.
set -euo pipefail

pr="${TYCI_PR:-}"
if [ -z "$pr" ]; then
    echo "ci_wait.sh: TYCI_PR is not set" >&2
    echo fail
    exit 0
fi
poll="${TYCI_CI_POLL_SEC:-20}"
appear="${TYCI_CI_APPEAR_SEC:-300}"

errf=$(mktemp)
trap 'rm -f "$errf"' EXIT
errors=0
start=$SECONDS
while :; do
    view=$(gh pr view "$pr" -R "${TYCI_REPO:-}" --json mergeable,mergeStateStatus --jq '.mergeable + " " + .mergeStateStatus' 2>/dev/null || true)
    case "$view" in
    CONFLICTING* | *DIRTY)
        echo conflict
        exit 0
        ;;
    esac
    out=$(gh pr checks "$pr" -R "${TYCI_REPO:-}" --json name,state,bucket 2>"$errf" || true)
    # gh exits 1 with this text when no check exists yet: that is "missing", not an API error.
    case "$(cat "$errf")" in *"no checks reported"*) out='[]' ;; esac
    if bucket=$(jq -er 'if type == "array" then [.[] | select(.name == "ci-ok")][0].bucket // "missing" else empty end' <<<"$out" 2>/dev/null); then
        errors=0
        case "$bucket" in
        pass)
            echo green
            exit 0
            ;;
        fail | cancel)
            echo red
            exit 0
            ;;
        esac
        echo "ci_wait.sh: ci-ok is $bucket" >&2
        if [ "$bucket" = missing ] && [ "${view##* }" = BEHIND ]; then
            echo behind
            exit 0
        fi
        if [ "$bucket" = missing ] && [ $((SECONDS - start)) -ge "$appear" ]; then
            echo "ci_wait.sh: ci-ok did not appear" >&2
            echo fail
            exit 0
        fi
    else
        errors=$((errors + 1))
        echo "ci_wait.sh: gh failure $errors" >&2
        if [ "$errors" -ge 3 ]; then
            echo fail
            exit 0
        fi
    fi
    sleep "$poll"
done
