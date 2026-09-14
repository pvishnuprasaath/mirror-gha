# Usage

## Installation

Requires [Go](https://go.dev/) 1.27+ and [Docker](https://www.docker.com/)
(jobs execute inside a real Docker container — that's how fidelity to
real GitHub Actions is kept honest).

```bash
git clone git@github.com:pvishnuprasaath/mirror-gha.git
cd mirror-gha
make build          # produces ./bin/mirror
```

There's no published binary release yet (pre-alpha) — build from source
for now.

## Quickstart

```bash
./bin/mirror run examples/workflows/basic-run.yml
```

Point it at any workflow file:

```bash
./bin/mirror run .github/workflows/ci.yml
```

## What's supported today

`mirror` executes a single job's `run:` steps in a real Ubuntu Docker
container, with:

- **Full expression syntax**: `${{ }}`, the `env`/`github`/`runner`/`steps`
  contexts, `==`/`!=`/`&&`/`||`/`!`, and the status functions `success()`,
  `failure()`, `always()`, `cancelled()`.
- **`if:` conditions** — a step whose condition evaluates false is
  reported as `skipped`, not silently dropped.
- **`continue-on-error: true`** — a failing step doesn't stop the job.
- **Step-to-step output passing** — the real `$GITHUB_OUTPUT` /
  `$GITHUB_ENV` / `$GITHUB_PATH` / `$GITHUB_STEP_SUMMARY` file protocol,
  the same one GitHub Actions itself uses, not a simulated approximation.

See [`examples/`](../examples/) for a runnable demonstration of each of
these.

## What's not supported yet

- Multi-job workflows with `needs:` dependencies
- `strategy.matrix` builds
- `uses:` actions — JS, Docker, or composite (Marketplace actions, `./local`
  actions, `docker://` actions)
- Artifacts (`actions/upload-artifact` / `download-artifact`) and caching
  (`actions/cache`)
- Windows and macOS runners (`windows-latest`, `macos-latest`) — these
  return a clear error naming the limitation, not a silent wrong result
- A dashboard UI

None of these fail silently — an unsupported `runs-on` value or a
workflow feature not yet wired up produces an explicit error, not a
false pass. See the [design spec's phased feature-parity matrix](design/specs/2026-09-14-mirror-gha-design.md#phased-feature-parity-matrix)
for the order these are being built in.

## Best practices while this is early

- **Don't treat a local pass as a guarantee.** `mirror` currently runs
  jobs in a plain `ubuntu:22.04` container, not a rebuild of GitHub's
  actual `runner-images` toolchain — a step that depends on
  preinstalled tooling (a specific language runtime, a CLI GitHub
  bundles by default) may behave differently locally until that
  fidelity work lands.
- **Use it for the fast, iterative loop** — testing a shell-script step,
  a conditional, or output-wiring change without waiting on a real CI
  run — not yet as a full replacement for CI on workflows using actions,
  matrices, or multi-job dependencies.
- **File an issue if a `run:`-only, single-job workflow behaves
  differently locally than it does on GitHub Actions.** That's squarely
  in scope today and is a real bug, not a known gap.
