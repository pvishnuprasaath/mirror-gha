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

## Docker Actions Runtime

**Implemented.**

**Scope:** `uses:` resolving to a Docker action (`runs.using: docker`) —
a raw `docker://image:tag` reference, or a Marketplace/local action
whose `runs.image` names a Dockerfile shipped alongside its
`action.yml`. Composite actions remain a separate, still-unscheduled
follow-up — a non-Docker, non-JS `runs.using` is still rejected with a
clear error.

Every design decision below was checked against act's actual source
(`nektos/act`, cloned fresh for this sub-project), per the same
standing direction as the JS Actions Runtime work.

**Architectural fit — a separate sibling container, not `docker exec`
into the job container.** This was the open question this sub-project
most needed act's source to answer, since it's the one place Docker
actions can't reuse the JS-action pattern (copy a binary in, exec it in
the job's own container) — a Docker action's image is frequently a
completely different base OS than the job's own `ubuntu:22.04`.
Confirmed directly in act's source: `pkg/runner/step_docker.go`'s
`runUsesContainer` (raw `docker://` steps) and `pkg/runner/action.go`'s
`execAsDocker` (local/Marketplace Docker actions) both build a fresh
`container.NewContainerInput` and run `Pull -> Remove(if !reuse) ->
Create -> Start(true)`, blocking until the container exits, then
`Remove` in `Finally()` — functionally `docker run --rm <image>` and
wait, once per step. This is a genuine divergence from JS actions
(`action.go`'s `rc.execJobContainer(...)` execs *inside* the existing
job container) that exists in act's own code too, not something
mirror-gha is introducing — confirming the sibling-container model is
the correct fit, not a compromise.

**Image resolution:**
- `docker://image:tag` is used directly, with no explicit pull step —
  `docker run` already auto-pulls a missing image on demand, so
  mirror-gha skips the explicit `Pull()` step act's code has for this
  case (act's own `ForcePull` config flag has no mirror-gha equivalent
  yet either — YAGNI until a real need for it shows up).
