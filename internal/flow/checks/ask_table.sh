#!/usr/bin/env bash
# ask_table.sh: writes the ask table of the dependency-bump run (state ask_table, #209). The table is
# the diff of go.mod and go.sum between the default branch and HEAD of the run worktree, so it shows
# the versions that the merge brings in, also after a later code or rebase state changed them.
# The diff is cut to 300 lines and passed through redact (lib_redact.sh) into
# $TYCI_RUN_DIR/ask-table.md. The ask message shows it.
#
# Env in:  TYCI_DEFAULT_BRANCH, TYCI_RUN_DIR. cwd = the run worktree.
# Keys:    ok    the table is written (an empty diff gives an empty table)
#          fail  git failed or the run dir is missing (the output above says why)
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi
# shellcheck disable=SC1091 # sibling file; shellcheck checks it on its own
. "$(dirname "${BASH_SOURCE[0]}")/lib_redact.sh"

run="${TYCI_RUN_DIR:-}"
if [ -z "$run" ] || [ -z "${TYCI_DEFAULT_BRANCH:-}" ]; then
    describe fail "TYCI_RUN_DIR or TYCI_DEFAULT_BRANCH is not set" \
        "the run was started without a run dir or a default branch" \
        "return failed; the dependency-bump run cannot write its ask table"
    echo fail
    exit 0
fi

if ! diff=$(git diff "origin/$TYCI_DEFAULT_BRANCH...HEAD" -- go.mod go.sum); then
    describe fail "git diff origin/$TYCI_DEFAULT_BRANCH...HEAD failed (the ref may be missing)" \
        "the default branch was not fetched, or the worktree is broken" \
        "run 'git fetch origin' in the worktree; when git works again, return ok so the step runs again"
    echo fail
    exit 0
fi

{
    echo "The dependency update changes go.mod and go.sum. The diff is below."
    echo
    echo '```diff'
    printf '%s\n' "$diff" | sed -n '1,300p' | redact
    echo '```'
} >"$run/ask-table.md"
echo ok
