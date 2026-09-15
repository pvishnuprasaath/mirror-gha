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
  `toJSON()`, `fromJSON()`, `hashFiles()` — a real SHA-256-over-matched-
  files implementation (single hash over sorted matched files'
  concatenated contents, matching act's own pure-Go algorithm), with a
  hand-rolled doublestar glob matcher (`*`, `?`, `**` across directory
  segments) covering the common real-world patterns
  (`hashFiles('**/package-lock.json')`) — no `!` negation or brace
  expansion yet, a documented gap versus the real `@actions/glob`
  library, not a silent wrong result.
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
  combinations after the first failure. `matrix.include`/`exclude` have
  real GitHub Actions merge semantics (matching act's own implementation):
  exclude drops any combination matching all of an exclude entry's
  key/value pairs (every exclude key must be a real matrix axis, or this
  is an error); include merges its extra keys into every combination
  whose axis-key subset already matches, or — if it matches nothing —
  becomes its own standalone combination. A matrix with only `include`
  and no axes treats each include entry as one full combination directly.
- **Real concurrent matrix execution** — a job's matrix combinations run
  concurrently, bounded by `strategy.max-parallel` (default 4 when
  unset, further capped by the actual combination count — matching
  act's own considered default, chosen to respect a single local Docker
  daemon's real resource limits rather than GitHub's own effectively
  unbounded cloud-runner default). Independent jobs (no shared `needs:`)
  still run sequentially — real GitHub Actions has no equivalent
  parallelism knob for jobs either, only for matrix combinations, so
  this isn't a gap. Fail-fast stops *starting* new combinations after a
  failure but doesn't cancel ones already running, extending mirror-gha's
  existing "skip not-yet-started, don't abort in-flight" semantic from
  steps to combinations. `concurrency:` (job-level; workflow-level is
  parsed but an intentional no-op — it exists in real GitHub Actions to
  serialize separate workflow *runs*, a concept with no meaning in a
  tool that only ever executes one run per invocation) serializes or
  cancels-in-progress combinations of the same job whose evaluated group
  name (which may reference `matrix.*`) coincides — a real gap in act
  itself (confirmed via source: the field doesn't exist there at all),
  so this is mirror-gha's own design rather than a port.
- **`github.event_name`/`github.event`/`GITHUB_EVENT_NAME`/
  `GITHUB_EVENT_PATH`** — real for a user-supplied `--event-path <file>`
  (loaded verbatim, matching act's own mechanism exactly); otherwise a
  synthetic but structurally real default payload (matching GitHub's own
  public webhook-payload shapes) for `push`, `pull_request`,
  `workflow_dispatch`, `workflow_call`, `repository_dispatch`, and
  `workflow_run` — any other trigger name defaults to `{}`, matching
  act's own default for the untyped case (act itself never fabricates a
  payload for any event, confirmed via source). `--event-name <name>`
  selects `github.event_name`; without it, the workflow's own `on:`
  block picks it when it names exactly one trigger, else it defaults to
  `"push"` — the same priority chain act uses. `github.event.*` supports
  arbitrary-depth expressions (`github.event.pull_request.head.ref`),
  not just the top-level fields. No `on: push: branches/paths` or
  `pull_request: types` filtering — matches act's own choice (dead,
  unwired code for this exists even in act's own source), and is
  inherently moot once a user has explicitly invoked a local run; the
  job's own `if:` conditions are the mechanism that still matters
  locally.
- **`container:` and `services:` job fields** — `container:` swaps the
  image the job's own container runs as (bare image string or a mapping
  with `env`/`ports`/`volumes`/`options`/`credentials`); `services:`
  starts one sidecar container per entry, reachable from job steps by
  its map key as hostname over a per-job Docker network created only
  when services are present (jobs without `services:` are unaffected —
  no network is created). Both support `credentials:` (`username`/
  `password`, exactly those two keys) for private registries — mirror-gha
  shells out to `docker login`/`docker logout` around the pull/run since
  it has no Docker Go SDK dependency (password piped via stdin, never
  passed as a CLI argument). Health-checked services (`options:
  --health-cmd ...`) are waited on (up to 5 minutes) before the job's
  steps start; services with no healthcheck are treated as ready
  immediately. Known v1 limitation: registry login/logout is
  process-wide (shared Docker daemon config), so two jobs in the same
  `mirror run` pulling different private images concurrently could
  race — acceptable for a local single-workflow-run tool, not solved in
  v1.
- **`runs-on: macos-latest`/`macos-13`/`macos-14`/`macos-15`** — executes
  directly on the host process, no container at all, since macOS cannot
  be virtualized or containerized on non-Apple hardware. Only works when
  mirror-gha itself is running on a Mac — from any other host OS this
  returns a clear, specific error rather than a silent wrong attempt.
  `container:`, `services:`, and `uses: docker://...`/Docker-action steps
  all error clearly on macOS jobs, matching real GitHub Actions' own
  documented Linux-only constraint for these features. The default shell
  for a `run:` step with no `shell:` is `bash` here (matching real GitHub
  Actions' own default for macOS runners), not `sh` (the Docker backend's
  default). Job steps inherit mirror-gha's own real host environment
  (`PATH`, `HOME`, etc.) — unlike the Docker backend's clean-container
  environment — matching how a real self-hosted/macOS runner operates as
  the logged-in user.
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
- **Real `actions/cache` support** — a local HTTP server matching
  GitHub's actual cache API (not a filesystem shim, which
  `actions/cache` would never call into) serves save/restore requests
  from the real, unmodified action. Restore-keys matching mirrors
  GitHub's actual semantics (exact match, then prefix-match fallback,
  most-recently-created wins) rather than a simplified approximation.
  The cache store persists across separate `mirror run` invocations —
  a cache saved in one run is genuinely restorable in a later one, the
  entire point of the feature. Also required exporting every `github.*`
  expression-context value as a real `GITHUB_*` env var (previously
  only available inside `${{ }}` expressions) — a general correctness
  gap found while getting this working for real, not specific to cache.
