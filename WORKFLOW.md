# Workflow: Issue → Feature Branch → Implementation → Code Review → PR → CI → Merge

This document describes the complete workflow for handling issues in the
[tyci](https://github.com/crazy-goat/tyci) repository using `gh` and `git`.

---

## 1. Browse Open Issues

```bash
# List open issues (title, number, labels)
gh issue list --state open --limit 30

# View a specific issue (description, labels, state)
gh issue view <NUMBER> --json title,body,labels,state
```

**Criteria for selecting the most impactful issue:**
- Issues labeled `enhancement` + `ux` / `cli` – high impact
- Issues about stability, correctness, performance
- Issues blocking other tasks
- Issues most relevant to users (e.g. TUI improvements, provider fixes)

---

## 2. Create a Fresh Feature Branch

```bash
# Make sure you're on main with the latest changes
git checkout main
git pull origin main

# Create a feature branch
git checkout -b feat/issue-<NUMBER>-<short-description>
```

**Branch naming convention:** `feat/issue-<NUMBER>-<kebab-case>`
(e.g. `feat/issue-42-readline-integration`)

---

## 3. Implement the Change

```bash
# Edit files, then commit and push
git add -A
git commit -m "feat(<scope>): <short description> (closes #<NUMBER>)"
git push origin feat/issue-<NUMBER>-<description>
```

**Commit message convention:**
- Type: `feat`, `fix`, `docs`, `refactor`, `ci`, `test`, `chore`
- Scope: `(agent)`, `(display)`, `(tui)`, `(api)`, `(tools)`, `(providers)`, `(internal)`, `(ci)` etc.
- Reference to issue: `(closes #<NUMBER>)`

---

## 4. Code Review via Subagent

After implementation, run a code review using a subagent (separate agent with
its own context). The subagent checks:

- Alignment with project architecture
- Type correctness and signatures
- Error handling and edge cases
- Coding style and idiomatic Go
- Test coverage
- Security (input validation, shell injection)

```bash
# The subagent receives a task like:
# "Code review the changes in files: <list of files>.
#  Check: type correctness, error handling, Go idioms,
#  missing tests, outdated documentation.
#  List all issues to fix."
```

---

## 5. Fix Issues Found in Code Review

```bash
# For each problem found:
# 1. Apply the fix
# 2. Commit with a descriptive message
git add -A
git commit -m "fix: <description of fix>"
git push origin feat/issue-<NUMBER>-<description>
```

**All issues must be fixed – even the least significant ones.**

---

## 6. Repeat Code Review

After fixing, invoke the subagent for another code review.

Repeat steps 5→6 until the subagent reports no issues.

> **Acceptance criteria:** The subagent responds: "Code looks good, no issues
> to fix."

---

## 7. Run Linters and Tests Locally

Before opening a PR, verify that all linters and tests pass on your machine:

```bash
# Run Go vet (static analysis)
go vet ./...

# Run all tests
go test ./... -count=1

# Race-detection tests (important for concurrent code)
go test -race ./... -count=1

# Build check
go build ./...
```

If you have `golangci-lint` installed, also run:

```bash
golangci-lint run ./...
```

Only create the PR when all lints and tests pass locally.

---

## 8. Create a Pull Request

```bash
# Create a PR from the feature branch to main
gh pr create \
  --title "feat: <short description> (closes #<NUMBER>)" \
  --body "## Description

Closes #<NUMBER>

## Changes

- <list of changes>

## Code Review

- [ ] Passed subagent code review
- [ ] All review comments addressed" \
  --base main \
  --assignee @me
```

---

## 9. Wait for CI

CI is configured in `.github/workflows/` (once set up). Typical checks:

1. **lint** – `go vet`, `golangci-lint` (if configured)
2. **test-matrix** (Go 1.24, 1.25) – unit tests across versions
3. **test** – aggregator checking that lint and test matrix passed

```bash
# Check PR status
gh pr view --json statusCheckRollup

# Wait for all checks to finish
gh pr checks --watch
```

---

## 10. Handle CI Failures

If CI fails:

```bash
# 1. See which checks failed
gh pr checks

# 2. View logs
gh run view --log --job <job-name>

# 3. Fix the issues locally
# 4. Run code review via subagent again (repeat steps 4-6)
# 5. Commit the fixes
git add -A
git commit -m "fix: <description of CI fix>"
git push origin feat/issue-<NUMBER>-<description>

# 6. Wait for CI to re-run
gh pr checks --watch
```

**Repeat until all CI checks pass.**

---

## 11. Merge PR and Close Issue

```bash
# Merge PR (squash merge recommended for clean history)
gh pr merge --squash --delete-branch

# Close the issue (automatic if commit contains "closes #<NUMBER>")
# Alternatively:
gh issue close <NUMBER>
```

---

## 12. Switch Back to main

```bash
git checkout main
git pull origin main
```

Done. Ready to start the next cycle from step 1.

---

## Quick Reference – Full Cycle

```bash
# 1. Pick an issue
gh issue list --state open --limit 30
gh issue view <NUMBER>

# 2. Feature branch
git checkout main && git pull origin main
git checkout -b feat/issue-<NUMBER>-<description>

# 3. Implementation
# ... coding ...
git add -A && git commit -m "feat: implement <desc> (closes #<NUMBER>)"
git push origin feat/issue-<NUMBER>-<description>

# 4. Code Review (subagent)
# ... fix issues ... (repeat until clean)

# 5. Run linters and tests locally
go vet ./...
go test ./... -count=1

# 6. PR
gh pr create --title "feat: <desc> (closes #<NUMBER>)" --body "..." --base main

# 7. CI
gh pr checks --watch
# ... if failures → fix, code review, push → wait for CI (repeat)

# 8. Merge
gh pr merge --squash --delete-branch
gh issue close <NUMBER>

# 9. Switch back
git checkout main && git pull origin main
```

---

## Notes

- **gh** must be configured and authenticated (`gh auth status`).
- All commits must be signed-off if the repo requires DCO.
- Keep feature branches short-lived. If a rebase is needed:
  ```bash
  git fetch origin main
  git rebase origin/main
  git push --force-with-lease origin feat/issue-<NUMBER>-<description>
  ```
- Code review via subagent runs locally – the subagent has access to
  read/write/edit/bash tools. Give it clear instructions on what to check.
- To run a race-free build (for debugging):
  ```bash
  make build
  ```
- For an optimized release build:
  ```bash
  make release
  ```
- The binary is `tyci`. After building, test it locally:
  ```bash
  ./tyci --help
  ```
