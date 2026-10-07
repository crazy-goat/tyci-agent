#!/usr/bin/env bash
# push.sh: pushes the issue branch and finds or creates its pull request (state push).
# Pushing is a script, never an agent action. Only refs/heads/<b>:refs/heads/<b> is
# pushed. The default branch is refused. No force option is ever used.
#
# Env in:  TYCI_BRANCH, TYCI_DEFAULT_BRANCH, TYCI_RUN_DIR, TYCI_REPO, TYCI_ISSUE.
#          cwd = the run worktree.
# Keys:    ok    pushed, PR number written to $TYCI_RUN_DIR/pr
#          fail  empty or default branch, or the push was rejected (also on a diverged branch)
# Errors:  any gh failure (list or create) exits non-zero WITHOUT a key (with a describe.sh block).
# Every fail prints a describe.sh block on stderr: what failed and what to do.
# Idempotent: a second run pushes nothing new and finds the PR.
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

branch="${TYCI_BRANCH:-}"
if [ -z "$branch" ] || [ "$branch" = "${TYCI_DEFAULT_BRANCH:-}" ]; then
    describe fail "push.sh refuses to push branch '$branch': it is empty or the default branch" \
        "the run was started with a wrong branch (a setup problem, not a code problem)" \
        "nothing to fix in the worktree; return failed so a human restarts the run on branch issue-<N>"
    echo fail
    exit 0
fi

if ! git push origin "refs/heads/$branch:refs/heads/$branch" >&2; then
    if git fetch -q origin "refs/heads/$branch" 2>/dev/null &&
        ! git merge-base --is-ancestor FETCH_HEAD HEAD; then
        describe fail "git push to origin/$branch was rejected (non-fast-forward): origin/$branch has commits that the local $branch does not have (the branches diverged). tyci never force-pushes" \
            "the branch already has commits from an earlier run or a person (often with an open PR)" \
            "compare 'git log HEAD..origin/$branch' and 'git log origin/$branch..HEAD'. If origin/$branch already has the work, run 'git reset --hard origin/$branch' and 'git merge origin/${TYCI_DEFAULT_BRANCH:-main}'; else run 'git merge origin/$branch' and resolve the conflicts. Never force-push. Then return ok" \
            "origin/$branch (just fetched) = $(git rev-parse --short FETCH_HEAD)"
    else
        describe fail "git push of $branch was rejected (see the git output above)" \
            "no network, no push permission, or a branch protection rule" \
            "run 'git push origin refs/heads/$branch:refs/heads/$branch' to see the error; fix a temporary cause and return ok, else return failed"
    fi
    echo fail
    exit 0
fi

find_pr() {
    gh pr list -R "$TYCI_REPO" --head "$branch" --state open --json number --jq '.[0].number'
}

pr=$(find_pr)
if [ -z "$pr" ]; then
    gh pr create -R "$TYCI_REPO" --base "$TYCI_DEFAULT_BRANCH" --head "$branch" \
        --title "$(git log -1 --format=%s)" --body "Closes #$TYCI_ISSUE" >&2
    pr=$(find_pr)
fi

printf '%s\n' "$pr" >"$TYCI_RUN_DIR/pr"
echo ok
