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

## Workflow proposal

Earlier runs of this repository are in the directories next to {{.RunDir}}. If the workflow `{{.Workflow}}` cannot handle this failure (no state or check covers it), or the same step failed with the same cause in an earlier run, also write a proposal in your artifact dir:

- `proposal.md`: a title line, then what failed, why, and what to change in the workflow.
- `proposal.patch`: a unified diff (`git diff` format, paths `a/.tyci/...` and `b/.tyci/...`) of the repository's `.tyci/` files only: the workflow JSON, check scripts, task templates or role prompts. Never change other files.

Diff against the repository's own `.tyci/` files first. If the repository has no `.tyci/workflows/{{.Workflow}}.json`, tyci ejects only the missing files of the builtin workflow before it applies the patch; existing `.tyci/` files and role prompts stay. To see the builtin files, run `tyci workflow eject {{.Workflow}} --dir <a new temp dir>`. A proposal cannot change a workflow from `~/.tyci/workflows/`.

Nothing changes until the user accepts the proposal. Do not apply it yourself.
