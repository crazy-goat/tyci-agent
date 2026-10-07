#!/usr/bin/env bash
# post_review.sh: posts the newest review report as one PR review (state post_review).
# The reviewer agent stays read-only; this script does the only write to GitHub.
# Every post starts with the marker line that fetch_comments.sh uses to
# recognise the comments of the agent itself.
#
# Env in:  TYCI_PR, TYCI_REPO, TYCI_RUN_DIR. The report is report.md in the
#          newest $TYCI_RUN_DIR/artifacts/NNN-review dir (highest NNN).
# Keys:    ok    the review was posted
#          fail  no PR, no review report, or gh failed (the runner records it, the run goes on)
# Always exits 0.
set -euo pipefail

pr="${TYCI_PR:-}"
review=""
best=-1
for f in "${TYCI_RUN_DIR:-}"/artifacts/*-review/report.md; do
    [ -e "$f" ] || continue
    n=$(basename "$(dirname "$f")")
    n=$((10#${n%%-*}))
    if [ "$n" -gt "$best" ]; then
        best=$n
        review=$f
    fi
done
if [ -z "$pr" ] || [ -z "$review" ] || [ ! -s "$review" ]; then
    echo "post_review.sh: no PR number or no review report" >&2
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
    echo fail
fi
