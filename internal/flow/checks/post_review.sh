#!/usr/bin/env bash
# post_review.sh: posts the final review.md as one PR review (state post_review).
# The reviewer agent stays read-only; this script does the only write to GitHub.
# Every post starts with the marker line that fetch_comments.sh uses to
# recognise the comments of the agent itself.
#
# Env in:  TYCI_PR, TYCI_REPO, TYCI_RUN_DIR (review.md lives there).
# Keys:    ok    the review was posted
#          fail  no PR, no review.md, or gh failed (the runner records it, the run goes on)
# Always exits 0.
set -euo pipefail

pr="${TYCI_PR:-}"
review="${TYCI_RUN_DIR:-}/review.md"
if [ -z "$pr" ] || [ ! -s "$review" ]; then
    echo "post_review.sh: no PR number or no review.md" >&2
    echo fail
    exit 0
fi

# The worker has handled the comments by now; do not hand them out again.
rm -f "${TYCI_RUN_DIR:-}/comments.md"

body=$(mktemp)
trap 'rm -f "$body"' EXIT
{
    echo "<!-- tyci-agent -->"
    cat "$review"
} >"$body"

if gh pr review "$pr" -R "${TYCI_REPO:-}" --comment --body-file "$body" >&2; then
    echo ok
else
    echo fail
fi
