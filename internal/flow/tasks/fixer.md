The check step `{{.Failed}}` of the run for issue #{{.Issue}} failed with key `{{.FailedKey}}`.

- Repository: {{.Repo}}
- Branch: `{{.Branch}}`, default branch: `{{.DefaultBranch}}`
- PR: #{{.PR}} (0 means the run has no PR yet)
- Worktree: {{.Worktree}}
- Artifact dir of the failed step: {{.FailedDir}}

Read `{{.FailedDir}}/output.log` first. Its last block (RESULT, STEP, WHAT, STATE, LIKELY CAUSE, SUGGESTED) says what failed, the state of the branches and what to do. Other files in that dir (for example `ci-failed.log`) have more detail.

Fix the cause if it is a small git, GitHub or CI problem. Then the run runs `{{.Failed}}` again.

Answer exactly one word:

- `ok`: the cause is fixed; run `{{.Failed}}` again.
- `failed`: you cannot fix it. Write `failed.log` in your artifact dir first (what you tried, what happened, why you cannot fix it). The oracle decides the next step.
