#!/usr/bin/env bash
# open_pr.sh: continues the open pull request of an earlier run (state open_pr).
# A run starts on a fresh branch issue-<N> from origin/<default branch>. When an open PR
# from the same branch already exists (an earlier run stopped before the merge), this run
# takes over that PR: the worktree branch moves to the PR head. So the run does not code
# the issue again, and its later push is a fast-forward. The next states take the merge
# lock, merge origin/<default branch> into the branch (update) and wait for CI.
#
# Env in:  TYCI_REPO, TYCI_BRANCH, TYCI_DEFAULT_BRANCH, TYCI_RUN_DIR. cwd = the run worktree.
# Keys:    none   no open PR from the branch: code the issue from origin/<default branch>
#          found  the worktree is on the PR head; the PR number is in $TYCI_RUN_DIR/pr
#          fail   the PR head cannot be fetched, or the worktree has work that is not on
#                 the PR head nor on origin/<default branch> (nothing is changed then)
# Errors:  a gh failure exits non-zero WITHOUT a key.
# Idempotent: a second run finds the worktree already on the PR head.
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

branch="${TYCI_BRANCH:-}"
default="${TYCI_DEFAULT_BRANCH:-}"
if [ -z "$branch" ] || [ "$branch" = "$default" ]; then
    describe fail "TYCI_BRANCH '$branch' is empty or the default branch, so open_pr.sh cannot look for the PR of the issue" \
        "the run was started with a wrong branch (a setup problem, not a code problem)" \
        "nothing to fix in the worktree; return failed so the oracle asks a human to restart the run on branch issue-<N>"
    echo fail
    exit 0
fi

pr=$(gh pr list -R "$TYCI_REPO" --head "$branch" --state open --json number --jq '.[0].number')
if [ -z "$pr" ]; then
    echo "open_pr.sh: no open PR from $branch; the run codes the issue from origin/$default" >&2
    echo none
    exit 0
fi

if ! git fetch origin "refs/heads/$branch" >&2; then
    describe fail "PR #$pr is open from $branch, but 'git fetch origin $branch' failed (see the git output above)" \
        "a network or auth problem, or the branch was deleted on origin while the PR stays open" \
        "run 'git ls-remote origin refs/heads/$branch'; if the branch exists, fetch it again and return ok; if it is gone, return failed (a human must close the PR or push the branch)" \
        "open PR #$pr"
    echo fail
    exit 0
fi

head=$(git rev-parse HEAD)
pr_head=$(git rev-parse FETCH_HEAD)
if [ -n "$(git status --porcelain)" ] ||
    ! { git merge-base --is-ancestor "$head" "$pr_head" ||
        git merge-base --is-ancestor "$head" "origin/$default"; }; then
    describe fail "PR #$pr is open from $branch, but the worktree has changes or commits that are on neither origin/$branch nor origin/$default; nothing was changed" \
        "an earlier run or a person left work in this worktree that was never pushed" \
        "look at 'git status' and 'git log origin/$branch..HEAD'; if the local work is a leftover, run 'git reset --hard origin/$branch' and return ok; if it has real work, return failed (a human decides)" \
        "open PR #$pr; PR head = $(git rev-parse --short "$pr_head")"
    echo fail
    exit 0
fi

git reset -q --hard "$pr_head" >&2
printf '%s\n' "$pr" >"$TYCI_RUN_DIR/pr"
echo "open_pr.sh: continuing PR #$pr: the worktree is on origin/$branch ($(git rev-parse --short HEAD))." \
    "The run does not code the issue again: it merges origin/$default and waits for CI." >&2
echo found
