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

## Cache Runtime

**Implemented.**

**Scope:** `actions/cache` (save/restore) working for real. Artifacts
(`actions/upload-artifact`/`download-artifact`, v3 and v4) are a
separate, still-unscheduled follow-up — deliberately split out given
the size of this feature area, per the "Artifacts and cache must be
real local HTTP servers" finding above.

Every design decision below was checked against act's actual source
(`nektos/act`, `pkg/artifactcache/`), per the standing direction to
treat it as the reference implementation for exactly this kind of
choice.

**No new action-type dispatch — this is the elegant part.**
`actions/cache` is itself a bundled JS action; it runs through
mirror-gha's *existing* JS-actions machinery (`prepareUsesStep`'s
`node*` branch) completely unmodified. Supporting it isn't "teach the
engine about a fourth action kind" — it's "run an HTTP server and set
two env vars," and the action's own bundled `@actions/cache` client
does the rest. No change needed to `prepareUsesStep`'s dispatch at all.

**API surface — matched to act's real routes, which match
`@actions/cache`'s actual client expectations** (`pkg/artifactcache/
handler.go:98-103`, confirmed against act's source rather than GitHub's
public docs, which don't fully describe this internal-only API): `GET
/_apis/artifactcache/cache?keys=...&version=...` (lookup), `POST
/_apis/artifactcache/caches` (reserve), `PATCH
/_apis/artifactcache/caches/{id}` (chunked upload via `Content-Range`),
`POST /_apis/artifactcache/caches/{id}` (commit/finalize), `GET
/_apis/artifactcache/artifacts/{id}` (blob download — the URL the
lookup response's `archiveLocation` points at), `POST
/_apis/artifactcache/clean` (no-op — satisfies the client's optional
call, matches act not implementing real cleanup logic here either).

**Restore-keys matching is a real reimplementation, not simplified
away.** act's `findCache` (`handler.go:372-404`): for each key in the
ordered list (the primary key, then `restore-keys` fallbacks in order),
try an exact match first; on a miss, an anchored-regex prefix match
(`^` + `regexp.QuoteMeta(prefix)`) against every stored key, sorted by
creation time descending (most-recent-wins). This is GitHub's actual
matching semantics, not a stub — real workflows depend on prefix
fallback behaving exactly this way (e.g. restoring the closest
dependency-lockfile-keyed cache when today's exact hash doesn't exist
yet). mirror-gha reproduces this exactly.

**Storage persists across invocations — this is not optional.** A
cache's entire purpose is surviving to a *later* run; an ephemeral
per-`mirror run` store would make `actions/cache` permanently miss and
defeat the feature. Blobs plus a JSON index (key, version, size,
createdAt) live at `~/.cache/mirror-gha/action-cache/`, parallel to the
existing action-source and Node caches. Deliberate divergence from
act's BoltDB-backed index (`pkg/artifactcache/storage.go`): a plain
JSON file behind an in-process mutex needs no new dependency (matches
every prior sub-project's Go-stdlib-only constraint) and is sufficient
for a single local CLI process — two concurrent `mirror run` invocations
racing the same store is an accepted, documented risk, not solved here.

**Server lifecycle — once per `mirror run` invocation, not per job.**
Checked against act's own lifecycle (`cmd/root.go:679-687`): both of
act's servers start once per `act` invocation, before any job runs, not
per-job — the opposite of how mirror-gha's Docker-action containers
work (spun up and torn down per step). mirror-gha's cache server
follows act's shape here: started in `cmd/mirror/main.go`'s
`runCommand`, before `RunWorkflow`, stopped via `defer` after it
returns — skipped entirely in `--list`/`--graph`/`--dryrun` modes,
which already never touch Docker.

**Networking — `host.docker.internal`, a deliberate divergence from
act's outbound-IP-binding trick.** act binds
`common.GetOutboundIP()` (`cmd/root.go:118`) — the host's real outbound
network IP — so a Linux container can reach back to the host process
without special networking. This project's actual development and
testing environment is macOS with Docker Desktop throughout (every
example workflow this session was verified against it), where
`host.docker.internal` is a built-in DNS alias resolving to the host
from inside any container — simpler and more portable across Docker
Desktop (macOS/Windows) than IP-autodetection, at the cost of not
working out of the box against plain Linux `dockerd` without extra
`--add-host` configuration. Documented as a known limitation (fixable
later with act's own outbound-IP fallback if Linux-host support becomes
a priority), not silently assumed to work everywhere. The server binds
`0.0.0.0:0` (OS-assigned port, avoiding collisions across concurrent
local runs) and `ACTIONS_CACHE_URL` is built as
`http://host.docker.internal:<port>/`.

**Env injection — one shared layer, not per step type.** Checked
against act's `withGithubEnv`/`setActionRuntimeVars`
(`run_context.go:1027,1075`): the cache/artifact env vars are set once,
at the same base-env layer every step type already shares — not
branched per `run:`/JS/Docker/composite step type. mirror-gha follows
this exactly: `ACTIONS_CACHE_URL` and a fixed placeholder
`ACTIONS_RUNTIME_TOKEN` (no real auth backend exists to validate a
token against — `@actions/cache`'s client just needs *a* non-empty
bearer value present, matching act's own approach of a locally-signed
but not really gatekept token) are merged into every job's `Context.Env`
at the same point `wf.Env`/`job.Env` already merge in `NewContext`, so
every step — regardless of kind — sees them identically.

