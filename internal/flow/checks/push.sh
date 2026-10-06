#!/usr/bin/env bash
# push.sh: pushes the issue branch and finds or creates its pull request (state push).
# Pushing is a script, never an agent action. Only refs/heads/<b>:refs/heads/<b> is
# pushed. The default branch is refused. No force option is ever used.
#
# Env in:  TYCI_BRANCH, TYCI_DEFAULT_BRANCH, TYCI_RUN_DIR, TYCI_REPO, TYCI_ISSUE.
#          cwd = the run worktree.
# Keys:    ok    pushed, PR number written to $TYCI_RUN_DIR/pr
#          fail  empty or default branch, or the push was rejected
# Errors:  any gh failure (list or create) exits non-zero WITHOUT a key.
# Idempotent: a second run pushes nothing new and finds the PR.
set -euo pipefail

branch="${TYCI_BRANCH:-}"
if [ -z "$branch" ] || [ "$branch" = "${TYCI_DEFAULT_BRANCH:-}" ]; then
    echo "push.sh: refusing to push '$branch'" >&2
    echo fail
    exit 0
fi

if ! git push origin "refs/heads/$branch:refs/heads/$branch" >&2; then
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
