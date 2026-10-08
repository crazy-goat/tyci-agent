#!/usr/bin/env bash
# describe.sh: sourced by every check script (it is not a state of its own).
# On a non-success key, a check script calls `describe` before it prints the key. describe writes
# one fixed-format block to stderr, so the block lands in the step's output.log. The fixer agent
# (and a human) reads it and knows what failed, the state of the branches and what to do next:
#
#   RESULT: fail
#   STEP: update
#   WHAT: git push to origin/issue-527 was rejected (non-fast-forward)
#   STATE: local issue-527 = 905f169; origin/main = 1f383ad; origin/issue-527 = 8f1ee26; PR #677; worktree clean
#   LIKELY CAUSE: the branch already has commits from an earlier run
#   SUGGESTED: fetch origin/issue-527, reset the worktree branch to it, merge origin/main, push; then return ok
#
# Plain text, fixed field names, one line per field. Never put a token in it.
# Sourcing also sets an ERR trap: when a command fails and set -e stops the script without a
# key, the trap prints the same block with RESULT: error and the failed command.

# describe <result> <what> <likely cause> <suggested> [extra state]
describe() {
    {
        echo
        echo "RESULT: $1"
        echo "STEP: ${TYCI_STATE:-$(basename "$0" .sh)}"
        echo "WHAT: $2"
        echo "STATE: $(describe_state)${5:+; $5}"
        echo "LIKELY CAUSE: $3"
        echo "SUGGESTED: $4"
    } >&2
}

# describe_state prints the branch facts from local refs (as of the last fetch). Best effort.
describe_state() {
    local b="${TYCI_BRANCH:-}" d="${TYCI_DEFAULT_BRANCH:-}" pb="" s="" clean
    # After open_pr.sh continued a PR from another branch, the push goes to that PR head (#374).
    pb="$b"
    if [ -s "${TYCI_RUN_DIR:-}/pr_branch" ]; then pb="$(<"$TYCI_RUN_DIR/pr_branch")"; fi
    if git rev-parse --git-dir >/dev/null 2>&1; then
        s="local ${b:-HEAD} = $(describe_rev HEAD)"
        [ -z "$d" ] || s+="; origin/$d = $(describe_rev "refs/remotes/origin/$d")"
        [ -z "$pb" ] || s+="; origin/$pb = $(describe_rev "refs/remotes/origin/$pb")"
        clean=clean
        [ -z "$(git status --porcelain 2>/dev/null)" ] || clean="has uncommitted changes"
        s+="; worktree $clean"
    fi
    [ -z "${TYCI_PR:-}" ] || s+="${s:+; }PR #$TYCI_PR"
    printf '%s' "$s"
}

describe_rev() {
    git rev-parse -q --short --verify "$1^{commit}" 2>/dev/null || echo missing
}

# describe_error is the ERR trap. Command substitutions and pipelines run in subshells and would
# print the block twice, so only the main shell prints it.
describe_error() {
    [ "$BASH_SUBSHELL" -eq 0 ] || return 0
    describe error "the command '$2' failed with exit code $1 (line $3 of $(basename "$0")); the script stopped without a key" \
        "a gh or git call failed: no network, gh is not logged in, a rate limit, or a missing branch or PR (see the output above)" \
        "read the error output above, fix its cause (for example 'gh auth status', fetch again), then return ok so the step runs again"
}

set -E
trap 'describe_error $? "$BASH_COMMAND" "$LINENO"' ERR
