#!/usr/bin/env bash
# push.sh: pushes the run branch and finds or creates its pull request (state push).
# The run branch is TYCI_BRANCH, unless open_pr.sh continued a PR from another branch: then it
# is the head branch in $TYCI_RUN_DIR/pr_branch (#374). Pushing is a script, never an agent
# action. Only the worktree HEAD is pushed, to refs/heads/<b> on origin. The default branch is
# refused. No force option is ever used.
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
# lib_forge.sh selects gh or `tyci forge` (GitLab). A copy of this script without it works only for GitHub.
forge_lib="$(dirname "${BASH_SOURCE[0]}")/lib_forge.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$forge_lib" ]; then . "$forge_lib"; else is_gitlab() { [ "${TYCI_FORGE:-github}" != gitlab ] && return 1; echo "$(basename "$0"): lib_forge.sh is missing" >&2; exit 1; }; github_only() { is_gitlab || :; }; fi

branch="${TYCI_BRANCH:-}"
if [ -s "${TYCI_RUN_DIR:-}/pr_branch" ]; then branch="$(<"$TYCI_RUN_DIR/pr_branch")"; fi
if [ -z "$branch" ] || [ "$branch" = "${TYCI_DEFAULT_BRANCH:-}" ]; then
    describe fail "push.sh refuses to push branch '$branch': it is empty or the default branch" \
        "the run was started with a wrong branch (a setup problem, not a code problem)" \
        "nothing to fix in the worktree; return failed so a human restarts the run on branch issue-<N>"
    echo fail
    exit 0
fi

if ! git push origin "HEAD:refs/heads/$branch" >&2; then
    if git fetch -q origin "refs/heads/$branch" 2>/dev/null &&
        ! git merge-base --is-ancestor FETCH_HEAD HEAD; then
        describe fail "git push to origin/$branch was rejected (non-fast-forward): origin/$branch has commits that the local $branch does not have (the branches diverged). tyci never force-pushes" \
            "the branch already has commits from an earlier run or a person (often with an open PR)" \
            "compare 'git log HEAD..origin/$branch' and 'git log origin/$branch..HEAD'. If origin/$branch already has the work, run 'git reset --hard origin/$branch' and 'git merge origin/${TYCI_DEFAULT_BRANCH:-main}'; else run 'git merge origin/$branch' and resolve the conflicts. Never force-push. Then return ok" \
            "origin/$branch (just fetched) = $(git rev-parse --short FETCH_HEAD)"
    else
        describe fail "git push of $branch was rejected (see the git output above)" \
            "no network, no push permission, or a branch protection rule" \
            "run 'git push origin HEAD:refs/heads/$branch' to see the error; fix a temporary cause and return ok, else return failed"
    fi
    echo fail
    exit 0
fi

find_pr() {
    if is_gitlab; then
        forge_op pr-list "$branch" | jq -r '.[0].number // empty'
    else
        gh pr list -R "$TYCI_REPO" --head "$branch" --state open --json number --jq '.[0].number'
    fi
}

pr=$(find_pr)
if [ -z "$pr" ]; then
    # The title is the subject of the first non-merge commit since the default branch. The last
    # commit can be a sync merge (rabbit-stream #864), so it is only the fallback.
    title=""
    default_ref="origin/$TYCI_DEFAULT_BRANCH"
    if git rev-parse --verify --quiet "$default_ref^{commit}" >/dev/null &&
        git merge-base --is-ancestor "$default_ref" HEAD; then
        title="$(git log --format=%s --reverse --no-merges "$default_ref"..HEAD | sed -n '1p')"
    fi
    if [ -z "$title" ]; then
        title="$(git log -1 --format=%s --no-merges HEAD)"
    fi
    if [ -z "$title" ]; then
        title="$(git log -1 --format=%s)"
    fi
    if [ -z "$title" ]; then
        title="$branch"
    fi
    # A run without an issue (for example a release, #207) gets an empty PR body.
    body=""
    if [ -n "${TYCI_ISSUE:-}" ]; then body="Closes #$TYCI_ISSUE"; fi
    if is_gitlab; then
        forge_op pr-create "$TYCI_DEFAULT_BRANCH" "$branch" "$title" "$body" >&2
    else
        gh pr create -R "$TYCI_REPO" --base "$TYCI_DEFAULT_BRANCH" --head "$branch" \
            --title "$title" --body "$body" >&2
    fi
    pr=$(find_pr)
fi

printf '%s\n' "$pr" >"$TYCI_RUN_DIR/pr"
echo ok
