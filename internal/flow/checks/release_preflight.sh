#!/usr/bin/env bash
# release_preflight.sh: checks that the milestone can be released (state preflight, #207).
# It is read-only: it changes nothing on GitHub or in git. The milestone is ready when all four hold:
#   - the milestone has no open issue,
#   - ci-ok of the default branch is success,
#   - no tag with the milestone title exists on origin,
#   - [Unreleased] in CHANGELOG.md has at least one entry ("- ").
#
# Env in:  TYCI_REPO, TYCI_DEFAULT_BRANCH, TYCI_PARAM_MILESTONE (the milestone title, for example v0.10.0).
#          cwd = the run worktree (CHANGELOG.md is read from it).
# Keys:    ready  all four checks pass
#          fail   one check fails (a describe.sh block says which one)
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

milestone="${TYCI_PARAM_MILESTONE:-}"
if [ -z "$milestone" ] || [ -z "${TYCI_REPO:-}" ] || [ -z "${TYCI_DEFAULT_BRANCH:-}" ]; then
    describe fail "TYCI_PARAM_MILESTONE, TYCI_REPO or TYCI_DEFAULT_BRANCH is not set" \
        "the run was started without its milestone" \
        "return failed; the release run cannot check the milestone"
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
    stop "gh api milestones failed (see the gh output above); nothing was released" \
        "no network, gh is not logged in, or a temporary GitHub problem" \
        "run 'gh auth status'; return failed and start the release again when gh works"
open=$(jq -r --arg t "$milestone" '.[] | select(.title == $t) | .open_issues' <<<"$ms")
if [ -z "$open" ]; then
    stop "milestone $milestone was not found in $TYCI_REPO" \
        "the milestone title is wrong or the milestone was deleted" \
        "return failed; start the release with the title of an existing milestone"
fi
if [ "$open" != 0 ]; then
    stop "milestone $milestone has $open open issue(s)" \
        "not all issues of the milestone are closed or moved" \
        "close or move the open issues, then start the release again"
fi

ci=$(gh api "repos/$TYCI_REPO/commits/$TYCI_DEFAULT_BRANCH/check-runs?per_page=100" \
    --jq '[.check_runs[] | select(.name == "ci-ok")] | sort_by(.started_at) | last | .conclusion // ""') ||
    stop "gh api check-runs of $TYCI_DEFAULT_BRANCH failed (see the gh output above)" \
        "no network, gh is not logged in, or a temporary GitHub problem" \
        "run 'gh auth status'; return failed and start the release again when gh works"
if [ "$ci" != success ]; then
    stop "ci-ok of $TYCI_DEFAULT_BRANCH is '${ci:-missing}', not success" \
        "the default branch has no green CI run" \
        "wait until ci-ok of $TYCI_DEFAULT_BRANCH is green, then start the release again"
fi

tags=$(git ls-remote --tags origin "refs/tags/$milestone") ||
    stop "git ls-remote --tags origin failed (see the git output above)" \
        "no network or git is not authenticated" \
        "fix the connection to origin, then start the release again"
if [ -n "$tags" ]; then
    stop "tag $milestone already exists on origin" \
        "an earlier release used this version" \
        "choose another version: rename the milestone, then start the release again"
fi

entries=$(awk '/^## \[Unreleased\]/ { in_section = 1; next } /^## \[/ { in_section = 0 }
    in_section && /^- / { n++ } END { print n + 0 }' CHANGELOG.md) ||
    stop "CHANGELOG.md cannot be read in the run worktree" \
        "the file is missing or the run has no worktree" \
        "return failed; check that CHANGELOG.md exists on the default branch"
if [ "$entries" -eq 0 ]; then
    stop "[Unreleased] in CHANGELOG.md has no entry" \
        "the changes of the milestone are not written down" \
        "add the entries under [Unreleased], then start the release again"
fi

echo ready
