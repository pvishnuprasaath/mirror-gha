# Mirror — Local GitHub Actions Runtime (Design Spec)

**Date:** 2026-09-14
**Status:** Approved design, pending implementation plan
**CLI binary:** `mirror`
**Product name:** mirror-gha

## Problem

Developers have no way to test GitHub Actions workflow changes locally. The
only feedback loop today is: push a commit, open/update a PR, wait for GHA to
run, read results. Existing local-testing tools (`nektos/act` and its VS Code
UI wrappers) solve the basic case but have known fidelity gaps: Docker
containers aren't real GitHub-hosted VMs, macOS/Windows runners can't be
faithfully emulated, artifact/cache/matrix emulation is partial, and nobody
has meaningfully closed these gaps in 6+ years.

## Goal

An exact local replica of the GitHub Actions execution environment — full
workflow syntax, full contexts/expressions, all three action types (JS,
Docker, composite), real artifact/cache semantics, and (eventually) real
runner-OS parity for Linux, Windows, and macOS — built from scratch, not
layered on `act`.

## Non-goals (explicitly out of scope)

- No multi-repo / organization fleet dashboard. Each repo adopts the tool
  independently; a developer with multiple repos runs it per-repo.
- Not scoped to "CI" — GitHub Actions covers ChatOps, issue/PR automation,
  release automation, and scheduled jobs, not just build/test pipelines. The
  engine must support all trigger types, not just push/pull_request.

## Users

Individual developers, self-serve, per-repo. Bootstrapped business —
open-core monetization, not VC-funded, so v1 must be shippable by a small
team without multi-year runway.

## Hard technical constraint

macOS cannot be virtualized or containerized on non-Apple hardware — Apple's
EULA restricts macOS guest OS to Apple-branded hardware, and no Docker macOS
images exist. "Exact replica on every host" is not literally achievable for
macOS jobs; the honest design is that macOS jobs only run natively when the
host itself is a Mac (which mirrors how GitHub's own macOS runners work:
dedicated Apple hardware, not virtualized on commodity infra).

## Architecture

One Go binary, five internal modules, shared by both the CLI and the
dashboard (dashboard is a view over the same engine state, not a separate
execution path):

1. **Workflow Engine** (`internal/engine`) — parses `.github/workflows/*.yml`
   using GitHub's actual syntax: triggers, `needs`, `if`, `strategy.matrix`
   (include/exclude), `outputs`, reusable `workflow_call`. Builds a job DAG.
   Evaluates `${{ }}` expressions and all contexts (`github`, `env`, `vars`,
   `job`, `steps`, `runner`, `secrets`, `matrix`, `needs`, `inputs`) per
   GitHub's expression-language spec.

2. **Runner Backend** (`internal/runner`, pluggable interface) — v1 ships
   `LinuxDockerBackend` only. Each job runs in a container tracking
   `actions/runner-images: ubuntu-latest`'s published toolchain, rebuilt on a
   schedule to track GitHub's updates. `WindowsHyperVBackend` and
   `MacOSTartBackend` are stub implementations behind the same interface —
   present in code as explicit `// TODO: Phase 2/3`, returning a clear
   "not available on this host" error rather than silently skipping.

