Prepare the CHANGELOG for the release of milestone {{.Params.milestone}} in {{.Repo}}.

The release version is `{{.Params.milestone}}`. Use it as written. The version without the leading `v` is `X.Y.Z`.

1. You work in the current directory. It is the run worktree, on branch `{{.Branch}}`.
2. Read section 3 of `docs/release-workflow.md`. Follow it.
3. Open `CHANGELOG.md`. Rename the heading `## [Unreleased]` to `## [X.Y.Z] - YYYY-MM-DD`. Use today's date for YYYY-MM-DD. Run `date -u +%Y-%m-%d` to get it.
4. Add a new empty heading `## [Unreleased]` above the renamed section. Leave one blank line between the two sections.
5. Keep every entry of the release section as it is. Do not change, reword or remove an entry.
6. Change no other file. Only `CHANGELOG.md` may change. Run `git status` to check this.
7. Commit the change with exactly this message: `chore: release {{.Params.milestone}}`.
8. Do not push. Do not create a tag. The run pushes the branch after you answer.
9. If `CHANGELOG.md` has no entry under `## [Unreleased]`, change nothing and do not commit.

Your final answer is exactly one word:

- `done`: you committed the change.
- `failed`: you could not commit the change.
