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
| [`job-dependencies.yml`](workflows/job-dependencies.yml) | Multi-job `needs:` — a `deploy` job waits for `build` and reads its declared job output via the `needs` context |
| [`matrix-build.yml`](workflows/matrix-build.yml) | `strategy.matrix` — the job runs once per axis value, each with its own `matrix` context |
| [`workspace.yml`](workflows/workspace.yml) | The job workspace — real project files visible, default working directory, `github.workspace`/`$GITHUB_WORKSPACE`, writes land back on the real host filesystem |
| [`uses-local-action.yml`](workflows/uses-local-action.yml) | `uses:` with a local JS action ([`actions/hello-action`](workflows/actions/hello-action/), a hand-written fixture with no npm dependencies) — real Node execution, `with:` inputs, output read back via `steps.<id>.outputs` |
| [`uses-marketplace-action.yml`](workflows/uses-marketplace-action.yml) | `uses:` with a real, unmodified Marketplace action (`actions/hello-world-javascript-action`) — fetched and cached from GitHub, including its deprecated stdout-based output convention |
| [`uses-docker-action.yml`](workflows/uses-docker-action.yml) | `uses:` with a repo-based Docker action ([`actions/docker-hello-action`](workflows/actions/docker-hello-action/)) — builds its Dockerfile (cached by tag after the first run), runs as a separate sibling container |
| [`uses-docker-image.yml`](workflows/uses-docker-image.yml) | `uses:` with a raw `docker://image:tag` reference — no `action.yml`, `entrypoint`/`args` from `with:`, proves the job workspace bind mount and network join both work |

## What's not shown here (yet)

These examples deliberately stick to what's actually implemented today.
Composite actions (`runs.using: composite`), artifacts, caching, matrix
`include`/`exclude`, and Windows/macOS runners aren't supported yet — see
[the roadmap](../docs/design/specs/2026-09-14-mirror-gha-design.md#phased-feature-parity-matrix)
for what's coming and in what order. Workflow files using those features
will fail with a clear "not supported yet" error rather than silently
doing the wrong thing.
