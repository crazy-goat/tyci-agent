#!/usr/bin/env bash
# post_review.sh: posts the newest review report as one PR review (state post_review).
# The reviewer agent stays read-only; this script does the only write to GitHub.
# Every post starts with the marker line that fetch_comments.sh uses to
# recognise the comments of the agent itself.
#
# Env in:  TYCI_PR, TYCI_REPO, TYCI_REVIEW_DIR (the artifact dir of the newest review
#          step, set by the runner). The review is report.md in that dir.
# Keys:    ok    the review was posted
#          skip  the run has no review step (it continued an open PR without a new review)
#          fail  no PR, an empty review report, or gh failed (the runner records it, the run goes on)
# Always exits 0.
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

pr="${TYCI_PR:-}"
review_dir="${TYCI_REVIEW_DIR:-}"
if [ -z "$review_dir" ]; then
    echo "post_review.sh: this run has no review step (it continued an open PR of an earlier run); nothing to post" >&2
    echo skip
    exit 0
fi
review="$review_dir/report.md"
if [ -z "$pr" ] || [ ! -s "$review" ]; then
    describe fail "no PR number (TYCI_PR='$pr') or an empty review report ($review)" \
        "the reviewer wrote an empty report.md, or the run lost its PR number" \
        "nothing to do: the run records a warning and goes on to ci"
    echo fail
    exit 0
fi

body=$(mktemp)
trap 'rm -f "$body"' EXIT
{
    echo "<!-- tyci-agent -->"
    cat "$review"
} >"$body"

if gh pr review "$pr" -R "${TYCI_REPO:-}" --comment --body-file "$body" >&2; then
    echo ok
else
    describe fail "gh pr review $pr --comment failed (see the gh output above); the review was not posted" \
        "no network, gh is not logged in, or no permission to comment" \
        "nothing to do: the run records a warning and goes on to ci"
    echo fail
fi
