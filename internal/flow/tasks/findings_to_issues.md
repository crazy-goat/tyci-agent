{{if .ForgeCmd}}Turn the findings of this run into GitLab issues in {{.Repo}}. Use only the commands below for GitLab; `gh` and `glab` do not work for this repo.

1. Read `findings.md` in `{{.Worktree}}` and the review `report.md` files listed under "Run so far". If there are no findings, say so and finish with `done`.
2. For each finding, search for a similar issue: `{{.ForgeCmd}} issue-search "<2-4 keywords>"`.
3. If a similar issue exists, write the comment to a file and add it: `{{.ForgeCmd}} issue-comment <n> <file>`. Mention merge request !{{.PR}}. Do not create a duplicate.
4. Otherwise write the body to a file and create an issue: `{{.ForgeCmd}} issue-create "<title>" <file>`. Write in English. Use a conventional-commit title. Put the context and file:line (when known) in the body.
5. NEVER set a milestone.
6. NEVER apply the accepted label.
7. Never close, edit or relabel other issues.
8. Final answer: one line `done`.
{{else}}Turn the findings of this run into GitHub issues in {{.Repo}}.

1. Read `findings.md` in `{{.Worktree}}` and the review `report.md` files listed under "Run so far". If there are no findings, say so and finish with `done`.
2. For each finding, search for a similar issue: `gh issue list -R {{.Repo}} --search "<2-4 keywords>" --state all`.
3. If a similar issue exists, add a comment: `gh issue comment <n> -R {{.Repo}} --body "..."`. Mention PR #{{.PR}}. Do not create a duplicate.
4. Otherwise create an issue: `gh issue create -R {{.Repo}} --title "..." --body "..."`. Write in English. Use a conventional-commit title. Put the context and file:line (when known) in the body.
5. NEVER pass the --milestone option to `gh issue create`.
6. NEVER apply the accepted label.
7. Never close, edit or relabel other issues.
8. Final answer: one line `done`.
{{end}}
