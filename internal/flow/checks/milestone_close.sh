#!/usr/bin/env bash
# milestone_close.sh: closes the milestone of the release with the GitHub API (state close, #207).
# It is idempotent: a milestone that is already closed (for example after a resumed run) gives ok
# and no second API call.
#
# Env in:  TYCI_REPO, TYCI_PARAM_MILESTONE (the milestone title, vX.Y.Z).
# Keys:    ok    the milestone is closed (now or before)
#          fail  the milestone was not found, or a gh call failed
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

milestone="${TYCI_PARAM_MILESTONE:-}"
if [ -z "$milestone" ] || [ -z "${TYCI_REPO:-}" ]; then
    describe fail "the milestone param or TYCI_REPO is not set" \
        "the run was started without its milestone" \
        "return failed; the release run cannot close the milestone"
    echo fail
    exit 0
fi

# stop <what> <likely cause> <suggested>: prints the failure block and the key, then ends the script.
stop() {
    describe fail "$@"
    echo fail
    exit 0
}

ms=$(gh api --paginate "repos/$TYCI_REPO/milestones?state=all&per_page=100") ||
    stop "gh api milestones failed (see the gh output above); the milestone is not closed" \
        "no network, gh is not logged in, or a temporary GitHub problem" \
        "run 'gh auth status'; return failed and close the milestone by hand if needed"
line=$(jq -r --arg t "$milestone" '.[] | select(.title == $t) | "\(.number) \(.state)"' <<<"$ms")
if [ -z "$line" ]; then
    stop "milestone $milestone was not found in $TYCI_REPO" \
        "the milestone title is wrong or the milestone was deleted" \
        "return failed; close the milestone by hand"
fi
number=${line% *}
state=${line#* }
if [ "$state" = closed ]; then
    echo "milestone_close.sh: milestone $milestone is already closed" >&2
    echo ok
    exit 0
fi

gh api -X PATCH "repos/$TYCI_REPO/milestones/$number" -f state=closed >/dev/null ||
    stop "gh api PATCH of milestone $milestone failed (see the gh output above)" \
        "no network, gh is not logged in, or a temporary GitHub problem" \
        "run 'gh auth status'; return failed and close milestone $milestone by hand"
echo ok
