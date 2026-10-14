#!/usr/bin/env bash
# rebalance_apply.sh: creates the new milestones and assigns the issues of a validated split (state apply, #208).
# It reads only proposal.json, the copy that rebalance_validate.sh wrote, never the answer of the split step.
# It is idempotent: a milestone or an issue that an earlier run wrote is skipped. Every write is
# appended to $TYCI_RUN_DIR/applied.txt, one line each: "milestone <title>" or "issue <number>".
# A milestone in milestones.json (the open ones, rebalance_collect.sh) is not created again.
# A failed gh call stops the script (describe prints the block); the writes made so far are in applied.txt.
#
# Env in:  TYCI_REPO, TYCI_RUN_DIR.
# Input:   $TYCI_RUN_DIR/proposal.json and milestones.json.
# Keys:    done  every milestone of the proposal exists and every issue is assigned
#          fail  the run has no run dir or repo, or proposal.json is missing or not valid
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

run="${TYCI_RUN_DIR:-}"
if [ -z "$run" ] || [ -z "${TYCI_REPO:-}" ]; then
    describe fail "TYCI_RUN_DIR or TYCI_REPO is not set" \
        "the run was started without a run dir or repository" \
        "return failed; the rebalance run cannot apply the split"
    echo fail
    exit 0
fi

if ! jq -e '(.assign | type == "object") and (.create | type == "array")' "$run/proposal.json" >/dev/null 2>&1; then
    describe fail "proposal.json is missing or not valid: the split was not validated" \
        "the validate step did not finish with ok" \
        "return failed; run rebalance again to propose and validate a split"
    echo fail
    exit 0
fi

touch "$run/applied.txt"

# written <line>: true when the line is already in applied.txt.
written() {
    grep -qxF "$1" "$run/applied.txt"
}

while IFS= read -r title; do
    [ -n "$title" ] || continue
    if written "milestone $title" || jq -e --arg t "$title" 'any(.[]; .title == $t)' "$run/milestones.json" >/dev/null; then
        continue
    fi
    gh api -X POST "repos/$TYCI_REPO/milestones" -f "title=$title" >/dev/null
    printf 'milestone %s\n' "$title" >>"$run/applied.txt"
done < <(jq -r '.create[]' "$run/proposal.json")

while IFS=$'\t' read -r number title; do
    [ -n "$number" ] || continue
    if written "issue $number"; then
        continue
    fi
    gh issue edit "$number" -R "$TYCI_REPO" --milestone "$title" >/dev/null
    printf 'issue %s\n' "$number" >>"$run/applied.txt"
done < <(jq -r '.assign | to_entries[] | .key as $t | .value[] | "\(.)\t\($t)"' "$run/proposal.json")

echo "done"