- A local/Marketplace action whose `runs.image` names a Dockerfile
  (typically the literal string `Dockerfile`, resolved relative to the
  action's own source directory — never the job workspace) is built via
  `docker build`. Tagged `mirror-gha-<sanitized-action-ref>:latest`,
  where sanitizing replaces every non-alphanumeric character with `-` —
  matches act's own tagging scheme in `action.go` (`act-<sanitized>-
  dockeraction:latest`) closely enough to keep the same debuggability
  (`docker images` shows which local image belongs to which action)
  without literally reusing act's `act-` prefix.
- Cache check via `docker image inspect <tag>` before rebuilding — a
  hit skips the build entirely. No arch-mismatch detection (act's
  version checks this because it supports multiple runner
  architectures; mirror-gha's single Linux Docker backend doesn't need
  it yet) and no `ForceRebuild` flag (YAGNI, same reasoning as
  `ForcePull` above).

**Container invocation:**
- `runs.entrypoint`/`runs.args` from `action.yml`, overridable by
  `with: {entrypoint, args}` exactly like act (`step.With["entrypoint"]`
  / `step.With["args"]` win when set) — `args` gets `${{ }}` expression
  substitution first, same as every other `with:` value.
- `with:` inputs become `INPUT_*` env vars via the **same transform**
  already implemented for JS actions (`internal/actions.InputEnv` is
  generic over `runs.using` already — confirmed against act's
  `populateEnvsFromInput`, which uses the identical transform
  regardless of action type). No new input-mapping code needed here.
  act's Dockerfile-specific extra step, `evalDockerArgs` (which also
  injects raw unprefixed input keys plus `runs.env` for
  `${{ inputs.x }}`-style Dockerfile `ARG`/`ENV` substitution), isn't
  replicated — no evidence any real action needs it beyond act's own
  Dockerfile-templating convenience, and it would be new surface area
  with no current caller.
- `runs.env` (action-level static env vars) merged in alongside
  `INPUT_*` and `GITHUB_*`.

**Mounts and networking** (`dockerJob.RunDockerAction`, new method):
- The job's own workspace directory, bind-mounted read-write at
  `/github/workspace` — same host path as the job container's own
  mount, so a Docker action sees exactly the same files a `run:` step
  would. Requires `dockerJob` to start storing `hostWorkspaceDir` (not
  currently kept — `StartJob` only threads it through to the initial
  `docker run`, this sub-project is the first caller that needs it
  again afterward).
- The action's own source directory, bind-mounted read-only at
  `/mirror-actions/<step-id>` (same path convention as JS actions) —
  set as `GITHUB_ACTION_PATH`. A direct bind mount, not `docker cp`,
  since this container is created fresh for this one step and never
  reused — no need to inject into an already-running container the way
  JS actions inject into the long-lived job container.
- This step's `FilesDir`, bind-mounted at `/mirror-files`, with
  `GITHUB_ENV`/`GITHUB_PATH`/`GITHUB_OUTPUT`/`GITHUB_STEP_SUMMARY`
  pointed at files under it — same file-based workflow-command protocol
  every other step type already uses, so output/env-file parsing in
  `RunJob` needs zero changes.
- `--network container:<jobContainerID>`, so a Docker action can reach
  `localhost` the same way a `run:` step in the job container can —
  matches act's `NetworkMode` exactly
  (`fmt.Sprintf("container:%s", rc.jobContainerName())` in
  `run_context.go`).
- `/var/run/docker.sock` bind-mounted in unconditionally, matching
  act's and real GitHub-hosted runners' own behavior (confirmed: act
  does this for every Docker action, no opt-in gate in its source).
  **This is a real, explicit trust decision, not an oversight**: any
  Docker action — including an unmodified Marketplace one — gets full
  host Docker daemon control the moment it runs, matching what real
  GitHub Actions runners already do. Documented in `docs/usage.md` as a
  trust caveat, not silently shipped.

**Interface shape:** `runner.Job` gains `RunDockerAction(ctx,
DockerActionSpec) (StepResult, error)`, alongside the existing
`CopyToContainer`/`Exec` methods — deliberately not folded into `Exec`,
since a Docker action needs its own image/network/mount set rather than
argv into the already-running job container. `DockerActionSpec` carries
`{Image, Entrypoint, Args, Env, ActionSourceDir,
ActionPathInContainer, FilesDir}`. `dryRunJob.RunDockerAction` is a
no-op fake success, matching `dryRunJob.CopyToContainer`'s existing
pattern — dry-run mode never touches Docker at all for any step type.

`internal/engine/uses_step.go`'s `prepareUsesStep` now dispatches on
`metadata.Runs.Using`: a `node*` prefix keeps today's argv-into-job-
container path unchanged; `docker` resolves/builds the image and
returns a `DockerActionSpec` instead of argv; anything else is still
rejected. `RunJob`'s step loop branches once on which of the two came
back and calls `Exec` or `RunDockerAction` accordingly — everything
downstream (output-file parsing, legacy stdout-output parsing, the
`GITHUB_ENV` merge) stays exactly as it is today, since none of it
cares how a step actually ran.

**Platform scope:** Linux Docker daemon only, consistent with
mirror-gha's current single backend — checked act's source for
Docker-action-specific platform gating and found none beyond requiring
a working Docker daemon at all (its own OS-specific build tags gate the
whole container package, not Docker actions in particular). No new
restriction needed beyond what already exists.

## Composite Actions Runtime

**Implemented.**

**Scope:** `uses:` resolving to a composite action (`runs.using: composite`)
— its own `action.yml` carries a `runs.steps:` list of nested `run:`/
`uses:` steps (which may themselves be JS, Docker, or another composite
action, recursively). This is the last of the three `runs.using` kinds —
after this, no `runs.using` value is rejected as unsupported anymore.

Every design decision below was checked against act's actual source
(`nektos/act`, cloned fresh for this sub-project).

**Architectural fit — the same step-execution machinery, not a
parallel executor.** This was the central question this sub-project
needed act's source to answer. Confirmed directly:
`action_composite.go`'s `compositeExecutor` builds each nested step via
`stepFactoryImpl.newStep` — the **exact same** factory
`job_executor.go` uses for a job's own top-level steps
(`job_executor.go:76`). Composite nesting in act is nothing more than
"call the same step dispatch against a different `RunContext`" — no
separate isolation boundary either: `newCompositeRunContext` reuses the
**same `JobContainer`** as the parent, so a composite action's steps run
in the same container a `run:` step would. mirror-gha follows this
exactly: `RunJob`'s per-step execution body (the `if:` check, env/file
setup, dispatch to `Exec`/`RunDockerAction`, output parsing, and
bookkeeping) is extracted into a reusable `runStep` function that both
the top-level job loop and a new `runCompositeSteps` call — not two
independent implementations of "run a step."

**The `inputs` context is not new storage — it's `INPUT_*` env vars
re-exposed.** Checked act's `getEvaluatorInputs`
(`expression.go:481`): `for k, v := range env { if
strings.HasPrefix(k, "INPUT_") { inputs[...] = v } }`. It isn't
composite-specific machinery at all — any `RunContext`'s env
produces an `inputs.*` context generically; it's simply empty outside
an action invocation, since normal job steps never set `INPUT_*` on
themselves. mirror-gha's `Context.resolvePath` gains an `"inputs"`
case that reverses the same `INPUT_<TRANSFORMED_NAME>` transform
already used everywhere else (`internal/actions.InputEnv`) against
`Context.Env` — duplicated locally rather than cross-imported, same
precedent as `requireDocker`/`requireNetwork` being duplicated
per-package throughout this project. This is a structural
requirement, not a nice-to-have: without it, a composite action's own
nested steps have no way to reference its declared inputs at all,
making composite support close to useless.

**Nested step namespace and output surfacing.** act's composite
sub-context gets a fresh `StepResults` map (`newCompositeRunContext`,
`action_composite.go:47`) — a composite's nested step IDs never
collide with or leak into the calling job's `steps.*`. After the
nested steps finish, the composite's own declared `outputs:` (each a
`value: ${{ steps.x.outputs.y }}` expression, evaluated **against the
nested scope**) get written onto the *parent* context as the outer
`uses:` step's own output (`action_composite.go:102-107`,
`rc.setOutput(...)`). mirror-gha mirrors this with a child `Context` —
fresh `Steps` map, `Env` seeded from the parent's env plus this
composite's own `INPUT_*` vars and its own `GITHUB_ACTION_PATH` — and
evaluates each `ActionOutput.Value` against that child context once
`runCompositeSteps` returns, surfacing the results as the calling
`uses:` step's `steps.<id>.outputs`.

**Metadata shape changes** (`internal/actions/metadata.go`):
- `ActionRuns` gains `Steps []ActionStep` — a small struct local to
  `internal/actions` (`ID`, `Name`, `Run`, `Uses`, `Shell`,
  `WorkingDirectory`, `With`, `Env`, `If`, `ContinueOnError`),
  deliberately not `engine.Step` itself: `internal/engine` already
  imports `internal/actions`, so the reverse import would cycle.
  `internal/engine/uses_step.go` converts each `ActionStep` into an
  `engine.Step` at the call site — a small, one-directional conversion,
  not duplicated execution logic.
- `ActionMetadata.Outputs` changes from `map[string]interface{}` to
  `map[string]ActionOutput{Description, Value string}`. `Value` is the
  one genuinely new field: for JS/Docker actions `outputs:` is purely
  descriptive (the action itself writes `$GITHUB_OUTPUT` directly), but
  a composite action's outputs are *computed* from its own `Value`
  expression — this field was structurally absent because nothing
  needed it before now.

**Shell/working-directory defaults are not inherited.** Checked act's
`step_run.go:168`: `step.WorkflowShell =
rc.Run.Job().Defaults.Run.Shell` — for a composite's nested step, `rc`
is the child context, whose synthetic `Job()` is an empty
`model.Job{}` (`newCompositeRunContext`), so this always resolves to
`""` and falls through to the OS/container shell-detection fallback,
**never** the calling workflow's `defaults.run.shell`. mirror-gha's
`runCompositeSteps` calls `runStep` with a synthetic empty `&Job{}`/
`&Workflow{}` (no `Defaults`) for exactly this reason — `effectiveShell`/
`effectiveWorkingDirectory` already fall back to the built-in default
when nothing is set, so no new fallback logic is needed, just the right
(empty) inputs to the existing one.

**Recursion — a deliberate divergence from act, not parity.**
Composite-in-composite works for free in act's model (the same dispatch
recurses on `runs.using` again for any nested `uses:`), and act's source
has no depth cap or cycle detection anywhere in
`action_composite.go`/`step_action_local.go` — unbounded, by omission.
mirror-gha adds a fixed recursion-depth cap (10) with a clear error
instead of matching this exactly: act's action references are content-
addressed fetches over the network, making a self-referencing cycle
rare in practice; mirror-gha's local-path (`./`) resolution makes a
composite action accidentally referencing itself a real, easy-to-hit
crash (unbounded Go call-stack recursion) rather than a theoretical one.
No real action nests anywhere near 10 deep — this is cheap insurance,
not a functional limitation.

**Node runtime and job-container sharing.** The same per-job `nodeReady`
bool already threaded through `prepareUsesStep` is threaded through
`runCompositeSteps` too — a JS action nested inside a composite action
shares the one pinned Node copy-in per job, not one per composite
invocation, consistent with how top-level JS steps already behave.

## `--local-repository`

**Implemented.**

**Scope:** override local action resolution for testing an in-progress
action before merging/tagging it — the same problem act's own
`--local-repository` flag solves.

**Exact flag, matched to act's syntax** (`cmd/root.go:130`):
`--local-repository owner/repo[@ref]=local/path` (or a full URL in place
of `owner/repo`), repeatable. act parses each value via
`strings.Cut(l, "=")`, matching an override on `owner/repo@ref` (or the
full URL form). mirror-gha's `mirror run` gains the identical flag,
parsed into a `map[string]string` keyed the same way.

**Wiring — a decorator in front of the real fetch, not a code path
inside it.** act's implementation, `LocalRepositoryCache`
(`pkg/runner/local_repository_cache.go:19`), wraps the real
`ActionCache`: `Fetch` checks the override map first, and only falls
through to the real network fetch on a miss. mirror-gha follows the
same shape: the override map is threaded through `JobRunOptions` into
`prepareUsesStep`, checked before calling `actions.FetchRemote` — a
hit returns the local path directly, skipping the fetch/cache/tar-
extract path entirely, with no change to `FetchRemote` itself.

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