**Testing:** unit tests per route via Go's `httptest` (reserve/upload/
commit/lookup/restore-key-matching), no Docker needed for these. Real
end-to-end proof: the actual `actions/cache@v4` action doing a save
then a restore across two *separate* `mirror run` invocations of the
same workflow, verifying a genuine cache hit on the second run — this
exercises the full real stack (a real JS action, a real HTTP call from
inside the container back to the host server, real disk persistence
across process invocations) with no fakes anywhere in the chain.

## Artifacts Runtime

**Implemented.**

**Scope:** `actions/upload-artifact` and `actions/download-artifact`
working for real — both the legacy v3 REST protocol and the current v4
protocol, together in one sub-project (unlike Cache Runtime, this one
wasn't split further — the two protocols share one server and one
storage layer, so splitting them would mean building the same
scaffolding twice).

Every design decision below was checked against act's actual source
(`nektos/act`, `pkg/artifacts/`), per the standing direction to treat
it as the reference implementation.

**No new action-type dispatch — same property Cache Runtime already
established.** `actions/upload-artifact`/`download-artifact` are
themselves bundled JS actions; they run through mirror-gha's existing
JS-actions machinery completely unmodified once the right env vars are
present. Nothing in `prepareUsesStep`'s dispatch changes.

**Two real protocol generations, confirmed field-for-field against
act's source, not assumed from `@actions/toolkit` memory:**

