#!/usr/bin/env bash
# release_wait.sh: waits for the release workflow of the tag and checks the GitHub release (state wait, #207).
# The run of .github/workflows/release.yaml for the tag (its headBranch is the tag name) must succeed.
# The release then needs at least one tyci_* asset and checksums.txt. The script polls until the run
# completes. A run that is still going when TYCI_RELEASE_TIMEOUT seconds have passed is a failure.
#
# Env in:  TYCI_REPO, TYCI_PARAM_MILESTONE (the tag name, vX.Y.Z), optional TYCI_RELEASE_TIMEOUT
#          (default 1800) and TYCI_RELEASE_POLL (default 15 seconds between two polls).
# Keys:    ok      the run succeeded and the release has the binaries and checksums.txt
#          failed  the run failed, a required asset is missing, or the timeout passed
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

tag="${TYCI_PARAM_MILESTONE:-}"
if [ -z "$tag" ] || [ -z "${TYCI_REPO:-}" ]; then
    describe fail "the milestone param or TYCI_REPO is not set" \
        "the run was started without its milestone" \
        "return failed; the release run cannot find the release workflow"
    echo failed
    exit 0
fi

# stop <what> <likely cause> <suggested>: prints the failure block and the key, then ends the script.
stop() {
    describe fail "$@"
    echo failed
    exit 0
}

timeout="${TYCI_RELEASE_TIMEOUT:-1800}"
poll="${TYCI_RELEASE_POLL:-15}"
deadline=$(($(date +%s) + timeout))

while :; do
    # A failed gh call is retried until the timeout, so a short network problem does not end the run.
    if ! runs=$(gh run list -R "$TYCI_REPO" --workflow release.yaml --json headBranch,status,conclusion --limit 20); then
        echo "release_wait.sh: gh run list failed, retrying (see the gh output above)" >&2
        runs="[]"
    fi
    state=$(jq -r --arg t "$tag" '[.[] | select(.headBranch == $t)][0] // empty | "\(.status) \(.conclusion // "")"' <<<"$runs")
    if [ "${state%% *}" = completed ]; then
        if [ "${state#* }" != success ]; then
            stop "the release workflow for $tag ended with '${state#* }', not success" \
                "a build or upload step of release.yaml failed" \
                "run 'gh run view --log-failed' for the release run of $tag; return failed"
        fi
        assets=$(gh release view "$tag" -R "$TYCI_REPO" --json assets --jq '.assets[].name' 2>/dev/null || true)
        if ! grep -qx checksums.txt <<<"$assets" || ! grep -q '^tyci_' <<<"$assets"; then
            stop "the release $tag has no tyci_* binary or no checksums.txt" \
                "the release workflow succeeded but did not attach the assets" \
                "run 'gh release view $tag' and check the assets; return failed"
        fi
        echo ok
        exit 0
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
        stop "the release workflow for $tag did not finish within $timeout seconds" \
            "the run is slow, queued, or was never started" \
            "run 'gh run list --workflow release.yaml'; return failed and check the release by hand"
    fi
    sleep "$poll"
done
