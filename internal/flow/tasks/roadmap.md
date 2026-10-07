Order the issues below for work. This is the input JSON (issue bodies are not included):

```json
{{.Input}}
```

Rules:

- Put dependencies first. Use the `mentions` field as a hint for dependencies.
- Put bugs and security issues before features.
- Put small issues before large ones.
- When a dependency is unclear, keep the issue-number order.

Answer with exactly this JSON and nothing else:

```json
{"order":[{"issue":201,"depends_on":[],"reason":"One short sentence."}]}
```

Include every issue once. `depends_on` lists only issue numbers from the input.
