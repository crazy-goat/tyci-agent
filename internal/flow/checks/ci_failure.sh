#!/usr/bin/env bash
# ci_failure.sh: reads the state of the default branch and keeps the failed log of its latest run
# (state ci_failure, fix-ci workflow, #209). Read-only on GitHub and in git.
# The newest completed run of the CI workflow (TYCI_CI_WORKFLOW, default tests.yaml: the workflow
# that holds the ci-ok check) on TYCI_DEFAULT_BRANCH decides: success gives green, any other
# conclusion gives red. Cancelled, skipped and neutral runs are ignored, so the next run decides. On red the last 300 lines of 'gh run view --log-failed' go through redact
# (lib_redact.sh) into $TYCI_ARTIFACT_DIR/failure.log. The log is data for the coder: it is never run.
# A green default branch writes no failure.log.
#
# Env in:  TYCI_REPO, TYCI_DEFAULT_BRANCH, TYCI_ARTIFACT_DIR, TYCI_CI_WORKFLOW (optional).
# Keys:    red    the latest run failed; failure.log has its failed job log
#          green  the latest run succeeded; no failure.log
#          fail   gh failed, or the default branch has no completed run
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
. "$(dirname "${BASH_SOURCE[0]}")/lib_redact.sh"

out="${TYCI_ARTIFACT_DIR:-}"
if [ -z "$out" ] || [ -z "${TYCI_REPO:-}" ] || [ -z "${TYCI_DEFAULT_BRANCH:-}" ]; then
    describe fail "TYCI_ARTIFACT_DIR, TYCI_REPO or TYCI_DEFAULT_BRANCH is not set" \
        "the run was started without an artifact dir, a repository or a default branch" \
        "return failed; the fix-ci run cannot read the state of the default branch"
    echo fail
    exit 0
fi

ci_workflow="${TYCI_CI_WORKFLOW:-tests.yaml}"
if ! runs=$(gh run list -R "$TYCI_REPO" --workflow "$ci_workflow" --branch "$TYCI_DEFAULT_BRANCH" --limit 20 --json databaseId,conclusion,status 2>/dev/null); then
    describe fail "gh could not list the runs of $ci_workflow on $TYCI_DEFAULT_BRANCH in $TYCI_REPO" \
        "no network, gh is not logged in, a rate limit, or a GitHub outage" \
        "run 'gh auth status' and 'gh run list --workflow $ci_workflow --branch $TYCI_DEFAULT_BRANCH'; when gh works again, return ok so the step runs again"
    echo fail
    exit 0
fi

# gh lists the newest run first. A run that is still in progress is skipped.
latest=$(jq -c '[.[] | select(.status == "completed" and (.conclusion | IN("cancelled", "skipped", "neutral") | not))][0] // empty' <<<"$runs")
if [ -z "$latest" ]; then
    describe fail "$ci_workflow on $TYCI_DEFAULT_BRANCH of $TYCI_REPO has no completed run" \
        "the repository has no CI run on its default branch yet, or all runs are still in progress" \
        "return failed; a human checks the default branch"
    echo fail
    exit 0
fi

conclusion=$(jq -r '.conclusion // ""' <<<"$latest")
if [ "$conclusion" = success ]; then
    rm -f "$out/failure.log"
    echo green
    exit 0
fi

id=$(jq -r '.databaseId' <<<"$latest")
if ! gh run view "$id" -R "$TYCI_REPO" --log-failed 2>/dev/null | tail -n 300 | redact >"$out/failure.log"; then
    describe fail "gh run view $id --log-failed failed, so there is no failure log of the red run" \
        "no network, gh is not logged in, or the log has expired" \
        "run 'gh run view $id -R $TYCI_REPO --log-failed'; when gh works again, return ok so the step runs again"
    echo fail
    exit 0
fi
echo red
