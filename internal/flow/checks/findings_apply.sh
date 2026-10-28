#!/usr/bin/env bash
# findings_apply.sh: moves the picked findings into the milestone (state apply, #206).
# It reads the answer of the pick step and approvals.txt of the run. It moves only issues that are
# listed in candidates.json. An answer with any other issue number moves nothing (key fail).
# Critical findings move at once. Doubtful findings move only after a human answered "yes" in the
# ask state (a line of approvals.txt with state "ask" and answer "yes"). Until then the key is doubt
# and the doubtful numbers are listed on stderr, so the pause notice shows them.
# Every moved number is appended to $TYCI_RUN_DIR/moved.txt and skipped by later runs (idempotent).
#
# Env in:  TYCI_REPO, TYCI_RUN_DIR, TYCI_PARAM_MILESTONE (the milestone title), TYCI_ANSWER (optional).
# Input:   $TYCI_RUN_DIR/candidates.json (findings_collect.sh) and the answer of the pick step:
#          $TYCI_ANSWER or, when it is not set, the newest $TYCI_RUN_DIR/artifacts/*-pick/output.txt.
#          The answer is {"critical":[N,...],"doubt":[N,...]}.
#          A code fence line in the answer is ignored.
# Keys:    done   nothing doubtful is left to move
#          doubt  doubtful findings wait for a human answer (listed on stderr)
#          fail   the answer is missing or invalid, lists an issue that is not a candidate (nothing moved),
#                 or a gh call failed (the numbers moved so far are in moved.txt)
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

run="${TYCI_RUN_DIR:-}"
milestone="${TYCI_PARAM_MILESTONE:-}"
if [ -z "$run" ] || [ -z "${TYCI_REPO:-}" ] || [ -z "$milestone" ]; then
    describe fail "TYCI_RUN_DIR, TYCI_REPO or the milestone param is not set" \
        "the run was started without its milestone" \
        "return failed; the milestone-close run cannot move findings"
    echo fail
    exit 0
fi

shopt -s nullglob
answers=("$run"/artifacts/*-pick/output.txt)
shopt -u nullglob
if [ -n "${TYCI_ANSWER:-}" ]; then
    # The runner names the answer of the pick step, also when the state has another name (#784).
    answers=()
    if [ -f "$TYCI_ANSWER" ]; then answers=("$TYCI_ANSWER"); fi
fi
if [ "${#answers[@]}" -eq 0 ]; then
    describe fail "the pick step left no answer (output.txt)" \
        "the oracle did not finish the pick step" \
        "return failed; run milestone-close again to pick the findings"
    echo fail
    exit 0
fi
answer="${answers[$((${#answers[@]} - 1))]}"

plan=$(sed '/^[[:space:]]*```/d' "$answer")
if ! jq -e '(.critical | type == "array") and (.doubt | type == "array")
    and ([.critical[], .doubt[]] | all(type == "number"))' <<<"$plan" >/dev/null 2>&1; then
    describe fail "the pick answer is not {\"critical\":[N],\"doubt\":[N]} with issue numbers" \
        "the oracle answered in another form" \
        "return failed; run milestone-close again to pick the findings"
    echo fail
    exit 0
fi

cands=$(jq '[.[].number]' "$run/candidates.json")
if ! jq -e --argjson c "$cands" '[.critical[], .doubt[]] | all(. as $n | any($c[]; . == $n))' <<<"$plan" >/dev/null; then
    describe fail "the pick answer lists an issue that is not a candidate; nothing was moved" \
        "the oracle answered with an issue number that is not in candidates.json" \
        "return failed; check the pick answer in the artifacts of the pick step"
    echo fail
    exit 0
fi

# move <issue>: adds the issue to the milestone once. A failed gh call stops the script (describe prints it).
move() {
    if grep -qx "$1" "$run/moved.txt" 2>/dev/null; then
        return 0
    fi
    gh issue edit "$1" -R "$TYCI_REPO" --milestone "$milestone" >/dev/null
    printf '%s\n' "$1" >>"$run/moved.txt"
}

while IFS= read -r n; do
    move "$n"
done < <(jq -r '.critical[]' <<<"$plan")

doubt=$(jq -r '.doubt | map(tostring) | join(" #")' <<<"$plan")
if [ -n "$doubt" ]; then
    approved=false
    if [ -f "$run/approvals.txt" ] && awk -F'\t' '$2 == "ask" && $3 == "yes" { found = 1 } END { exit !found }' "$run/approvals.txt"; then
        approved=true
    fi
    if [ "$approved" != true ]; then
        describe doubt "doubtful findings wait for a human answer: #$doubt" \
            "the oracle is not sure these open issues belong to the milestone" \
            "answer yes to move them, or no to leave them in the milestone-close run"
        echo doubt
        exit 0
    fi
    while IFS= read -r n; do
        move "$n"
    done < <(jq -r '.doubt[]' <<<"$plan")
fi
printf "done\n"