- **v3** (`actions/upload-artifact@v3` and earlier) — a REST-ish
  protocol: `POST /_apis/pipelines/workflows/:runId/artifacts` reserves
  a container (`{fileContainerResourceUrl}`), `PUT
  /upload/:runId?itemPath=...` uploads raw bytes (`Content-Range` for
  chunked uploads, `Content-Encoding: gzip` marked by a literal
  `.gz__` filename suffix — the client controls compression, act
  doesn't compress server-side), `PATCH .../artifacts` finalizes as a
  pure no-op, `GET .../artifacts` lists (`{count, value: [{name,
  fileContainerResourceUrl}]}`), `GET
  /download/:container?itemPath=...` lists a container's files
  (`{value: [{path, itemType, contentLocation}]}`), `GET
  /artifact/*path` streams raw bytes (falling back to a `.gz__`-suffixed
  path if the plain one doesn't exist). All plain `encoding/json`,
  camelCase fields — no protobuf anywhere in v3.
- **v4** (current default) — routes at twirp-*shaped* URL paths
  (`/twirp/github.actions.results.api.v1.ArtifactService/{CreateArtifact,
  FinalizeArtifact,ListArtifacts,GetSignedArtifactURL,DeleteArtifact,
  UploadArtifact,DownloadArtifact}`) but genuinely plain HTTP+JSON
  underneath — act uses real protobuf-generated Go types
  (`google.golang.org/protobuf`) and `protojson` to (de)serialize them,
  but there's no actual twirp wire framing or protobuf-over-the-wire
  bytes involved, just JSON bodies at those specific paths. **A real
  correction found only by reading act's `.pb.go` files directly, not
  its own doc comment**: the top-of-file comment in act's
  `artifacts_v4.go` shows sample requests in snake_case
  (`workflow_run_backend_id`), but `protojson.Marshal` actually emits
  each field's `json_name` variant from the `.pb.go` struct tags, which
  is **camelCase** (`workflowRunBackendId`) — the doc comment is
  misleading, the generated code is ground truth.

**mirror-gha reproduces the v4 wire shape without a protobuf
dependency.** Since the actual wire format is plain JSON regardless of
what act uses internally to produce it, mirror-gha hand-writes
`encoding/json` structs with the same camelCase field names — no
`google.golang.org/protobuf` dependency, keeping the project's
zero-external-Go-dependency record intact (matching every prior
sub-project's "no new dependencies" constraint). `CreateArtifact` ->
`{ok, signedUploadUrl}`, `FinalizeArtifact` -> `{ok, artifactId}` (an
FNV-32a hash of the artifact name, matching act's own
`artifactNameToID` — not a real incrementing database id),
`ListArtifacts` -> `{artifacts: [...]}`, `GetSignedArtifactURL` ->
`{signedUrl}`, `DeleteArtifact` -> `{ok, artifactId}`.
`UploadArtifact`/`DownloadArtifact` carry the raw zip bytes (the real
v4 client always zips client-side before uploading — act's server
never unzips, it stores the blob as-is at
`<baseDir>/<runId>/<artifactName>/<artifactName>.zip`).

**Signed URLs are simulated, not real — and mirror-gha simplifies
further than act does here.** act's `CreateArtifact`/
`GetSignedArtifactURL` build a URL pointing back at its *own* server
(no real separate blob-storage origin), HMAC-signed with a hardcoded
static key purely to shape-match the real protocol's signed-URL
concept. mirror-gha points the signed URL back at its own server too,
but skips the HMAC signing entirely — there is no trust boundary to
protect on a local, single-user server, so replicating act's fake
signature would be complexity with no payoff, a deliberate divergence
beyond what act itself already simplified.

**Storage does not persist across `mirror run` invocations — the
opposite of Cache Runtime, for a real semantic reason.** A cache's
whole point is surviving to a later run; an artifact's isn't — real
GitHub Actions artifacts belong to one workflow run (produced by one
job, consumed by a later job in the *same* run, or downloaded
afterward via the API/UI). mirror-gha's artifact store is a fresh
`os.MkdirTemp` directory per `mirror run` invocation, not deleted when
the run finishes (its path is printed at the end of the run) so a user
can inspect what got uploaded — but never reused as a restore source
by a *later* invocation the way the cache store deliberately is.

**Server lifecycle and env injection — always-on, matching Cache
Runtime's own precedent, a deliberate divergence from act.** act's own
artifact server is opt-in (`--artifact-server-path`, empty by default —
no flag, no server, no env injection at all), unlike its cache server
(on by default). mirror-gha doesn't carry that inconsistency forward:
both servers start together, unconditionally, in the same place
`cmd/mirror/main.go`'s `runCommand` already starts the cache server —
consistent with this project's low-friction positioning ("things just
work locally, no flags to discover first"), which the Cache Runtime
sub-project already established as the project's own norm rather than
act's. `ACTIONS_RUNTIME_URL` and `ACTIONS_RESULTS_URL` are both set to
the artifact server's own base URL (matching act finding both env vars
carry the identical value — one URL serves v3 and v4 traffic, there's
no separate `ACTIONS_ARTIFACT_URL`), merged into the same
`JobRunOptions.ExtraEnv` map the cache server's own env vars already
populate. The existing fixed placeholder `ACTIONS_RUNTIME_TOKEN` is
reused as-is — no new token needed, since neither this server nor
act's own validates it.

**Testing:** unit tests per route (v3 and v4 separately) via `httptest`,
no Docker needed for these. Real end-to-end proof against both a
current (v4-protocol) and a legacy pinned (v3-protocol) version of the
real, unmodified `actions/upload-artifact`/`download-artifact` actions,
in a two-job workflow (one job uploads, a later job downloads) —
research flagged that exactly which real client version maps to which
protocol wasn't verified against a live client this session (only
inferred from act's own server-side routes), so this needs the same
"verify for real, fix what's found" discipline that caught three real
bugs during Cache Runtime's own end-to-end verification.

## Services and Container Runtime

**Scope:** job-level `container:` (swap the image the job's own container
runs as) and `services:` (sidecar containers reachable from job steps by
name), including registry `credentials:` for both. Every design decision
below was checked against act's actual source (`nektos/act`,
`pkg/model/workflow.go`, `pkg/runner/run_context.go`,
`pkg/container/docker_network.go`), per the standing direction to treat
it as the reference implementation.

**No separate "runner" container — act doesn't have one either.** A
common misconception: `container:` doesn't add a sidecar alongside some
fixed management container. act runs exactly one container per job
(`startJobContainer`); `container:` just changes which image that single
container uses (`platformImage()` reads `job.Container().Image` before
falling back to the `runs-on` default). mirror-gha mirrors this exactly:
`LinuxDockerBackend.StartJob` swaps `linuxRunnerImage` for
`containerSpec.Image` when non-empty, nothing else about job execution
changes.

**Model** — `internal/engine/workflow.go`: `Job` gains
`RawContainer yaml.Node` (tag `container`) and
`Services map[string]ContainerSpec` (tag `services`), plus a
`Job.Container() (*ContainerSpec, error)` method decoding either shape
GitHub Actions allows — a bare image string (`container: node:20`) or a
mapping (`container: {image: ..., env: ..., ...}`) — matching act's own
`yaml.Node`-kind-switch in its `Container()` method. `ContainerSpec{Image
string; Env map[string]string; Ports, Volumes []string; Options string;
Credentials map[string]string}` is reused for both `container:` and each
`services:` entry, matching act's own struct reuse.

**Networking — a per-job Docker network, only when `services:` is
present.** Docker's embedded DNS only resolves `--network-alias` names on
a user-defined network, never the default bridge — so whenever a job
declares `services:`, mirror-gha creates `docker network create
mirror-svc-<jobID>` before starting anything, attaches the job container
to it with `--network mirror-svc-<jobID>`, and attaches each service
container with `--network mirror-svc-<jobID> --network-alias <name>`.
Jobs with no `services:` are completely unaffected — no network is
created, the job container keeps using Docker's default network exactly
as it does today. `RunDockerAction`'s existing `--network
container:<jobContainerID>` (joining the job container's network
namespace for Docker-action steps) needs no change: it inherits whatever
network the job container is already on, custom or default. Confirmed
this matches act's own model (`networkName()` in `run_context.go`
computes the same conditional network-or-default logic; the actual
`docker network create` call is act's `pkg/container/docker_network.go`,
idempotent against an existing network of the same name).
`host.docker.internal` (needed for the cache/artifact servers) is
expected to keep resolving on a custom bridge network under Docker
Desktop — verified for real during this sub-project's own end-to-end
task, not assumed.

**Service startup and health wait.** Each service starts via `docker run
-d` with its own `-e`/`-p`/`-v` flags from `Env`/`Ports`/`Volumes`, plus
any `Options` tokens appended raw. After starting, mirror-gha polls
`docker inspect --format '{{.State.Health.Status}}'`: empty output (no
`HEALTHCHECK` defined on the image, and no `--health-cmd` supplied via
`Options`) means ready immediately; `"healthy"` means ready; anything
else polls up to 5 minutes (matching act's own `waitForServiceContainers`
timeout) before erroring. Services start once per job, before the job
container's steps run, and are torn down once per job — never per step.

**Registry credentials — `docker login`/`docker logout` around the pull,
since mirror-gha shells to the `docker` CLI rather than using act's
Docker Go SDK.** act validates `Credentials` as exactly two keys
(`username`, `password`), interpolates both as expressions, and feeds
them to the Docker SDK's `RegistryAuth` before pulling
(`pkg/runner/run_context.go`'s `handleCredentials`/
`handleServiceCredentials` -> `getImagePullOptions`). mirror-gha
replicates the validation (exactly `username`+`password`, error
otherwise) but the mechanism is necessarily different: `docker login
<registry> -u <username> --password-stdin` (password piped via stdin,
never `-p`, so it never appears in argv or shell history) runs once
before that image's first pull/run, and `docker logout <registry>` runs
during `dockerJob.Stop`. The registry host is parsed from the image
reference, defaulting to Docker Hub (`index.docker.io`) when the
reference has no dot, colon, or `localhost` prefix — matching act's own
default-registry heuristic. Same mechanism for `container:` and every
`services:` entry; act's legacy `DOCKER_USERNAME`/`DOCKER_PASSWORD`
secrets fallback (marked TODO-remove in act's own source) is not
replicated. **Known v1 limitation, documented rather than silently
wrong:** login/logout is process-wide (shared Docker daemon config, since
mirror-gha has no isolated per-job credential store the way the SDK's
in-memory `RegistryAuth` gives act) — two jobs in the same `mirror run`
pulling different private images concurrently could race. Acceptable for
a local single-workflow-run tool; flagged in `docs/usage.md`, not solved
in v1.

**`Options` needs quote-aware tokenizing.** Real-world values like
`--health-cmd "pg_isready -U postgres"` contain spaces inside quotes, so
a naive `strings.Fields` split would break the most common real use case
(Postgres/MySQL health checks). A small hand-rolled tokenizer (single-
and double-quote aware, no shell expansion, no external dependency) is
added rather than reaching for a shlex-style package — keeps the
project's zero-external-Go-dependency record intact, and the feature
surface (whitespace + quoted substrings) is small enough that a
hand-rolled scanner is genuinely simpler than a dependency would be.

**Backend/Job interface change.** `runner.Backend.StartJob` gains two
parameters: `containerSpec *ContainerSpec` and `services
map[string]ContainerSpec` (a new `runner.ContainerSpec` type, mirroring
the engine one but decoupled the same way `DockerActionSpec` already is —
the engine converts its own type to the runner's at the call site). Both
are nil/empty for every job without `container:`/`services:` — the
overwhelming majority of existing and future workflows — so every
existing test and call site updates mechanically, no behavior change for
the common case.

**Job-container-level `env`/`ports`/`volumes`/`options`.** `container.Env`
merges into `actx.Env` alongside `job.Env` (container wins on conflict —
a rare edge case, but the more specific scope should win, matching how
step-level settings already override job-level ones elsewhere in this
codebase). `container.Volumes`/`Ports`/`Options` become extra `-v`/`-p`/raw
args on the `docker run` that starts the job container itself.

**Testing:** unit tests for `Job.Container()` parsing (both shapes,
malformed input), the credentials validator, and the `Options` tokenizer
(plain tokens, quoted substrings with embedded spaces, mixed). Real
end-to-end verification: a `services: postgres` workflow doing a real
`pg_isready`/`psql` round trip from a step against the service by
hostname, and a `container: node:20` workflow running a step that
depends on that image's toolchain not being present in the default
`ubuntu:22.04` image (proving the swap is real, not a no-op).

## macOS Host Backend

**Scope:** `runs-on: macos-latest`/`macos-13`/`macos-14`/`macos-15`,
executing directly on the host process with no container at all — the
only architecturally honest option, since macOS cannot be virtualized or
containerized on non-Apple hardware (see "Hard technical constraint"
above). Checked against act's own host-execution mode
(`pkg/container/host_environment.go`, `pkg/runner/run_context.go`) per
the standing direction to treat it as the reference implementation,
though act's version is a generic, undocumented `-P
label=-self-hosted` escape hatch with no macOS awareness at all — this
sub-project gives mirror-gha a real, first-class macOS backend act
doesn't have.

**Backend selection gates on the actual host OS, not just the
label.** `runner.SelectBackend` maps the macOS `runs-on` labels to a new
`HostBackend` only when `runtime.GOOS == "darwin"`; on any other host OS
it returns a clear, specific error ("macOS jobs require running
mirror-gha on a Mac host") rather than attempting anything — matching
the spec's pre-existing hard constraint rather than act's silent
generic bypass, which has no OS check at all.

**No container, no bind mount — real `os/exec` directly on the host,
matching act's `HostEnvironment.exec`.** `HostJob.WorkspacePath()`
returns the real workspace directory unchanged (there is nothing to
mount into); `FilesRoot()` is a real host temp directory, and the
`GITHUB_ENV`/`OUTPUT`/`PATH`/`STEP_SUMMARY`/`STATE` workflow-command
files live at literal paths under it — no `/mirror-files`-style
container-path translation is needed for these at all, since there is
no container-side/host-side split left to bridge.

**One real translation problem: the `/mirror-node`/`/mirror-actions/<id>`
convention.** These are synthetic absolute paths the engine layer
(`internal/actions`) already bakes into `uses_step.go`'s JS-action
`Args`/`GITHUB_ACTION_PATH`, and into `Job.CopyToContainer`'s destination
argument — meaningful today only because the Docker backend bind-mounts
them inside an isolated container filesystem namespace. A bare host
process has no such namespace: writing to the literal path `/mirror-node`
on a real Mac would need root and would collide across concurrent or
repeated runs. `HostJob` keeps its own per-job real root directory and
rewrites any `/mirror-*`-prefixed path — `CopyToContainer`'s destination,
each JS-action `Args` entry, the `GITHUB_ACTION_PATH` env value — to
`filepath.Join(root, thatPath)` before executing, mirroring act's own
`ToContainerPath`-style host-path translation, scoped to the one path
family this codebase already uses for this purpose. Nothing else in a
step's `Args`/`Env`/`Command` is touched — this rewrite only ever applies
to strings this project itself generates with that exact prefix, never
to workflow-author-controlled content.

**Node runtime download must target the backend's platform, not
mirror-gha's own host OS — a real bug the naive fix would introduce.**
`internal/actions/node.go`'s `EnsureNode` currently hardcodes `linux` in
its download URL; that's correct today only because the Docker backend
always targets a Linux container regardless of what OS mirror-gha itself
is running on (this very project is developed and tested on a Mac
already, running Linux job containers). Naively switching that hardcode
to `runtime.GOOS` would break the *existing*, already-shipped Docker
backend on exactly this kind of Mac dev machine — it would fetch a
`darwin` Node binary and `docker cp` it into a Linux container. Fix: add
`Platform() string` to the `runner.Job` interface (`dockerJob` always
returns `"linux"`, matching its fixed container target regardless of
host; the new `hostJob` returns `"darwin"`), thread that value into
`EnsureNode(cacheRoot, platform)`, and add a `darwin` branch to its URL
construction (`https://nodejs.org/dist/v<ver>/node-v<ver>-darwin-<arch>.tar.gz`
— Node.js publishes real darwin tarballs with the identical internal
`bin/node` layout as the linux ones already handled).

