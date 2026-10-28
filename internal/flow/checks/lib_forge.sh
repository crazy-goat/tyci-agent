#!/usr/bin/env bash
# lib_forge.sh: sourced by the check scripts that call the forge (it is not a state of its own).
# For a GitLab repo (repos in ~/.tyci/config.json) the runner sets TYCI_FORGE=gitlab and
# TYCI_BIN (the tyci binary). The scripts then call the hidden `tyci forge` command, never gh.
# A `tyci forge` result has the JSON shape of the gh --json fields, so the jq code of a script
# is the same for both forges. Source describe.sh first.
#
#   is_gitlab           true when the repo is on GitLab
#   forge_op OP ARGS    runs `tyci forge --repo $TYCI_REPO OP ARGS`
#   github_only KEY     on GitLab: prints a describe.sh block and KEY, then exits 0

is_gitlab() { [ "${TYCI_FORGE:-github}" = gitlab ]; }

forge_op() { "${TYCI_BIN:?TYCI_BIN is not set}" forge --repo "${TYCI_REPO:?TYCI_REPO is not set}" "$@"; }

github_only() {
    is_gitlab || return 0
    describe "$1" "$(basename "$0") works only for GitHub repos; ${TYCI_REPO:-this repo} is on GitLab" \
        "the workflow has a step that tyci supports only on GitHub" \
        "nothing to fix in the worktree; return failed (a human does this step by hand)"
    echo "$1"
    exit 0
}
