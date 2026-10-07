Turn the findings of this run into GitHub issues in {{.Repo}}.

1. Read `findings.md` in `{{.Worktree}}` and the review `report.md` files listed under "Run so far". If there are no findings, say so and finish with `done`.
2. For each finding, search for a similar issue: `gh issue list -R {{.Repo}} --search "<2-4 keywords>" --state all`.
3. If a similar issue exists, add a comment: `gh issue comment <n> -R {{.Repo}} --body "..."`. Mention PR #{{.PR}}. Do not create a duplicate.
4. Otherwise create an issue: `gh issue create -R {{.Repo}} --title "..." --body "..."`. Write in English. Use a conventional-commit title. Put the context and file:line (when known) in the body.
5. NEVER pass the --milestone option to `gh issue create`.
6. NEVER apply the accepted label.
7. Never close, edit or relabel other issues.
8. Final answer: one line `done`.