**Scope cuts — all matching real GitHub Actions' own documented
constraints, not limitations mirror-gha is inventing.** `uses:
docker://...` steps, `container:`, and `services:` all hard-error clearly
on `HostBackend`: real GitHub Actions itself doesn't support Docker
container actions or container/service jobs on macOS runners either
(a documented upstream constraint, not something Linux-only about this
project specifically) — matching reality is more correct here than act's
own behavior, which silently still requires Docker for a `uses:
docker://...` step even inside its generic host-execution mode. Cross-host
macOS (requesting `macos-latest` from a non-Mac host) stays the
already-documented hard limitation from this spec's "Hard technical
constraint" section — a real Mac (or licensed VM) the user provisions,
not something this tool can wave away.

**Shell default divergence, worth flagging explicitly.** The Docker
backend defaults a `run:` step with no `shell:` to `sh` (a bare
`ubuntu:22.04` image's lowest common denominator). Real GitHub Actions'
own documented default for both Linux and macOS runners is `bash`, and
every real macOS ships one at a fixed path — `HostJob.Exec` defaults to
`bash` instead of `sh`, a divergence scoped to this backend only,
correcting toward real GitHub Actions behavior rather than introducing a
new inconsistency.

**Testing:** unit tests for the `/mirror-*` path-rewrite helper and
`Job.Platform()` across both backends. Real end-to-end verification on
this machine (the only place it's possible to verify for real, since
macOS execution requires an actual Mac host) — a `runs-on: macos-latest`
workflow running a plain `run:` step and a real JS action, proving Node
actually executes natively with no Docker involved anywhere in the path.

## Matrix Concurrency and `concurrency:` Groups

**Scope:** real, bounded concurrent execution of a job's matrix
combinations (`strategy.max-parallel`), plus `concurrency:` group
serialization/`cancel-in-progress` scoped to the one new race surface
this introduces — combinations of the *same* job whose resolved group
name happens to coincide. Checked against act's actual source
(`nektos/act`, `pkg/runner/runner.go`'s `NewPlanExecutor`,
`pkg/common/executor.go`'s `NewParallelExecutor`) per the standing
direction to treat it as the reference implementation — with two
corrections found and deliberately not carried over, detailed below.

**Scope correction from the original ask, made explicit:** `max-parallel`
only ever governs matrix combinations of one job — real GitHub Actions
has no equivalent knob for independent jobs, which run in parallel there
only bounded by runner availability, a concept that doesn't exist
locally. Independent jobs in mirror-gha stay exactly as sequential as
they are today; this is not a gap relative to real GitHub Actions, it's
matching it. Only matrix-combination concurrency is new here.

**Default cap when `max-parallel` is unset: 4, not unbounded — adopted
directly from act's own considered choice** (`pkg/runner/runner.go`,
paired with a rationale comment in `pkg/model/workflow.go`'s
`Strategy.GetMaxParallel`), not invented independently. Real GitHub
Actions' own default is effectively unbounded (limited only by
cloud-runner availability) — but mirror-gha's job containers all compete
for one local Docker daemon's real CPU/memory/network, the same resource
constraint act's own default was chosen to respect. `len(combinations)`
caps this further when smaller than 4 (or than an explicit
`max-parallel` value), so a 2-combination matrix never spins up more than
2 concurrent containers regardless of the configured limit.

