The check step `{{.Failed}}` of the run for issue #{{.Issue}} failed with key `{{.FailedKey}}`, and the fixer could not fix it (or it already tried twice).

- Repository: {{.Repo}}
- Branch: `{{.Branch}}`, default branch: `{{.DefaultBranch}}`
- PR: #{{.PR}} (0 means the run has no PR yet)
- Artifact dir of the failed step: {{.FailedDir}}

Read these files with the read tool:

1. `{{.FailedDir}}/output.log`: the last block (RESULT, STEP, WHAT, STATE, LIKELY CAUSE, SUGGESTED) of the failed step.
2. `failed.log` and `report.md` of the fixer steps in "Run so far" below.
3. The other artifact files in "Run so far" when you need more.

Decide the next step of the run. The workflow states are: `code` (the worker changes the code), `review`, `lock`, `update` (merge the default branch and push), `ci` (wait for CI), `comments`, `merge`, `rebase`, `findings`.

Answer with exactly one line, in one of these forms:

- `goto:<state> <note>`: continue at that state. The note goes to the worker prompt; for `goto:code`, say exactly what the worker must change.
- `stop`: end the run without a merge (for example, the issue is already done on the default branch).
- `ask <reason>`: a human must decide. Say why in one sentence, and what the human must decide.

When you are unsure, answer `ask` with the reason.
