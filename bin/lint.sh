#!/usr/bin/env bash
# Run all static analysis, linters and formatter checks. --fix applies fixes first.
set -uo pipefail
cd "$(dirname "$0")/.." || exit 1

FIX=0
[ "${1:-}" = "--fix" ] && FIX=1
failed=()

step() {
    local name="$1"; shift
    echo "==> $name"
    "$@" || failed+=("$name")
}

if [ "$FIX" = 1 ]; then
    golangci-lint fmt
fi

step "gofmt" golangci-lint fmt --diff
step "golangci-lint" golangci-lint run
step "go vet" go vet ./...
step "shellcheck" bash -c 'git ls-files -z "*.sh" | xargs -0 -r shellcheck'

if [ "${#failed[@]}" -gt 0 ]; then
    echo "Failed: ${failed[*]}" >&2
    exit 1
fi
echo "All checks passed."