**Two things intentionally NOT carried over from act, because they'd be
bugs, not fidelity:**
1. **act has a genuine unguarded data race** on matrix combinations of
   the same job — every combination shares one `*model.Job` pointer and
   concurrently mutates its `.Result`/`.Outputs` map with zero mutex
   (`pkg/runner/run_context.go`'s `result()`/`interpolateOutputs()`).
   mirror-gha's design instead has each combination's goroutine compute
   its own fully independent local result (steps, outputs, conclusion);
   these are merged into the job's shared `WorkflowResult` only after
   `sync.WaitGroup.Wait()` returns, under a mutex, by combination index —
   never a concurrent write to shared state during execution.
2. **`WorkflowResult`'s existing doc comment already documents a known
   GitHub Actions gotcha**: a matrixed job's `Outputs` reflect "the last
   combination to finish," reproduced deliberately as a fixed,
   deterministic choice (whichever combination is *last by index*) before
   this sub-project, since execution was strictly sequential. Under real
   concurrency, "last to finish" becomes a genuine wall-clock race between
   goroutines — mirror-gha switches to tracking this via whichever
   combination's completion the shared mutex-protected write actually
   lands last, which is *more* faithful to real GitHub Actions' own
   documented nondeterminism here than the previous deterministic
   approximation was, not a regression.

**Concurrency primitive: a semaphore channel + `sync.WaitGroup`, not a
new abstraction.** Every combination's goroutine launches immediately;
each acquires a `chan struct{}` semaphore sized to the effective
max-parallel before actually calling `RunJob`, and releases it on
completion — the standard Go bounded-worker-pool idiom, functionally
equivalent to act's own hand-rolled `NewParallelExecutor` (also
goroutines + channels, no external dependency) without introducing a
new named executor abstraction this project doesn't otherwise use.

