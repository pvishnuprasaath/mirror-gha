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
runner-OS mirror for Linux, Windows, and macOS — built from scratch, not
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
   (Marketplace, local `./path`, `docker://`). Executes JS actions (bundles
   the Node version per the action's `runs.using`), Docker actions
   (builds/pulls the action's own image), composite actions (recursively
   expands into the step graph). All three supported from day one.

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
   full-mirror and scriptable, independent of whether the dashboard is open.

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

Artifacts/cache are real local filesystem stores keyed the same way
`actions/upload-artifact` and `actions/cache` key theirs (name + path +
hash), so those actions work unmodified against the shim.

## Phased feature-mirror matrix

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
