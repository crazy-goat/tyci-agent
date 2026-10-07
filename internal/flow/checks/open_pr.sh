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

branch="${TYCI_BRANCH:-}"
default="${TYCI_DEFAULT_BRANCH:-}"
if [ -z "$branch" ] || [ "$branch" = "$default" ]; then
    echo "open_pr.sh: TYCI_BRANCH '$branch' is empty or the default branch; cannot look for its PR" >&2
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
    echo "open_pr.sh: PR #$pr is open, but 'git fetch origin $branch' failed." \
        "Check that the branch exists on origin, then answer 'goto open_pr'." >&2
    echo fail
    exit 0
fi

head=$(git rev-parse HEAD)
pr_head=$(git rev-parse FETCH_HEAD)
if [ -n "$(git status --porcelain)" ] ||
    ! { git merge-base --is-ancestor "$head" "$pr_head" ||
        git merge-base --is-ancestor "$head" "origin/$default"; }; then
    echo "open_pr.sh: PR #$pr is open from $branch, but the worktree has changes or commits" \
        "that are on neither origin/$branch nor origin/$default. Nothing was changed." \
        "A human decides: keep the local work (merge origin/$branch into it by hand) or drop it" \
        "(git reset --hard origin/$branch), then answer 'goto open_pr'." >&2
    echo fail
    exit 0
fi

git reset -q --hard "$pr_head" >&2
printf '%s\n' "$pr" >"$TYCI_RUN_DIR/pr"
echo "open_pr.sh: continuing PR #$pr: the worktree is on origin/$branch ($(git rev-parse --short HEAD))." \
    "The run does not code the issue again: it merges origin/$default and waits for CI." >&2
echo found