**Fail-fast under concurrency: stops starting new combinations, doesn't
cancel in-flight ones — an extension of the existing pre-concurrency
semantic, not a new one.** A shared, mutex-guarded `stopStartingNew`
bool is checked by each goroutine immediately before it would start
running (after acquiring its semaphore slot); if already set, that
combination is recorded as `skipped` instead of run. Combinations already
past that check when a sibling combination's failure sets the flag run to
completion uninterrupted — matches mirror-gha's own existing (pre-this-
sub-project) fail-fast behavior of skipping not-yet-started work rather
than aborting running work, now extended from "the next step" to "the
next not-yet-started combination," not a new, more aggressive semantic
GitHub Actions' own active-cancellation behavior would imply. A 3-
combination matrix that previously always demonstrated "skip after
failure" under strictly sequential execution now only demonstrates it
when there are more combinations than the effective max-parallel (a
second wave to actually skip) — this is a real, deliberate behavior
change existing tests need to account for, not an oversight.

**`concurrency:` — a real gap in act (not implemented there at all,
confirmed via source and a repo-wide grep with zero hits), so this is
mirror-gha's own design, not ported from anywhere.** `Job.Concurrency()`
(dual-shape, matching every other `container:`/`environment:`-style
field: a bare group-name string, or `{group, cancel-in-progress}`) is
resolved **per matrix combination**, since the group string can reference
`${{ matrix.* }}` — evaluated via the same `SubstituteExpressions` every
`with:` value already uses, against that combination's own `Context`
(with `Matrix` set), before that combination's goroutine attempts to
acquire its group. Two combinations of the same job whose evaluated
group strings differ (the common case — e.g. `concurrency:
deploy-${{ matrix.env }}`) never contend with each other at all; only a
static group string (or two different matrix values that happen to
interpolate to the same string) creates real contention. Workflow-level
`concurrency:` is parsed (so it never errors) but is an explicit,
documented no-op: it exists in real GitHub Actions to serialize/cancel
*separate workflow runs* sharing a group, a concept with no meaning in a
tool that only ever executes one workflow run per invocation — this
matches this field's pre-existing "inherently satisfied, no-op" framing
from before this sub-project, still true for the workflow-level case even
though the job-level case now has real teeth.

