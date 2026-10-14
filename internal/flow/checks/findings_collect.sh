#!/usr/bin/env bash
# findings_collect.sh: collects the findings of a milestone-close run (state collect, #206).
# A finding is an open issue without a milestone whose author has write or admin permission.
# Issue text is untrusted data: it is written to candidates.json and never run as a command.
#
# Env in:  TYCI_REPO, TYCI_RUN_DIR, optional TYCI_EXCLUDE_LABEL (issues with this label are skipped).
# Writes:  $TYCI_RUN_DIR/candidates.json, an array of {number, title, body}. The body is cut to 4000 characters.
# Keys:    found  candidates.json has at least one finding
#          none   no finding
#          fail   the run has no run dir or repo, or a gh failure
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi
# shellcheck disable=SC1091 # sibling file; shellcheck checks it on its own
. "$(dirname "${BASH_SOURCE[0]}")/lib_issues.sh"

run="${TYCI_RUN_DIR:-}"
if [ -z "$run" ] || [ -z "${TYCI_REPO:-}" ]; then
    describe fail "TYCI_RUN_DIR or TYCI_REPO is not set" \
        "the run was started without a run dir or repository" \
        "return failed; the milestone-close run cannot collect findings"
    echo fail
    exit 0
fi

if ! cands=$(issues_candidates); then
    describe fail "gh could not list the open issues without a milestone of $TYCI_REPO, or read an author permission" \
        "a GitHub outage, a rate limit, or gh is not logged in" \
        "check 'gh auth status', then return ok so the step runs again"
    echo fail
    exit 0
fi

printf '%s\n' "$cands" >"$run/candidates.json"
if [ "$(jq 'length' <<<"$cands")" -eq 0 ]; then
    echo none
else
    echo found
fi