- **Real `actions/upload-artifact`/`download-artifact` support** — both
  the legacy v3 REST protocol and the current v4 protocol, served by one
  local HTTP server (`internal/artifactserver`). Unlike the cache store,
  artifact storage is a fresh directory per `mirror run` invocation
  (printed at the end of the run so it's inspectable) — never restored
  by a later invocation, matching real GitHub Actions' own per-run
  artifact model. No new action-type dispatch was needed: both actions
  are themselves bundled JS actions, running through the existing
  JS-actions machinery unmodified once the right env vars
  (`ACTIONS_RUNTIME_URL`, `ACTIONS_RESULTS_URL`) are present.
- **JS action post entry points** (`runs.post` in `action.yml`) run
  after all of a job's own top-level steps finish, in reverse step
  order, with state passed from the main run via `$GITHUB_STATE` /
  `STATE_*` env vars — exactly how `actions/cache@v4` itself works (its
  `main` only restores; the actual save happens in `post`). Nested
  `uses:` steps inside a composite action don't get their own post
  actions run yet — a documented, accepted scope limit.
- **`environment:`** — parsed as either a bare name or a `{name, url}`
  mapping, exposed as `github.environment`. Name only — no approval gate
  and no environment-scoped secrets/vars store, a documented v1 scope
  limit (there's no environment-secrets concept locally to scope against).
- **`permissions:`** (workflow- and job-level, bare `read-all`/`write-all`
  or a scope map) — parsed and validated, but shim-only: there's no real
  GitHub API surface running locally for it to actually restrict. Every
  step gets a `GITHUB_TOKEN` env var (a plain placeholder string, not
  JWT-shaped — real ones aren't either) so actions reading
  `process.env.GITHUB_TOKEN` directly don't crash on it being missing,
  plus a minimal `secrets` context exposing `secrets.GITHUB_TOKEN` for
  `with:` blocks that reference it that way instead.
- **Workflow commands beyond `::set-output`**: `::group::`/`::endgroup::`
  render as a plain-terminal fold marker (`▶ <title>`, no raw passthrough —
  there's no foldable UI locally to match GitHub's own log rendering).
  `::error::`/`::warning::`/`::notice::` render with a labeled marker and
  have zero effect on the step's exit code, matching real GitHub Actions
  (the step's own exit code is what fails it). `::add-mask::` registers a
  value at runtime, redacted (case-sensitive literal substring replace)
  from the step that registered it and every subsequent step's output in
  the same job — a real per-job mask registry, not just a display trick.
  `::debug::` lines are hidden by default and only shown with `mirror run
  --debug`, which also exports `ACTIONS_STEP_DEBUG=true` to steps —
  matching real GitHub Actions' own default-hidden debug logging (act
  itself doesn't gate this at all).

See [`examples/`](../examples/) for a runnable demonstration of each of
these.

The expression parser itself is [`third_party/ghaexpr`](../third_party/ghaexpr) —
a surgical extraction of [rhysd/actionlint](https://github.com/rhysd/actionlint)'s
lexer/parser (MIT licensed; see its `NOTICE.md` for provenance) — rather
than a hand-rolled one, so the grammar itself is actionlint-grade
correct; `mirror` supplies the evaluation semantics on top.

## What's not supported yet

- Windows runners (`windows-latest`) — this returns a clear error naming
  the limitation, not a silent wrong result
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