**Scheduler mechanics — a small, self-contained type, not folded into
`workflow_run.go`'s own control flow.** `cancel-in-progress: false`
(the default) acquires a per-group `chan struct{}` sized 1, acting as a
mutex — a second combination in the same group blocks until the first
releases it (a real queue, not a skip). `cancel-in-progress: true`
instead cancels the group's currently-running combination's own
`context.Context` (layered on top of that combination's existing
per-job `TimeoutMinutes` context, unchanged) before starting the new one
— using a per-group monotonically increasing generation counter to avoid
a delayed release from an already-superseded combination incorrectly
clearing a newer one's state, a real correctness hazard a naive
"store one cancel func per group" implementation would hit under
genuine concurrency.

**Testing:** unit tests for `Job.Concurrency()`/`Workflow.Concurrency()`
parsing (both shapes), the scheduler's blocking-queue and
cancel-in-progress behavior in isolation (provable deterministically via
controlled goroutine ordering, not wall-clock timing), and a real
timing-based proof that concurrent matrix execution is genuinely faster
than sequential (N combinations of a fixed-duration fake step complete in
meaningfully less than N × that duration when `max-parallel` allows it) —
timing-based tests are inherently a little less precise than logic-based
ones, so this uses a generous margin rather than a tight bound. The
existing `TestRunWorkflow_MatrixFailFastStopsRemainingCombinations` test
needs its matrix strategy given an explicit `MaxParallel: 1` to keep
testing what it was actually written to test (sequential fail-fast skip
behavior) now that the *default* behavior for its 3-combination matrix
would otherwise run all three concurrently in one wave — a real behavior
change this sub-project causes, not a test bug being fixed incidentally.

## Triggers and Event Payloads

**Scope:** `github.event_name`/`github.event`/`GITHUB_EVENT_NAME`/
`GITHUB_EVENT_PATH` wired up for real, replacing the current hardcoded
`event_name: "workflow_dispatch"` stub — plus, by explicit request beyond
what act itself does, synthetic default event payloads for `push`,
`pull_request`, `workflow_dispatch`, `workflow_call`,
`repository_dispatch`, and `workflow_run` so common `github.event.*`
fields work out of the box without a hand-written payload file. Checked
against act's actual source (`nektos/act`, `pkg/runner/runner.go`'s event
loading, `pkg/model/github_context.go`, `cmd/root.go`'s event-name
resolution) per the standing direction — with one deliberate, explicit
divergence documented below.

