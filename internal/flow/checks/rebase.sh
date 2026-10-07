#!/usr/bin/env bash
# rebase.sh: brings the issue branch up to date with origin/<default branch> (state rebase).
# The name is historical: this script MERGES, it never rebases. History is squash-merged,
# so no force push is needed. Pushing is left to the sibling push.sh (explicit ref, never
# the default branch, never force).
#
# Env in:  TYCI_DEFAULT_BRANCH, TYCI_BRANCH, plus what push.sh needs. cwd = the run worktree.
# Keys:    ok        branch is up to date and pushed
#          conflict  merge conflicted; the merge was aborted, the worktree is clean
#          fail      fetch, merge or push failed
set -euo pipefail

git fetch origin "$TYCI_DEFAULT_BRANCH" >&2 || { echo fail; exit 0; }

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
        git merge --abort
        echo conflict
    else
        git merge --abort 2>/dev/null || true
        echo fail
    fi
    exit 0
fi

push="$(dirname "${BASH_SOURCE[0]}")/push.sh"
if [ ! -f "$push" ]; then
    echo "rebase.sh: push.sh not found next to me" >&2
    echo fail
    exit 0
fi
last="$(bash "$push" | awk 'NF{l=$0} END{print l}')" || last=fail
if [ "$last" = ok ]; then echo ok; else echo fail; fi
