# Examples

Each file here is a real, runnable workflow demonstrating one supported
feature. Build the binary first (`make build` from the repo root), then:

```bash
./bin/mirror run examples/workflows/<file>.yml
```

| File | Demonstrates |
|---|---|
| [`basic-run.yml`](workflows/basic-run.yml) | The minimum viable workflow — a job with `run:` steps, executed in a real Docker container |
| [`conditional-step.yml`](workflows/conditional-step.yml) | `if:` conditions evaluated against the `env` context, including a step that gets skipped |
| [`continue-on-error.yml`](workflows/continue-on-error.yml) | `continue-on-error: true` — a failing step that doesn't stop the job |
| [`output-passing.yml`](workflows/output-passing.yml) | A step writing to `$GITHUB_OUTPUT`, read back by a later step via `steps.<id>.outputs.<name>` |
| [`shared-state.yml`](workflows/shared-state.yml) | All steps in a job share one environment — a file written by step 1 is readable by step 2, matching real GitHub Actions |
| [`expression-functions.yml`](workflows/expression-functions.yml) | Built-in expression functions (`startsWith`, `format`) and real `\|\|` short-circuit default-value semantics |

## What's not shown here (yet)

These examples deliberately stick to what's actually implemented today.
Multi-job dependencies (`needs:`), matrix builds, `uses:` actions (JS,
Docker, or composite), artifacts, and caching aren't supported yet — see
[the roadmap](../docs/design/specs/2026-09-14-mirror-gha-design.md#phased-feature-parity-matrix)
for what's coming and in what order. Workflow files using those features
will fail with a clear "not supported yet" error rather than silently
doing the wrong thing.
