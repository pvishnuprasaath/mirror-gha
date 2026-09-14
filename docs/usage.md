# Usage

## Installation

Requires [Docker](https://www.docker.com/) either way (jobs execute
inside a real Docker container — that's how fidelity to real GitHub
Actions is kept honest).

**No tagged release exists yet** (pre-alpha). Build from source for now:

```bash
git clone git@github.com:pvishnuprasaath/mirror-gha.git
cd mirror-gha
make build          # produces ./bin/mirror — requires Go 1.27+
```

Once the first `v*` tag is pushed, `curl -fsSL .../install.sh | sh` and
prebuilt binaries on the GitHub Releases page become available — see
[../README.md](../README.md#install) and
[RELEASING.md](RELEASING.md) for exactly how the release pipeline works.

## Quickstart

```bash
./bin/mirror run examples/workflows/basic-run.yml
```

Point it at any workflow file:

```bash
./bin/mirror run .github/workflows/ci.yml
```

## Introspection flags

Three read-only modes for understanding a workflow before actually
running it — none of them touch Docker:

```bash
mirror run --list workflow.yml     # every job (and matrix combination), its runs-on and needs
mirror run --graph workflow.yml    # jobs in dependency order, with needs: indented underneath
mirror run --dryrun workflow.yml   # runs the real needs/matrix/if orchestration, but every
                                    # step reports success without actually executing —
                                    # useful for checking a workflow's shape/logic quickly.
                                    # An unsupported runs-on still errors even in dry-run mode,
                                    # since "would this even run here" is what it's for.
```

## The job workspace

Every job bind-mounts a real directory on disk — by default, wherever
`mirror run` was invoked from — into the job as its workspace, at
`/github/workspace` (act's own convention). This is a genuine bind mount,
not a copy: files a step writes there land back on your real filesystem,
and files already on disk (your actual project) are visible from the
first step onward.

```bash
mirror run workflow.yml                    # workspace = current directory
mirror run --workdir /path/to/repo workflow.yml   # workspace = an explicit directory
```

Steps get this via three equivalent handles: the `github.workspace`
expression context, the `$GITHUB_WORKSPACE` environment variable, and as
the *default* working directory whenever a step/job/workflow doesn't set
`working-directory`/`defaults.run.working-directory` explicitly.

One fidelity caveat, not yet addressed: jobs run as root inside the
container, so files a step writes can end up root-owned on the host on
Linux. Not an issue on macOS via Docker Desktop's filesystem layer.

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
  `toJSON()`, `fromJSON()`. `hashFiles()` isn't implemented yet (a real
  workspace exists now, so nothing blocks it — it just hasn't been built)
  and returns a clear error rather than a wrong result.
- **`if:` conditions** — a step whose condition evaluates false is
  reported as `skipped`, not silently dropped.
- **`continue-on-error: true`** — a failing step doesn't stop the job.
- **Step-to-step output passing** — the real `$GITHUB_OUTPUT` /
  `$GITHUB_ENV` / `$GITHUB_PATH` / `$GITHUB_STEP_SUMMARY` file protocol,
  the same one GitHub Actions itself uses, not a simulated approximation.
  Outputs set via the older, deprecated stdout-based workflow commands
  (`::set-output name=X::Y` and `##[set-output name=X;]Y`) are parsed
  too, since a large fraction of real-world actions — including GitHub's
  own `actions/hello-world-javascript-action` — still use them.
- **`uses:` JS actions** — Marketplace (`owner/repo[/subpath]@ref`) and
  local (`./path`, workspace-relative) actions, executed for real: source
  fetched and cached from GitHub, run against one pinned Node build
  copied into the job container, `with:` inputs mapped to `INPUT_*` env
  vars exactly as GitHub Actions does (dashes preserved in input names —
  which is exactly why `uses:` steps exec Node directly with no
  intermediate shell: a shell silently drops env vars with dashed names
  before it execs children).
- **`uses:` Docker actions** — a raw `docker://image:tag` reference, or a
  Marketplace/local action whose `runs.image` names a Dockerfile (built
  and cached by tag, rebuilt only if the tag doesn't already exist). Runs
  as its own container — not exec'd into the job's container, since a
  Docker action's image is frequently a different base OS entirely —
  joined to the job container's network namespace so `localhost`
  service-container access still works, with the job workspace, `with:`/
  `INPUT_*`, and `$GITHUB_OUTPUT` all working the same way they do
  everywhere else. `/var/run/docker.sock` is mounted into every Docker
  action container unconditionally, matching real GitHub-hosted runners —
  this gives any Docker action, including an unmodified Marketplace one,
  full host Docker daemon control the moment it runs.
- **`uses:` composite actions** — a composite action's own `runs.steps:`
  execute through the exact same step-execution logic every other step
  uses (recursively — a composite can nest another composite, capped at
  10 levels deep as a safety net against a self-referencing local action,
  not a real-world limitation). Its own declared inputs are available to
  its nested steps via the `inputs.*` expression context; its own
  `outputs:` (each a `${{ steps.x.outputs.y }}`-style expression,
  evaluated against the composite's own nested step scope) bridge back up
  as the calling `uses:` step's own output. Nested steps never inherit
  the calling workflow/job's `defaults.run.shell`/`working-directory` —
  matches real GitHub Actions' own behavior here. No `runs.using` value a
  real action might declare is rejected as unsupported anymore.
- **`--local-repository owner/repo[@ref]=local/path`** (repeatable) —
  overrides where a Marketplace-style action reference resolves from,
  for testing an in-progress local edit before publishing/tagging it.
  Checked before the network fetch; a hit skips fetching entirely.
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
- **A local `vars` context** — real GitHub Actions populates this from a
  repository/organization variable store mirror-gha has no equivalent of
  (no GitHub API shim exists), so values come from the CLI instead:
  `--var NAME=VALUE` (repeatable; bare `--var NAME` sets an empty value)
  and `--var-file <path>` (default `.vars`, one `NAME=VALUE`/bare `NAME`
  per line, blank lines and `#` comments skipped, missing file not an
  error) — matching act's own `--var`/`--var-file` flags exactly. `--var`
  entries override `--var-file` entries on conflict.

See [`examples/`](../examples/) for a runnable demonstration of each of
these.

The expression parser itself is [`third_party/ghaexpr`](../third_party/ghaexpr) —
a surgical extraction of [rhysd/actionlint](https://github.com/rhysd/actionlint)'s
lexer/parser (MIT licensed; see its `NOTICE.md` for provenance) — rather
than a hand-rolled one, so the grammar itself is actionlint-grade
correct; `mirror` supplies the evaluation semantics on top.

## What's not supported yet

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
  a conditional, matrix, multi-job dependency, or JS/Docker-action change
  without waiting on a real CI run — not yet as a full replacement for CI
  on workflows using composite actions, artifacts, or caching.
- **File an issue if a workflow using only `run:`/`uses:` (JS, Docker, or
  local) steps behaves differently locally than it does on GitHub
  Actions.** That's squarely in scope today and is a real bug, not a
  known gap.
