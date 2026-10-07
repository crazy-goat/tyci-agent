#!/usr/bin/env bash
# rebase.sh: brings the issue branch up to date with origin/<default branch> (state rebase).
# The name is historical: this script MERGES, it never rebases. History is squash-merged,
# so no force push is needed. Pushing is left to the sibling push.sh (explicit ref, never
# the default branch, never force).
#
# Env in:  TYCI_DEFAULT_BRANCH, TYCI_BRANCH, plus what push.sh needs. cwd = the run worktree.
# Keys:    ok        branch is up to date and pushed
#          conflict  merge conflicted; the merge was aborted, the worktree is clean, and stderr
#                    names the files and tells the coder how to resolve them
#          fail      fetch, merge or push failed
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

if ! git fetch origin "$TYCI_DEFAULT_BRANCH" >&2; then
    describe fail "git fetch origin $TYCI_DEFAULT_BRANCH failed (see the git output above)" \
        "no network, git is not authenticated, or a temporary GitHub problem" \
        "run 'git fetch origin $TYCI_DEFAULT_BRANCH'; when it works, return ok so the step runs again"
    echo fail
    exit 0
fi

# A conflict only in CHANGELOG.md keeps both sides (parallel runs all add an entry at the
# top). Returns 0 when the merge is committed. Any other conflict stays for the caller.
resolve_changelog() {
    [ "$(git diff --name-only --diff-filter=U)" = CHANGELOG.md ] || return 1
    local tmp rc=1
    tmp=$(mktemp -d)
    if git show :2:CHANGELOG.md >"$tmp/ours" && git show :1:CHANGELOG.md >"$tmp/base" &&
        git show :3:CHANGELOG.md >"$tmp/theirs" && git merge-file --union "$tmp/ours" "$tmp/base" "$tmp/theirs" &&
        cp "$tmp/ours" CHANGELOG.md && git add CHANGELOG.md && git commit --no-edit -q >&2; then
        rc=0
    fi
    rm -rf "$tmp"
    return $rc
}

if ! git merge --no-edit "origin/$TYCI_DEFAULT_BRANCH" >&2 && ! resolve_changelog; then
    if git rev-parse -q --verify MERGE_HEAD >/dev/null; then
        files=$(git diff --name-only --diff-filter=U | paste -sd ' ' -)
        git merge --abort
        describe conflict "merging origin/$TYCI_DEFAULT_BRANCH into $TYCI_BRANCH conflicts in: $files. The merge was aborted and the worktree is clean" \
            "$TYCI_DEFAULT_BRANCH changed the same lines as this branch" \
            "run 'git merge origin/$TYCI_DEFAULT_BRANCH', resolve the conflicts in these files (keep the changes of both sides), run the checks and commit the merge. Do not push"
        echo conflict
    else
        git merge --abort 2>/dev/null || true
        describe fail "git merge origin/$TYCI_DEFAULT_BRANCH into $TYCI_BRANCH failed without a conflict (see the git output above); the merge was aborted" \
            "uncommitted changes or untracked files in the worktree that the merge would overwrite, or a broken git state" \
            "run 'git status'; commit or remove the files that block the merge (never drop committed work), then return ok so the step runs again"
        echo fail
    fi
    exit 0
fi

push="$(dirname "${BASH_SOURCE[0]}")/push.sh"
if [ ! -f "$push" ]; then
    describe fail "push.sh was not found next to rebase.sh in $(dirname "${BASH_SOURCE[0]}")" \
        "a custom check directory that has rebase.sh but not push.sh" \
        "nothing to fix in the worktree; return failed so a human installs push.sh next to rebase.sh"
    echo fail
    exit 0
fi
last="$(bash "$push" | awk 'NF{l=$0} END{print l}')" || last=fail
# A failed push.sh already printed its describe block (stderr passes through).
if [ "$last" = ok ]; then echo ok; else echo fail; fi
