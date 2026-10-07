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
# Keys:    merged     the PR was squash merged, or was already merged (a resumed run)
#          behind     the PR is behind the default branch (rebase it first)
#          protected  the diff touches a protected path; no gh call was made
#          fail       fetch, CI re-check, head match or merge failed
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

if ! git fetch origin "$TYCI_DEFAULT_BRANCH" >&2; then
    describe fail "git fetch origin $TYCI_DEFAULT_BRANCH failed (see the git output above); nothing was merged" \
        "no network, git is not authenticated, or a temporary GitHub problem" \
        "run 'git fetch origin $TYCI_DEFAULT_BRANCH'; when it works, return ok so the step runs again"
    echo fail
    exit 0
fi

protected=$(git diff --name-only "origin/${TYCI_DEFAULT_BRANCH}...HEAD" | grep -E '^(\.github/|\.tyci/|internal/flow/checks/)' || true)
if [ -n "$protected" ]; then
    describe protected "the PR changes protected paths: $(paste -sd ' ' - <<<"$protected"); tyci never merges such a PR unattended" \
        "the issue changes the process files (.github/, .tyci/, internal/flow/checks/)" \
        "a human reviews and merges PR #$TYCI_PR by hand"
    echo protected
    exit 0
fi

# Idempotent after a crash between the merge and the state write: the resumed run
# finds the PR already merged.
if [ "$(gh pr view "$TYCI_PR" -R "$TYCI_REPO" --json state --jq .state 2>/dev/null || true)" = MERGED ]; then
    echo "merge.sh: PR $TYCI_PR is already merged" >&2
    echo merged
    exit 0
fi

bucket=$(gh pr checks "$TYCI_PR" -R "$TYCI_REPO" --json name,bucket --jq '[.[] | select(.name == "ci-ok")][0].bucket // ""') || bucket=""
# A gh failure (including "no checks reported") gives an empty bucket, which is "missing", as in ci_wait.sh.
if [ "$bucket" != pass ]; then
    echo "merge.sh: ci-ok is '${bucket:-missing}'" >&2
    if [ -z "$bucket" ] && [ "$(gh pr view "$TYCI_PR" -R "$TYCI_REPO" --json mergeStateStatus --jq .mergeStateStatus 2>/dev/null || true)" = BEHIND ]; then
        describe behind "PR #$TYCI_PR has no ci-ok check and is behind the default branch" \
            "the default branch moved after CI ran" \
            "the run goes to rebase: it merges origin/$TYCI_DEFAULT_BRANCH, pushes and waits for CI again"
        echo behind
        exit 0
    fi
    describe fail "ci-ok of PR #$TYCI_PR is '${bucket:-missing}', not pass, so merge.sh did not merge" \
        "CI was started again after the ci step, a new push started a new CI run, or gh failed" \
        "run 'gh pr checks $TYCI_PR'; if ci-ok is pending or missing, return ok after it passes ('gh pr checks $TYCI_PR --watch'); if it failed, return failed"
    echo fail
    exit 0
fi

state=$(gh pr view "$TYCI_PR" -R "$TYCI_REPO" --json mergeStateStatus --jq .mergeStateStatus) || {
    describe fail "'gh pr view $TYCI_PR --json mergeStateStatus' failed (see the gh output above); nothing was merged" \
        "no network, gh is not logged in, a rate limit, or a GitHub outage" \
        "run 'gh auth status' and 'gh pr view $TYCI_PR'; when gh works again, return ok so the step runs again"
    echo fail
    exit 0
}
if [ "$state" = BEHIND ]; then
    describe behind "PR #$TYCI_PR is behind the default branch (mergeStateStatus BEHIND)" \
        "the default branch moved after CI ran" \
        "the run goes to rebase: it merges origin/$TYCI_DEFAULT_BRANCH, pushes and waits for CI again"
    echo behind
    exit 0
fi

head=$(git rev-parse HEAD)
remote_head=$(gh pr view "$TYCI_PR" -R "$TYCI_REPO" --json headRefOid --jq .headRefOid) || remote_head=""
if [ "$head" != "$remote_head" ]; then
    describe fail "the local HEAD $head differs from the PR head '$remote_head', so merge.sh did not merge (--match-head-commit protects against a merge of unseen code)" \
        "the local branch has commits that were not pushed, or someone pushed to the PR branch" \
        "compare 'git log --oneline -3' with 'git ls-remote origin refs/heads/${TYCI_BRANCH:-<branch>}'. If the local branch is ahead, push it (git push origin refs/heads/<b>:refs/heads/<b>) and return failed (CI must run again: the oracle sends the run to ci); if origin is ahead, return failed"
    echo fail
    exit 0
fi

if gh pr merge "$TYCI_PR" -R "$TYCI_REPO" --squash --delete-branch --match-head-commit "$head" >&2; then
    echo merged
else
    describe fail "gh pr merge $TYCI_PR --squash failed (see the gh output above)" \
        "the PR head moved, a required check or review is missing, the branch is not up to date, or a temporary GitHub problem" \
        "run 'gh pr view $TYCI_PR --json mergeStateStatus,reviewDecision' and 'gh pr checks $TYCI_PR'; fix a temporary cause (for example wait and try again) and return ok; else return failed"
    echo fail
fi
