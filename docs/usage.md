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

`mirror` executes a job's `run:` steps in one real, long-lived Ubuntu
Docker container — the same container for every step in the job, so
filesystem state (files written, packages installed) persists step to
step, matching real GitHub Actions rather than a fresh sandbox each time.

- **The real GitHub Actions expression grammar**: `${{ }}`, the
  `env`/`github`/`runner`/`steps` contexts (matched case-insensitively,
  same as real GitHub Actions), parenthesized grouping, `==`/`!=`/`<`/
  `<=`/`>`/`>=`/`&&`/`||`/`!` — `&&`/`||` return the actual operand
  (not a coerced boolean), so `env.NAME || 'default'` works as a
  default-value pattern the same way it does on GitHub. Built-in
  functions: `success()`, `failure()`, `always()`, `cancelled()`,
  `contains()`, `startsWith()`, `endsWith()`, `format()`, `join()`,
  `toJSON()`, `fromJSON()`. `hashFiles()` isn't implemented yet — it
  needs a real checked-out workspace — and returns a clear error rather
  than a wrong result.
- **`if:` conditions** — a step whose condition evaluates false is
  reported as `skipped`, not silently dropped.
- **`continue-on-error: true`** — a failing step doesn't stop the job.
- **Step-to-step output passing** — the real `$GITHUB_OUTPUT` /
  `$GITHUB_ENV` / `$GITHUB_PATH` / `$GITHUB_STEP_SUMMARY` file protocol,
  the same one GitHub Actions itself uses, not a simulated approximation.
- **Multi-job workflows with `needs:`** — jobs run in dependency order; a
  job whose `needs:` didn't all succeed is reported `skipped`, not run. A
  job's declared `outputs:` are available to dependents via
  `needs.<job>.outputs.<name>` and `needs.<job>.result`.
- **`strategy.matrix`** — a job with a matrix runs once per combination of
  its axes, each with its own `matrix.<key>` context. `fail-fast`
  (defaults to `true`, matching GitHub Actions) stops starting new
  combinations after the first failure. `matrix.include`/`exclude` are
  parsed but rejected with a clear error rather than approximated — their
  real merge semantics are fiddly enough that guessing wrong would be
  worse than refusing.
- **`timeout-minutes`** at both job and step level.
- **`defaults.run.shell` / `defaults.run.working-directory`** at workflow
  and job level, with the real GitHub Actions precedence: step overrides
  job defaults, which override workflow defaults.
- **A local-only `vars` context** — there's no repository/organization
  variable store to read from yet (that's part of the GitHub API shim,
  not built), so `vars.*` currently just resolves to nothing unless a
  future CLI flag populates it. Documented here so it's clear this is a
  stub, not a silent no-op you'd have to discover by trial and error.

See [`examples/`](../examples/) for a runnable demonstration of each of
these.

The expression parser itself is [`third_party/ghaexpr`](../third_party/ghaexpr) —
a surgical extraction of [rhysd/actionlint](https://github.com/rhysd/actionlint)'s
lexer/parser (MIT licensed; see its `NOTICE.md` for provenance) — rather
than a hand-rolled one, so the grammar itself is actionlint-grade
correct; `mirror` supplies the evaluation semantics on top.

## What's not supported yet

- `uses:` actions — JS, Docker, or composite (Marketplace actions, `./local`
  actions, `docker://` actions)
- Artifacts (`actions/upload-artifact` / `download-artifact`) and caching
  (`actions/cache`)
- `matrix.include` / `matrix.exclude`
- `services:` and `container:` job fields
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
  a conditional, matrix, or multi-job dependency change without waiting
  on a real CI run — not yet as a full replacement for CI on workflows
  that use `uses:` actions, artifacts, or caching.
- **File an issue if a `run:`-only, single-job workflow behaves
  differently locally than it does on GitHub Actions.** That's squarely
  in scope today and is a real bug, not a known gap.