3. **Actions Runtime** (`internal/actions`) — resolves `uses:` references
   (Marketplace, local `./path`, `docker://`). Executes JS actions, Docker
   actions (builds/pulls the action's own image), composite actions
   (recursively expands into the step graph). JS actions are implemented
   (see "JS Actions Runtime" below); Docker and composite are sequenced
   after.

   **Correction (2026-09-14):** an earlier version of this section claimed
   act resolves a specific Node *version* per action (citing
   `GetNodeToolFullPath` as doing per-version resolution). Verified against
   act's actual source and that's wrong — `GetNodeToolFullPath`
   (`pkg/runner/run_context.go`) just execs `node -e "console.log(process.execPath)"`
   inside the already-running container to find whatever Node happens to
   be on `PATH`, and treats `node12`/`16`/`20`/`24` identically
   (`pkg/model/action.go`'s `IsNode()`) — no per-version binary selection
   anywhere in act. mirror-gha follows the same simplification: one pinned
   Node build, used for every JS action in a job regardless of declared
   `runs.using`. See "JS Actions Runtime" for the actual implementation.

   Composite actions must also each get their own nested execution
   context — a composite action's steps can themselves `uses:` further
   actions, so this is a recursive expansion, not a flat inline splice.

4. **GitHub API Shim** — implements `GITHUB_TOKEN`-scoped REST endpoints,
   plus checks/deployment/OIDC endpoints. **Hybrid network mode**: without a
   token, calls return structurally-real mock responses (fixtures matching
   GitHub's actual API schemas); with a token, the shim proxies through to
   real GitHub so token-dependent actions genuinely work.

5. **Dashboard Server** (`internal/dashboard`) — local HTTP server
   (`mirror dashboard`), serves a web UI in the default browser reading the
   same run-state the CLI produces (live log streaming, run history, job
   graph). No separate process, no Electron/native-window layer — same
   binary, a `dashboard` subcommand. `mirror run` alone is always
   full-parity and scriptable, independent of whether the dashboard is open.

## Job workspace

**Implemented.** A real repo directory is bind-mounted into every job —
not a copy, a direct mount — at `/github/workspace` (act's own
convention). Default source is wherever `mirror run` was invoked from;
overridable via `--workdir`. Exposed to steps three ways: the
`github.workspace` expression context, the `GITHUB_WORKSPACE` env var,
and as the default step working directory whenever
`working-directory`/`defaults.run.working-directory` isn't set.

This was a prerequisite gap the original architecture missed entirely —
jobs had no repo mounted at all, so every step only ever touched `/tmp`.
It's also the foundation the Actions Runtime needs: `actions/checkout`
and `hashFiles()` both require somewhere real to operate on.

Known fidelity caveat, not yet addressed: the container runs as root, so
files a step writes can end up root-owned on the host on Linux (not an
issue on macOS via Docker Desktop's filesystem layer).

## JS Actions Runtime

**Implemented.**

**Scope:** `uses:` resolving to a JS action, either Marketplace
(`owner/repo[/subpath]@ref`) or local (`./path`, workspace-relative).
Docker actions (`docker://...`) and composite actions are separate,
already-sequenced follow-ups — a non-JS `runs.using` (`docker`,
`composite`) is rejected with a clear error, not approximated.

Every design decision below was checked against act's actual source
(`nektos/act`, per explicit direction to treat it as the reference
implementation for exactly this kind of choice), not just inferred.

**Step model:** `Step` gains `Uses string` and `With map[string]string`.
A step with both `run:` and `uses:` set is a hard error (real GitHub
Actions disallows it too, and silently preferring one would hide a
workflow-authoring mistake). `with:` values get `${{ }}` expression
substitution before use, same as `run:` commands.

**Action resolution and caching** (`internal/actions`, new package):
- `owner/repo[/subpath]@ref` fetches via GitHub's codeload tarball
  endpoint — `https://codeload.github.com/<owner>/<repo>/tar.gz/<ref>`
  resolves tags, branches, and commit SHAs uniformly, no auth needed for
  public actions.
- **Fetches shell out to `curl`, and extraction to `tar`** — deliberately
  not Go's `net/http`. This machine's corporate network already broke Go's
  own TLS trust this session (`go get` failed on a cert-verification error
  `curl` sailed through fine — Go on Darwin doesn't reliably pick up the
  system trust store the way `curl`'s Security-framework-backed stack
  does). Checked whether act avoids this: it doesn't — act uses `go-git`
  (`pkg/runner/action_cache.go`), whose HTTP transport is itself built on
  `net/http`, so it would hit the identical wall on a Netskope-intercepted
  network. The `curl`/`tar` shell-out isn't an arbitrary preference; it's
  solving a problem act's typical network environment doesn't have, and it
  matches the same shell-out pattern `install.sh` already uses.
- Cached at `~/.cache/mirror-gha/actions/<owner>/<repo>/<ref>/`, keyed by
  the literal ref string — a mutable branch ref like `@main` won't
  auto-refresh. Matches act's own cache-by-ref behavior
  (`~/.cache/act`, `pkg/runner/run_context.go`) and its same staleness
  tradeoff, accepted rather than solved.
- `action.yml`/`action.yaml` parsed into `{Name, Inputs map[string]
  {Description, Required, Default}, Outputs, Runs{Using, Main}}` —
  matches act's own model shape (`pkg/model/action.go`).

**Node runtime — one pinned build, not per-version resolution.** Checked
against act's `GetNodeToolFullPath` (`pkg/runner/run_context.go`) and
`IsNode()` (`pkg/model/action.go`): act does not resolve a specific Node
version per action at all. It probes whatever Node binary is already on
`PATH` inside the container and uses that uniformly for every
`node12`/`16`/`20`/`24` action. mirror-gha follows the same
simplification — download and cache **one** pinned Node LTS build
(`~/.cache/mirror-gha/node/`, via the same `curl`+`tar` mechanism, arch
selected from `runtime.GOARCH` under the documented assumption that the
container's architecture matches the host's, true for default Docker
Desktop behavior), and use it for every JS action in a job regardless of
its declared `runs.using`. This drops an entire dimension of complexity
(multi-version toolcache) that act itself doesn't have either.

**Getting Node and action source into the container — `docker cp`, not a
pre-declared mount.** Checked act's actual injection mechanism
(`pkg/container/docker_run.go`'s `CopyTarStream`): it uses the Docker
Engine API's `CopyToContainer` — the SDK equivalent of `docker cp` — to
inject action source into the already-running container on demand, per
step, not via a mount declared at container-start. Since mirror-gha
already shells out to the `docker` CLI rather than using the SDK, the
direct equivalent is `docker cp <hostPath> <containerID>:<destPath>`
immediately before the step that needs it. `runner.Job` gains one new
method for this: `CopyToContainer(ctx, hostPath, containerPath) error` —
implemented as a real `docker cp` in `LinuxDockerBackend`'s job type, a
no-op in `DryRunBackend`'s. No change needed to `Backend.StartJob` at
all — simpler than the pre-mount design originally considered.

**Execution flow for a `uses:` step:** resolve the action (local path
under the workspace, or fetch+cache remote) -> parse `action.yml` ->
reject non-Node `runs.using` with a clear error -> ensure the pinned
Node build is cached, `docker cp` it into the container once per job at
the fixed path `/mirror-node` (the first JS-action step in a job
triggers this; later JS steps in the same job reuse it, tracked by a
local bool in the job's step loop) -> `docker cp` this step's resolved
action source to `/mirror-actions/<step-id>` (the same step ID already
computed for `steps.<id>.*` context lookups — unique per step, so
distinct actions used in the same job never collide) -> compute
`INPUT_*` env vars (exact transform
verified against act's `pkg/runner/action.go`: `"INPUT_" +
regexp("[^A-Z0-9-]").ReplaceAllString(strings.ToUpper(key), "_")` —
uppercase, dashes preserved, everything else non-alphanumeric becomes
`_`; action-metadata `default` values fill in inputs `with:` doesn't
set) plus `GITHUB_ACTION_PATH` -> exec
`<node>/bin/node <action>/<main>`.

Outputs and env updates need **zero** new plumbing: JS actions write via
`$GITHUB_OUTPUT`/`$GITHUB_ENV`, the same file-based protocol every
`run:` step already uses. `working-directory:` isn't valid on `uses:`
steps in real GitHub Actions either — `uses:` steps always execute
against the job workspace, no override.

## Data flow

```
mirror run [workflow.yml] [--event push --payload event.json]
  -> Workflow Engine: parse YAML, resolve triggers/matrix, build job DAG
  -> for each job (topological order, respecting `needs`):
      -> resolve `runs-on` -> pick Runner Backend
         (v1: Linux only; others error with Phase 2/3 TODO message)
      -> Actions Runtime: resolve every `uses:` in the job
         (fetch from cache or Marketplace/git, unpack JS/Docker/composite)
      -> Runner Backend: spin container, inject contexts as env +
         GITHUB_ENV / GITHUB_PATH / GITHUB_OUTPUT / GITHUB_STEP_SUMMARY
         files (the real file-based command protocol GitHub uses today,
         not the deprecated stdout `::set-output::`)
      -> step-by-step execution, streaming stdout/stderr live to CLI and
         (if open) Dashboard Server via a shared event bus
      -> GitHub API Shim intercepts GITHUB_TOKEN-authenticated calls —
         mock or proxy depending on hybrid mode
      -> on completion: persist run record (status, logs, artifacts, cache
         entries) to `~/.mirror/runs/<id>/`
  -> Dashboard, if open, reflects the same run-store (live tail or replay)
```

**Artifacts and cache must be real local HTTP servers, not a filesystem
shim.** Comparing against act's `pkg/artifacts/` and `pkg/artifactcache/`
confirmed how this actually has to work: `actions/upload-artifact` and
`actions/cache` don't read/write files directly — they call GitHub's real
Artifact/Cache REST API, at a URL the runner injects via
`ACTIONS_RUNTIME_URL` (and a matching runtime token) as job-scoped
environment variables. To work unmodified, this project's engine must run
a local HTTP server implementing that same API surface (upload/download/
finalize for artifacts; get/reserve/save for cache) and inject its own
`ACTIONS_RUNTIME_URL` pointing at it — a plain "watch the filesystem"
shim would not be intercepted by those actions at all, since they never
touch the filesystem directly for this. The storage backing that server
can still be the local filesystem, keyed the same way GitHub's real API
keys artifacts/cache entries (name + path + hash) — that part of the
original design holds, only the transport layer needed correcting.

## Phased feature-parity matrix

### Phase 1 (v1) — Linux backend, full breadth elsewhere

| Area | Coverage |
|---|---|
| Triggers | push, pull_request, workflow_dispatch, workflow_call, workflow_run, repository_dispatch — synthetic payload injection. `schedule` parsed but not daemon-scheduled (Phase 4) |
| Expressions/contexts | Full |
| Job features | needs, if, matrix, max-parallel, fail-fast, container, service containers, environment (name only, no approval gate), concurrency (local semantics), permissions (shim-enforced), timeout-minutes, continue-on-error, defaults |
| Actions | JS, Docker, composite — Marketplace + local path + `docker://` |
| Artifacts/Cache | Local shim, real upload/download/cache semantics |
| Workflow commands | GITHUB_ENV/PATH/OUTPUT/STEP_SUMMARY files, `::group::`, `::error/warning/notice::`, secret masking, ACTIONS_STEP_DEBUG |
| GITHUB_TOKEN | Mocked permissions object; hybrid mode proxies real calls with a supplied token |
| Dashboard | Run history, live log stream, job DAG view (free, view-only) |

### Phase 2 — Windows backend
Hyper-V/QEMU, tracks `actions/runner-images: windows-latest` toolchain.
Cross-host execution (running a Windows job from a non-Windows box) stays a
documented limitation — requires a real Windows VM the user provisions,
licensing can't be waved away by this tool.

### Phase 3 — macOS backend
Tart/UTM, Apple Silicon hosts only, per the hard technical constraint above.
This is a documented boundary, not a bug to fix later.

### Phase 4+ — Platform-depth features
Environment approval-gate simulation, real OIDC federation test harness
(AWS/GCP/Azure), opt-in publish-to-real-PR-checks (hybrid token), scheduled
watch-mode daemon, deployment-status round-trip, build attestations,
runner-image drift alerts.

## Monetization (open-core)

- **Free (CLI):** everything in Phase 1-3 — workflow engine, all OS backends
  once built, all three action types, artifacts/cache, hybrid network mode,
  basic dashboard. Core promise ("run your real CI locally") is never
  paywalled.
- **Pro:** Phase 4 ops/collaboration features — environments + approvals,
  OIDC test harness, checks-publishing, secrets vault UI, watch-mode daemon,
  advanced dashboard (diffing, search, runner-image drift alerts).

  **Decision (2026-09-14):** act already ships basic watch-mode
  (`--watch`, re-run on file change) for free, which put this Pro-tier
  placement in tension with the incumbent. Deliberately kept watch-mode
  Pro-only anyway — the bet is that mirror's free-tier fidelity
  advantages (accurate `needs`/`matrix`, real job-container lifecycle,
  actionlint-grade expressions) carry the free tier regardless of this
  one feature being weaker than act's. Revisit if free-tier adoption
  data suggests this is actually costing conversions.

## Error handling / fidelity-gap surfacing

Core rule: **never silently degrade.** Anywhere real GitHub behavior can't be
exactly replicated (unsupported host OS, no token so OIDC/checks are mocked,
`schedule` not daemon-driven, an unavailable service), the engine tags the
run/step with a structured `FidelityWarning`, surfaced in both CLI
(`⚠ approximated: ...`) and a dedicated dashboard panel — never buried in
logs, never a quiet wrong-but-green result.

Execution failures map 1:1 to GitHub's own semantics — `continue-on-error`,
`if: failure()`, matrix `fail-fast` all behave identically to hosted runs,
since the DAG engine is the same code regardless of backend.

## Testing approach

- **Golden-run comparison**: run the same workflow on real GitHub-hosted
  runners and the local engine for a curated corpus, diff structured results
  (exit codes, GITHUB_OUTPUT values, artifact contents, step order/timing
  shape) — not raw logs. Divergence is either a bug or an undeclared fidelity
  gap; both get fixed (fix the bug, or add the `FidelityWarning`).
- **Contract tests against `actions/runner-images`**: scheduled job diffs the
  published Ubuntu runner-image manifest against the local Docker image's
  installed toolchain, fails CI on drift, forces a version-bump PR. Extends
  to Windows/macOS backends once built.
- **Unit/integration tests per module**: expression evaluator and DAG builder
  get GitHub's documented expression-language edge cases as fixtures; Actions
  Runtime gets integration tests against real popular actions (checkout,
  setup-node, upload-artifact); GitHub API Shim gets contract tests against
  GitHub's OpenAPI spec so mocked responses stay schema-valid.

## Naming

Product: **mirror-gha**. CLI: **`mirror`**. Deliberately avoids "act"
(collides with the incumbent `nektos/act`) and avoids a "CI" suffix (GHA
covers ChatOps/automation/release workflows, not just CI pipelines).

## Open items for next session

- Implementation plan (via writing-plans skill) for Phase 1.
- Name collision check (npm/GitHub/domain) for `mirror`/`mirror-gha` before
  any public registration.
- Decide secrets-input mechanism for v1 (local file vs OS keychain) — not
  yet specified.
