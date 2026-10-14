#!/usr/bin/env bash
# bump_deps.sh: updates the Go dependencies of the run worktree (state bump_deps, dependency-bump
# workflow, #209). Go only: it runs 'go get -u ./...' and 'go mod tidy'.
# When go.mod or go.sum changed, it commits them with the message 'chore(deps): bump Go dependencies'.
# A script commits here because the dependency-bump run has no coder step on the green path.
# The ask table is not written here: the state ask_table writes it from the branch diff, after the
# code and verify states may have changed go.mod again.
#
# Env in:  cwd = the run worktree.
# Keys:    changed   go.mod or go.sum changed; the commit is made
#          nochange  the update changed nothing; the run ends
#          fail      go or git failed (the output above says why)
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

if ! go get -u ./... >&2 || ! go mod tidy >&2; then
    describe fail "'go get -u ./...' or 'go mod tidy' failed (see the go output above); the worktree may have partial changes" \
        "no network, a Go proxy outage, or a module that no longer builds" \
        "run 'go get -u ./...' in the worktree to see the error; when it works again, return ok so the step runs again; else return failed"
    echo fail
    exit 0
fi

if git diff --quiet -- go.mod go.sum; then
    echo nochange
    exit 0
fi

if ! git add go.mod go.sum || ! git commit -q -m "chore(deps): bump Go dependencies" >&2; then
    describe fail "git could not commit the go.mod and go.sum changes" \
        "no git identity is configured for the worktree, or a hook rejected the commit" \
        "run 'git status' and 'git commit'; fix the cause, then return ok so the step runs again"
    echo fail
    exit 0
fi
echo changed
