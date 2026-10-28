#!/usr/bin/env bash
# merge_approved.sh: the human-approved merge of a pull request that touches protected paths.
#
# It exists only as the target of the "approve" answer at the ask_protected pause. It sets
# TYCI_ALLOW_PROTECTED=1 and runs merge.sh, so the merge keeps every other rule (ci-ok pass,
# head match, squash) and only the protected-path stop is lifted, once, by a human.
#
# Keys: whatever merge.sh prints (merged, behind, protected, fail).
set -euo pipefail

dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [ ! -f "$dir/merge.sh" ]; then
    echo "merge_approved.sh: $dir/merge.sh is missing" >&2
    echo fail
    exit 0
fi

TYCI_ALLOW_PROTECTED=1 exec bash "$dir/merge.sh"
