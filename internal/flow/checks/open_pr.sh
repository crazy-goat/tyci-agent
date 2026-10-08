#!/usr/bin/env bash
# open_pr.sh: continues the open pull request of an earlier run (state open_pr).
# A run starts on a fresh branch issue-<N> from origin/<default branch>. When an open PR for
# the issue already exists (an earlier run stopped before the merge), this run takes over
# that PR: the worktree moves to the PR head. The PR is the one whose head is the run branch,
# or else one that GitHub links as closing the issue (a PR from another branch, #374). A PR
# from a fork is never used. So the run does not code the issue again, and its later push is
# a fast-forward to the PR head branch (push.sh reads $TYCI_RUN_DIR/pr_branch). The next states
# take the merge lock, merge origin/<default branch> into the branch (update) and wait for CI.
#
# Env in:  TYCI_REPO, TYCI_ISSUE, TYCI_BRANCH, TYCI_DEFAULT_BRANCH, TYCI_RUN_DIR. cwd = the run worktree.
# Keys:    none   no open PR for the issue: code the issue from origin/<default branch>
#          found  the worktree is on the PR head; the PR number is in $TYCI_RUN_DIR/pr and the
#                 head branch name in $TYCI_RUN_DIR/pr_branch
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
issue="${TYCI_ISSUE:-}"
if [ -z "$branch" ] || [ "$branch" = "$default" ]; then
    describe fail "TYCI_BRANCH '$branch' is empty or the default branch, so open_pr.sh cannot look for the PR of the issue" \
        "the run was started with a wrong branch (a setup problem, not a code problem)" \
        "nothing to fix in the worktree; return failed so the oracle asks a human to restart the run on branch issue-<N>"
    echo fail
    exit 0
fi

# The open PR of the issue: its head is the run branch, or GitHub links it as closing the issue
# (closingIssuesReferences: close, fix, resolve and their forms). The run branch wins when both
# match. A fork PR is never used: its head branch is not on origin.
# The run branch PR is looked up first on the server (--head), so it is found in a repo with more
# than 200 open PRs. The second lookup lists at most 200 open PRs.
pick_pr() {
    jq -r --arg b "$branch" --arg n "$issue" '
        [.[] | select(.isCrossRepository | not) |
            select(.headRefName == $b or ((.closingIssuesReferences // []) | any((.number | tostring) == $n)))]
        | sort_by(.headRefName != $b) | .[0] // empty | "\(.number) \(.headRefName)"'
}
fields=number,headRefName,isCrossRepository,closingIssuesReferences
found=$(gh pr list -R "$TYCI_REPO" --head "$branch" --state open --json "$fields" | pick_pr)
if [ -z "$found" ]; then
    found=$(gh pr list -R "$TYCI_REPO" --state open --limit 200 --json "$fields" | pick_pr)
fi
if [ -z "$found" ]; then
    echo "open_pr.sh: no open PR for issue #$issue or from $branch; the run codes the issue from origin/$default" >&2
    echo none
    exit 0
fi
read -r pr pr_branch <<<"$found"

if ! git fetch origin "refs/heads/$pr_branch" >&2; then
    describe fail "PR #$pr is open from $pr_branch, but 'git fetch origin $pr_branch' failed (see the git output above)" \
        "a network or auth problem, or the branch was deleted on origin while the PR stays open" \
        "run 'git ls-remote origin refs/heads/$pr_branch'; if the branch exists, fetch it again and return ok; if it is gone, return failed (a human must close the PR or push the branch)" \
        "open PR #$pr"
    echo fail
    exit 0
fi

head=$(git rev-parse HEAD)
pr_head=$(git rev-parse FETCH_HEAD)
if [ -n "$(git status --porcelain)" ] ||
    ! { git merge-base --is-ancestor "$head" "$pr_head" ||
        git merge-base --is-ancestor "$head" "origin/$default"; }; then
    describe fail "PR #$pr is open from $pr_branch, but the worktree has changes or commits that are on neither origin/$pr_branch nor origin/$default; nothing was changed" \
        "an earlier run or a person left work in this worktree that was never pushed" \
        "look at 'git status' and 'git log origin/$pr_branch..HEAD'; if the local work is a leftover, run 'git reset --hard origin/$pr_branch' and return ok; if it has real work, return failed (a human decides)" \
        "open PR #$pr; PR head = $(git rev-parse --short "$pr_head")"
    echo fail
    exit 0
fi

git reset -q --hard "$pr_head" >&2
printf '%s\n' "$pr" >"$TYCI_RUN_DIR/pr"
printf '%s\n' "$pr_branch" >"$TYCI_RUN_DIR/pr_branch"
echo "open_pr.sh: continuing PR #$pr: the worktree is on origin/$pr_branch ($(git rev-parse --short HEAD))." \
    "The run does not code the issue again: it merges origin/$default and waits for CI." >&2
echo found