**act's own mechanism, adopted directly for the real (non-synthetic)
path:** a `--event-path`/`-e` flag loads a real, user-supplied event JSON
file (`pkg/runner/runner.go`'s `configure()`); with no flag, act falls
back to a bare `{}` (or `{"inputs": {...}}` if `--input` flags were
passed) — **act never fabricates a schema-correct fake payload per event
type**, confirmed via source. `github.event` is a raw, untyped
`map[string]interface{}` populated by a direct `json.Unmarshal` of
whatever was loaded (`pkg/model/github_context.go`'s `GithubContext.Event
map[string]interface{}`) — no per-event-type schema or typing exists in
act, matching this project's own preference for a generic JSON-shaped
context value over hand-rolled per-event Go structs.
`GITHUB_EVENT_NAME`/`GITHUB_EVENT_PATH` are set as real step env vars
(`pkg/runner/run_context.go`'s `withGithubEnv`), with `GITHUB_EVENT_PATH`
pointing at a real file act writes into the job container
(`workflow/event.json` under its own `GetActPath()` convention) — not
just an in-memory expression-context value, since real actions
(`actions/github-script` and others) read the file directly.

**mirror-gha's `--event-path`/`--event-name` flags mirror this exactly.**
`--event-path <file>` loads real user-supplied JSON (default: none).
`--event-name <name>` selects `github.event_name`, resolved with the same
priority chain act uses at `cmd/root.go`'s CLI-arg resolution — explicit
flag wins; else, if the workflow's `on:` block names exactly one trigger,
use it; else, default to `"push"` (act's own final fallback) — requiring
a new `Workflow.OnEventNames() ([]string, error)` helper, since
`Workflow.On` is currently an unparsed raw `interface{}` with no existing
accessor for the three YAML shapes GitHub Actions allows (`on: push`,
`on: [push, pull_request]`, `on: {push: {...}, pull_request: {...}}`).

**The one deliberate divergence from act, at explicit request: synthetic
default payloads per trigger type when `--event-path` isn't given.**
There is no single "correct" fake payload — real GitHub webhook payloads
vary per repository, PR, and commit — so these are plausible, structurally
real shapes (matching GitHub's own public webhook-payload documentation,
not act-specific, since act has no reference implementation for this at
all) populated with the same placeholder conventions this project already
uses elsewhere (`sha: "000...0"`, `repository: "local/mirror-gha"`,
`ref: "refs/heads/main"`, `actor: "local"`). Covers exactly the six
trigger types named in scope; any other trigger name (`release`,
`issues`, `schedule`, etc.) falls back to `{}`, matching act's own
default for the untyped case rather than attempting exhaustive coverage
of GitHub's several dozen trigger types. `workflow_dispatch`/
`workflow_call`'s declared `inputs:` are represented structurally (an
empty `inputs: {}` object) but **not populated with real user-supplied
values** — actually wiring a `--input NAME=VALUE`-style flag into
`github.event.inputs.*` is a distinct, separately-scoped feature (it
would need its own new CLI mechanism, not just a payload shape), not
attempted in this sub-project.

**`github.event.*` needs a genuinely new expression-resolution path —
the existing `github` context case only ever handles flat two-segment
lookups (`github.workspace`, `github.sha`), but `github.event.*` needs
arbitrary-depth traversal into whatever JSON structure was loaded** (e.g.
`github.event.pull_request.head.ref`, four segments deep). A new
`resolveNestedPath(val interface{}, remaining []string) (interface{},
error)` helper recurses through `map[string]interface{}` (case-insensitive
key match, matching every other context lookup in this codebase) and
`[]interface{}` (numeric index) — returning `nil, nil` for a path that
doesn't resolve (a missing property, an out-of-range index), matching
this codebase's existing leniency for `steps.<id>.outputs.<name>`/
`needs.<job>.outputs.<name>` rather than erroring on every reference to
an absent field, since real payloads (and this project's own synthetic
ones) don't include every field every workflow might reference.

**Wiring reuses the existing `/mirror-*` `CopyToContainer` convention
established for the macOS host backend, rather than inventing a new
per-job file-staging mechanism.** `GITHUB_EVENT_PATH` must point at a
real file inside whatever environment a step actually runs in (a Docker
container, or the real host for macOS jobs) — the same "engine bakes a
synthetic absolute path, each backend makes it real" pattern already
built for `/mirror-node`/`/mirror-actions/<id>`. `RunJob` copies a host
directory containing exactly one file (`event.json`) to `/mirror-event`
via `Job.CopyToContainer` (already implemented generically by every
backend), then sets `actx.GitHub["event_path"] = "/mirror-event/event.json"`
— exported as `GITHUB_EVENT_PATH` automatically by the existing
`exportGitHubContextEnv` generic mechanism, no new env-export code
needed. `actx.GitHub["event_name"]` replaces the hardcoded stub the same
way. `actx.GitHub["event"]` holds the parsed `map[string]interface{}`
directly — `exportGitHubContextEnv`'s existing `string`-only type check
already skips it safely (there is no real `GITHUB_EVENT` env var in
GitHub Actions either, only `GITHUB_EVENT_PATH`/`GITHUB_EVENT_NAME`).

**Explicitly out of scope, matching act's own choice (confirmed via
source: the pattern-matching code for this exists in act but is
completely unwired, dead code) rather than an oversight:** `on: push:
branches/tags/paths` and `on: pull_request: types/branches` sub-filters.
These exist in real GitHub Actions to decide whether the platform's
server-side trigger fires a workflow run *at all* — a concept with no
meaning once a user has already explicitly invoked `mirror run
<file>.yml` locally; the job's own `if:` conditions (checking
`github.event_name`, `github.event.pull_request.base.ref`, etc.,
already fully supported) are the mechanism that still matters locally.

**Testing:** unit tests for `Workflow.OnEventNames()` (all three YAML
shapes), the event-name resolution priority chain, `resolveNestedPath`
(map traversal, array indexing, missing-path leniency), and each of the
six synthetic default payload builders. Real end-to-end verification: a
workflow with no `--event-path` given, checking several `github.event.*`
fields for each of the six covered trigger types via `if:` conditions and
`run:` step output, plus one run with a real user-supplied
`--event-path` file confirming it overrides the synthetic default.

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
shim.** Re-verified directly against act's real source for this
sub-project (`pkg/artifacts/server.go`, `pkg/artifactcache/handler.go`),
not just inferred: `actions/upload-artifact` and `actions/cache` don't
read/write files directly — they call GitHub's real Artifact/Cache REST
API, at a URL the runner injects via `ACTIONS_CACHE_URL`/
`ACTIONS_RUNTIME_URL` (plus a runtime token) as job-scoped environment
variables. A plain "watch the filesystem" shim would never be
intercepted by those actions at all, since they never touch the
filesystem directly for this. Two corrections to the original note,
found only by reading act's actual source rather than trusting the
general-knowledge version of this claim: **artifacts need both the old
REST-ish v3 API and the newer twirp-shaped v4 API** (act implements
both on the same router — real-world workflows are split across
`actions/upload-artifact@v3` and `@v4+`, so v4-only would silently break
a large fraction of them), and **act's cache-key restore-keys matching
is a real reimplementation of GitHub's own semantics** (exact match per
key, then anchored-regex prefix match, most-recent-created wins) — not
a stub, and not something mirror-gha can simplify away without breaking
real workflows' actual cache-hit behavior. See the "Cache Runtime"
section below for the first sub-project built on this finding;
artifacts (v3+v4) are a separate, still-unscheduled follow-up.

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
