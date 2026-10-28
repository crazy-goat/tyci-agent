#!/usr/bin/env bash
# rebalance_validate.sh: checks the split of the oracle of a rebalance run (state validate, #208).
# Every candidate issue must be assigned to exactly one milestone. A milestone in assign must exist
# in milestones.json or be listed in create, and a title in create must not exist yet. Titles have
# the form vX.Y.Z. The split is never trusted: a rule that fails stops the run from applying it.
#
# Env in:  TYCI_REPO, TYCI_RUN_DIR.
# Input:   $TYCI_RUN_DIR/candidates.json and milestones.json (rebalance_collect.sh), and the answer of
#          the split step, the newest $TYCI_RUN_DIR/artifacts/*-split/output.txt:
#          {"assign":{"vX.Y.Z":[N,...]},"create":["vX.Y.Z",...]}. A code fence line in the answer is ignored.
# Writes:  on ok, $TYCI_RUN_DIR/proposal.json (the validated copy, read by rebalance_apply.sh) and
#          ask-table.md (a table with every candidate issue once, shown in the ask message). On invalid,
#          validate.err with the reason, and ask-table.md with the reason too (the ask message shows it when
#          the max_visits pause ends the run). proposal.json is removed when a split is invalid, so a stale proposal is never applied.
# Keys:    ok       the split is valid and the proposal is written
#          invalid  the split breaks a rule; validate.err has the reason (the split step runs again)
#          fail     an input is missing or not JSON, or the answer has the wrong form
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi

run="${TYCI_RUN_DIR:-}"
if [ -z "$run" ] || [ -z "${TYCI_REPO:-}" ]; then
    describe fail "TYCI_RUN_DIR or TYCI_REPO is not set" \
        "the run was started without a run dir or repository" \
        "return failed; the rebalance run cannot validate the split"
    echo fail
    exit 0
fi

shopt -s nullglob
answers=("$run"/artifacts/*-split/output.txt)
shopt -u nullglob
if [ "${#answers[@]}" -eq 0 ]; then
    describe fail "the split step left no answer (output.txt)" \
        "the oracle did not finish the split step" \
        "return failed; run rebalance again to propose a split"
    echo fail
    exit 0
fi
answer="${answers[$((${#answers[@]} - 1))]}"

plan=$(sed '/^[[:space:]]*```/d' "$answer")
if ! jq -e '(.assign | type == "object") and (.create | type == "array")
    and ([.assign[]] | all(type == "array" and all(type == "number")))
    and (.create | all(type == "string"))' <<<"$plan" >/dev/null 2>&1; then
    describe fail "the split answer is not {\"assign\":{\"vX.Y.Z\":[N]},\"create\":[\"vX.Y.Z\"]}" \
        "the oracle answered in another form" \
        "return failed; run rebalance again to propose a split"
    echo fail
    exit 0
fi

if ! cands=$(jq -c '[.[].number]' "$run/candidates.json" 2>/dev/null) || ! miles=$(jq -c '[.[].title]' "$run/milestones.json" 2>/dev/null); then
    describe fail "candidates.json or milestones.json is missing or not JSON" \
        "rebalance_collect.sh did not finish" \
        "return failed; run rebalance again to collect the issues"
    echo fail
    exit 0
fi

# The rules. The filter prints the first broken rule, or nothing when the split is valid.
problem=$(jq -r --argjson c "$cands" --argjson m "$miles" '
    def title: test("^v[0-9]+[.][0-9]+[.][0-9]+$");
    . as $p
    | ([$p.assign | to_entries[] | .key as $t | .value[] | {n: ., t: $t}]) as $pairs
    | ([$pairs[].n]) as $nums
    | if ([($p.assign | keys[]), $p.create[]] | any(title | not)) then "a milestone title is not of the form vX.Y.Z"
      elif ($p.create | unique | length) != ($p.create | length) then "a title is listed twice in create"
      elif any($p.create[]; . as $t | any($m[]; . == $t)) then "a title in create already exists"
      elif any($p.assign | keys[]; . as $t | (any($m[]; . == $t) | not) and (any($p.create[]; . == $t) | not)) then "a milestone in assign does not exist and is not in create"
      elif ($nums | unique | length) != ($nums | length) then "an issue number is assigned to more than one milestone"
      elif any($nums[]; . as $n | any($c[]; . == $n) | not) then "an assigned issue number is not a candidate"
      elif any($c[]; . as $n | any($nums[]; . == $n) | not) then "a candidate issue is not assigned to a milestone"
      else "" end
    ' <<<"$plan")

if [ -n "$problem" ]; then
    rm -f "$run/proposal.json"
    printf '%s\n' "$problem" >"$run/validate.err"
    {
        echo "The proposal is not valid. The last split broke this rule:"
        echo
        head -c 500 "$run/validate.err"
    } >"$run/ask-table.md"
    describe invalid "the split is not valid: $problem" \
        "the oracle broke one rule of the split" \
        "return invalid; the split step runs again and reads validate.err"
    echo invalid
    exit 0
fi

jq -c '{assign: .assign, create: .create}' <<<"$plan" >"$run/proposal.json"
{
    echo "| Issue | Title | Milestone |"
    echo "| --- | --- | --- |"
    jq -r --argjson p "$(cat "$run/proposal.json")" '
        ([$p.assign | to_entries[] | .key as $t | .value[] | {key: tostring, value: $t}] | from_entries) as $where
        | sort_by(.number)[]
        | (.title | gsub("[\r\n]"; " ") | gsub("[|]"; "\\|")) as $title
        | ($where[(.number | tostring)]) as $t
        | "| #\(.number) | \($title) | \($t)\(if any($p.create[]; . == $t) then " (new)" else "" end) |"
        ' "$run/candidates.json"
} >"$run/ask-table.md"
rm -f "$run/validate.err"
echo ok
