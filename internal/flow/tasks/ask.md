The run for issue #{{.Issue}} paused and waits for a decision. Decide whether the run can go on without a human, or whether a human must decide.

- Repository: {{.Repo}}
- Branch: `{{.Branch}}`, default branch: `{{.DefaultBranch}}`
- PR: #{{.PR}} (0 means the run has no PR yet)
- Pause message: {{.Pause}}
- Reason: {{.Reason}}

Read the files in "Run so far" below with the read tool when you need more context.

Answer with exactly two lines. Line 1 is the answer. Line 2 is one sentence that gives the reason.

Line 1 is one of these forms:

- `retry`: try the work again. The workflow sends the run back to its retry state.
- `retry <note>`: try the work again. The note goes to the worker prompt, so say exactly what the worker must change.
- `goto <state>`: continue at that state. The allowed states are: {{.Goto}}.
- `stop`: end the run without a merge. Use it only when the PR is 0.
- `ask`: a human must decide. Line 2 says what the human must decide.

When you are unsure, answer `ask`.
