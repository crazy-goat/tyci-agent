# Changelog

All notable changes to this project are documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added
- `bin/lint.sh` runs `golangci-lint fmt --diff`, `golangci-lint run`, `go vet` and shellcheck; `--fix` applies `golangci-lint fmt` first. `make lint` calls it and the CI `lint` job runs only this script with pinned golangci-lint and shellcheck (#102)
- `docs/workflow.md` and `docs/release-workflow.md` follow the shared crazy-goat templates; project commands live in the new `AGENTS.md`
- `bin/` has the shared issue and worktree helper scripts, plus `worktree-setup.sh` and `worktree-teardown.sh`
- CI: aggregate `ci-ok` check; heavy jobs are skipped for documentation-only changes; a fast `docs` job checks Markdown
- Release workflow: pushing a `vX.Y.Z` tag publishes a GitHub release with notes from this file and `tyci` binaries for linux and darwin (amd64, arm64)
- Dependabot for Go modules and GitHub Actions
- A pull request template

### Changed
- `WORKFLOW.md` is replaced by `docs/workflow.md`
- Documentation under `docs/` and `TODO.md` is translated to English; Polish remains only as test data in the subagent test plans
- The CI workflow `ci.yml` is now `tests.yaml` and reads the Go version from `go.mod`
- `.golangci.yml`: the `misspell` check no longer forces the US locale, style-only staticcheck checks (ST, QF) are not enforced, and tests are excluded from `errcheck`

### Removed
- Dead code reported by the `unused` linter

### Fixed
- Findings of `golangci-lint` (unchecked errors, deprecated `x/ansi` mode constants, empty branches, redundant conversions and unformatted files), so `lint` passes again (#102)
