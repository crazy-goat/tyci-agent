#!/usr/bin/env bash
# rebalance_collect.sh: collects the input of a rebalance run (state collect, #208).
# A candidate is an open issue without milestone whose author has write or admin permission
# (the same rule as findings_collect.sh). Issue text is untrusted data: it is written to JSON and
# never run as a command.
#
# Env in:  TYCI_REPO, TYCI_RUN_DIR.
# Writes:  $TYCI_RUN_DIR/candidates.json, an array of {number, title, body}. The body is cut to 4000 characters.
#          $TYCI_RUN_DIR/milestones.json, an array of {title} for the open milestones.
# Keys:    found  candidates.json has at least one candidate
#          none   no candidate (the run ends)
#          fail   the run has no run dir or repo, or a gh failure
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi
# lib_forge.sh selects gh or `tyci forge` (GitLab). A copy of this script without it works only for GitHub.
forge_lib="$(dirname "${BASH_SOURCE[0]}")/lib_forge.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$forge_lib" ]; then . "$forge_lib"; else is_gitlab() { [ "${TYCI_FORGE:-github}" != gitlab ] && return 1; echo "$(basename "$0"): lib_forge.sh is missing" >&2; exit 1; }; github_only() { is_gitlab || :; }; fi
github_only fail
# shellcheck disable=SC1091 # sibling file; shellcheck checks it on its own
. "$(dirname "${BASH_SOURCE[0]}")/lib_issues.sh"

run="${TYCI_RUN_DIR:-}"
if [ -z "$run" ] || [ -z "${TYCI_REPO:-}" ]; then
    describe fail "TYCI_RUN_DIR or TYCI_REPO is not set" \
        "the run was started without a run dir or repository" \
        "return failed; the rebalance run cannot collect the issues"
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

if ! miles=$(gh api --paginate "repos/$TYCI_REPO/milestones?state=open&per_page=100" | jq -s -c '[.[][] | {title: (.title // "")}]'); then
    describe fail "gh could not list the open milestones of $TYCI_REPO" \
        "a GitHub outage, a rate limit, or gh is not logged in" \
        "check 'gh auth status', then return ok so the step runs again"
    echo fail
    exit 0
fi

printf '%s\n' "$cands" >"$run/candidates.json"
printf '%s\n' "$miles" >"$run/milestones.json"
if [ "$(jq 'length' <<<"$cands")" -eq 0 ]; then
    echo none
else
    echo found
fi
