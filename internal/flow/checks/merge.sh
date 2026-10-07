#!/usr/bin/env bash
# merge.sh: squash merges the pull request of the run (state merge).
# Merging is the most dangerous step, so the rules are fixed:
#   - merge only when the required check `ci-ok` is `pass`;
#   - the merge call ALWAYS has --squash --delete-branch --match-head-commit <local HEAD>;
#   - no other merge mode is ever used (no admin bypass, no auto merge, no plain merge, no rebase).
# Protected paths are the self-modification stop: a PR that changes the process files
# (.github/, .tyci/, internal/flow/checks/) is never merged unattended.
#
# Env in:  TYCI_PR, TYCI_REPO, TYCI_DEFAULT_BRANCH. cwd = the run worktree.
# Keys:    merged     the PR was squash merged
#          behind     the PR is behind the default branch (rebase it first)
#          protected  the diff touches a protected path; no gh call was made
#          fail       fetch, CI re-check, head match or merge failed
set -euo pipefail

if ! git fetch origin "$TYCI_DEFAULT_BRANCH" >&2; then
    echo fail
    exit 0
fi

protected=$(git diff --name-only "origin/${TYCI_DEFAULT_BRANCH}...HEAD" | grep -E '^(\.github/|\.tyci/|internal/flow/checks/)' || true)
if [ -n "$protected" ]; then
    echo "protected paths:" >&2
    echo "$protected" >&2
    echo protected
    exit 0
fi

bucket=$(gh pr checks "$TYCI_PR" -R "$TYCI_REPO" --json name,bucket --jq '[.[] | select(.name == "ci-ok")][0].bucket // ""') || bucket=""
# A gh failure (including "no checks reported") gives an empty bucket, which is "missing", as in ci_wait.sh.
if [ "$bucket" != pass ]; then
    echo "merge.sh: ci-ok is '${bucket:-missing}'" >&2
    if [ -z "$bucket" ] && [ "$(gh pr view "$TYCI_PR" -R "$TYCI_REPO" --json mergeStateStatus --jq .mergeStateStatus 2>/dev/null || true)" = BEHIND ]; then
        echo behind
        exit 0
    fi
    echo fail
    exit 0
fi

state=$(gh pr view "$TYCI_PR" -R "$TYCI_REPO" --json mergeStateStatus --jq .mergeStateStatus) || {
    echo fail
    exit 0
}
if [ "$state" = BEHIND ]; then
    echo behind
    exit 0
fi

head=$(git rev-parse HEAD)
remote_head=$(gh pr view "$TYCI_PR" -R "$TYCI_REPO" --json headRefOid --jq .headRefOid) || remote_head=""
if [ "$head" != "$remote_head" ]; then
    echo "merge.sh: local HEAD $head differs from PR head '$remote_head'" >&2
    echo fail
    exit 0
fi

if gh pr merge "$TYCI_PR" -R "$TYCI_REPO" --squash --delete-branch --match-head-commit "$head" >&2; then
    echo merged
else
    echo fail
fi
