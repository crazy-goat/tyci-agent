The merge or rebase of PR #{{.PR}} (branch `{{.Branch}}`, default branch `{{.DefaultBranch}}`) failed. This is the output:

```
{{.Reason}}
```

Decide what to do next. Answer with exactly one word:

- `retry`: the failure is temporary. Run the step again.
- `code`: the code must change (for example a merge conflict). Send the work back to the worker.
- `ask`: a human must decide.
