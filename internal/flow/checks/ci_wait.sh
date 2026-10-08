#!/usr/bin/env bash
# ci_wait.sh: waits until the required check `ci-ok` of a pull request is conclusive.
# Read-only, idempotent, safe to re-run after a restart. Other checks never decide.
#
# Env in:  TYCI_PR (required), TYCI_REPO, TYCI_CI_POLL_SEC (default 20),
#          TYCI_CI_APPEAR_SEC (default 300), TYCI_ARTIFACT_DIR (optional).
# Keys:    green  ci-ok passed
#          red    ci-ok failed or was cancelled
#          conflict  the PR conflicts with the default branch (no CI runs for it)
#          behind    ci-ok is missing and the PR is behind the default branch
#          fail   no PR number, ci-ok never appeared, or 3 consecutive gh failures
# The loop is otherwise unbounded: the state timeout_sec stops it. Progress goes to stderr.
# On red, the failed job log of the ci-ok workflow run (gh run view --log-failed) goes to
# $TYCI_ARTIFACT_DIR/ci-failed.log. A failure there never changes the key.
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

# save_failed_log <checks json>: best effort, never fails.
save_failed_log() {
    [ -n "${TYCI_ARTIFACT_DIR:-}" ] || return 0
    local link run_id
    link=$(jq -r '[.[] | select(.name == "ci-ok")][0].link // ""' <<<"$1" 2>/dev/null || true)
    run_id=$(sed -n 's#.*/actions/runs/\([0-9][0-9]*\).*#\1#p' <<<"$link")
    if [ -z "$run_id" ]; then
        echo "ci_wait.sh: no workflow run id in the ci-ok link" >&2
        return 0
    fi
    if ! gh run view "$run_id" -R "${TYCI_REPO:-}" --log-failed >"$TYCI_ARTIFACT_DIR/ci-failed.log" 2>&1; then
        echo "ci_wait.sh: gh run view $run_id --log-failed failed" >&2
    fi
    return 0
}

pr="${TYCI_PR:-}"
if [ -z "$pr" ]; then
    # The PR head branch: open_pr.sh may have continued a PR from another branch (#374).
    pr_branch="${TYCI_BRANCH:-<branch>}"
    if [ -s "${TYCI_RUN_DIR:-}/pr_branch" ]; then pr_branch="$(<"$TYCI_RUN_DIR/pr_branch")"; fi
    describe fail "TYCI_PR is not set: the run has no PR number, so there is no CI to wait for" \
        "push.sh did not write \$TYCI_RUN_DIR/pr, or the run state lost the PR" \
        "run 'gh pr list --head $pr_branch --state open'; if a PR exists, return failed with its number (the oracle sends the run back to update); if none exists, return failed"
    echo fail
    exit 0
fi
poll="${TYCI_CI_POLL_SEC:-20}"
appear="${TYCI_CI_APPEAR_SEC:-300}"

errf=$(mktemp)
trap 'rm -f "$errf"' EXIT
errors=0
start=$SECONDS
while :; do
    view=$(gh pr view "$pr" -R "${TYCI_REPO:-}" --json mergeable,mergeStateStatus --jq '.mergeable + " " + .mergeStateStatus' 2>/dev/null || true)
    case "$view" in
    CONFLICTING* | *DIRTY)
        describe conflict "PR #$pr conflicts with the default branch (GitHub state: $view); CI does not run for it" \
            "the default branch changed the same lines as this PR" \
            "the run goes to rebase: it merges origin/${TYCI_DEFAULT_BRANCH:-<default>} and a coder resolves the conflicts"
        echo conflict
        exit 0
        ;;
    esac
    out=$(gh pr checks "$pr" -R "${TYCI_REPO:-}" --json name,state,bucket,link 2>"$errf" || true)
    # gh exits 1 with this text when no check exists yet: that is "missing", not an API error.
    case "$(cat "$errf")" in *"no checks reported"*) out='[]' ;; esac
    if bucket=$(jq -er 'if type == "array" then [.[] | select(.name == "ci-ok")][0].bucket // "missing" else empty end' <<<"$out" 2>/dev/null); then
        errors=0
        case "$bucket" in
        pass)
            echo green
            exit 0
            ;;
        fail | cancel)
            save_failed_log "$out"
            describe red "the required check ci-ok of PR #$pr is $bucket" \
                "a test, lint or build job failed (or the run was cancelled)" \
                "read ${TYCI_ARTIFACT_DIR:-the artifact dir}/ci-failed.log (the failed job log), fix the code, run the checks from AGENTS.md and commit"
            echo red
            exit 0
            ;;
        esac
        echo "ci_wait.sh: ci-ok is $bucket" >&2
        if [ "$bucket" = missing ] && [ "${view##* }" = BEHIND ]; then
            describe behind "PR #$pr has no ci-ok check and is behind the default branch" \
                "the ruleset needs an up-to-date branch before CI counts" \
                "the run goes to rebase: it merges origin/${TYCI_DEFAULT_BRANCH:-<default>} and pushes, then CI runs again"
            echo behind
            exit 0
        fi
        if [ "$bucket" = missing ] && [ $((SECONDS - start)) -ge "$appear" ]; then
            describe fail "the check ci-ok of PR #$pr did not appear within ${appear}s" \
                "the workflow did not start: the head commit was not pushed, a workflow needs approval, or GitHub Actions is slow or down" \
                "run 'gh pr checks $pr' and 'gh run list --branch ${TYCI_BRANCH:-<branch>}'; if a run is queued or was just started again ('gh run rerun'), return ok so the step waits again; else return failed"
            echo fail
            exit 0
        fi
    else
        errors=$((errors + 1))
        echo "ci_wait.sh: gh failure $errors: $(tail -n 1 "$errf")" >&2
        if [ "$errors" -ge 3 ]; then
            describe fail "'gh pr checks $pr' failed 3 times in a row (last error: $(tail -n 1 "$errf"))" \
                "no network, gh is not logged in, a rate limit, or a GitHub outage" \
                "run 'gh auth status' and 'gh pr checks $pr'; when gh works again, return ok so the step waits again"
            echo fail
            exit 0
        fi
    fi
    sleep "$poll"
done
