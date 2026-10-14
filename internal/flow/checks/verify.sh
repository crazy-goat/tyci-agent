#!/usr/bin/env bash
# verify.sh: runs 'go build ./...' and 'go test ./...' in the run worktree (states verify of the
# fix-ci and dependency-bump workflows, #209). The output of both goes through redact (lib_redact.sh)
# into $TYCI_ARTIFACT_DIR/verify.log, so an agent that reads the log never sees a raw secret.
# The script never edits code and never commits.
#
# Env in:  TYCI_ARTIFACT_DIR. cwd = the run worktree.
# Keys:    green  build and tests passed
#          red    build or tests failed; verify.log has the output
#          fail   the run has no artifact dir
set -euo pipefail
# describe.sh prints the failure block (see there). A copy of this script without it still works.
describe_lib="$(dirname "${BASH_SOURCE[0]}")/describe.sh"
# shellcheck disable=SC1090 # sibling file; shellcheck checks it on its own
if [ -f "$describe_lib" ]; then . "$describe_lib"; else describe() { echo "$(basename "$0"): $2" >&2; }; fi
# shellcheck disable=SC1091 # sibling file; shellcheck checks it on its own
. "$(dirname "${BASH_SOURCE[0]}")/lib_redact.sh"

out="${TYCI_ARTIFACT_DIR:-}"
if [ -z "$out" ]; then
    describe fail "TYCI_ARTIFACT_DIR is not set" \
        "the run was started without an artifact dir" \
        "return failed; the run cannot keep the verify log"
    echo fail
    exit 0
fi

# The pipeline status is the status of the first failing go command (pipefail).
if { go build ./... && go test ./...; } 2>&1 | redact >"$out/verify.log"; then
    echo green
else
    echo red
fi
