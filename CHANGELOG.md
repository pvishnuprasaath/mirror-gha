# Changelog

All notable changes to this project are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project
doesn't have tagged releases yet (pre-alpha) — entries land under
Unreleased until the first `v0.1.0`.

## [Unreleased]

### Added

- **`matrix.include`/`matrix.exclude`.** Real GitHub Actions merge
  semantics, checked against act's own implementation
  (`pkg/model/workflow.go`'s `GetMatrixes`) rather than guessed: exclude
  drops any combination matching all of an exclude entry's key/value
  pairs (every exclude key must be a real matrix axis, or this is an
  error), applied before include; include merges its extra keys into
  every combination whose axis-key subset already matches (one include
  entry can merge into more than one combination), or — if it matches
  nothing — becomes its own standalone combination. A matrix with only
  `include` and no axes treats each include entry as one full
  combination directly. Previously these were parsed and explicitly
  rejected with a "not supported yet" error. Verified for real: a
  workflow combining a 2x2 axis matrix with one exclude and two include
  entries (one merging, one standalone) produced exactly the 4 expected
  job combinations.
- **Job workspace.** Every job bind-mounts a real directory (default:
  wherever `mirror run` was invoked from, overridable via `--workdir`)
  at `/github/workspace` — a genuine bind mount, not a copy, so writes
  land back on the real host filesystem and existing project files are
  visible from the first step. Exposed via `github.workspace`,
  `$GITHUB_WORKSPACE`, and as the default step working directory. This
  closes a prerequisite gap the original architecture missed
  entirely — jobs previously had no repo mounted at all, and it's the
  foundation the upcoming Actions Runtime needs (`actions/checkout`,
  `hashFiles()`). Verified for real: files written inside the container
  confirmed present on the host after the job exits, and vice versa.
- **Release pipeline.** GoReleaser (`.goreleaser.yaml`) + a GitHub
  Actions workflow (`.github/workflows/release.yml`) triggered on `v*`
  tags — builds linux/darwin × amd64/arm64 binaries, checksums, a
  grouped changelog, and a GitHub Release; a Homebrew cask is fully
  configured but disabled (`skip_upload: true`) until the
  `homebrew-tap` repo exists, see `docs/RELEASING.md`. `install.sh`
  gives a `curl | sh` install path that verifies the download's
  checksum. Verified end-to-end with a local `goreleaser --snapshot`
  build (real binary extracted and run) before committing.
- `mirror run --list` — every job (and matrix combination), its
  `runs-on` and `needs:`, without running anything.
- `mirror run --graph` — jobs in dependency order, `needs:` indented
  underneath.
- `mirror run --dryrun` — runs the real needs/matrix/if orchestration
  end to end, but every step reports success without executing. Still
  validates `runs-on` support, so an unsupported runner errors even in
  dry-run mode.
- Workflow YAML parsing (`internal/engine`) — jobs, steps, `env`, `if`,
  `continue-on-error`, `working-directory`, `timeout-minutes`,
  `defaults`, `strategy`, `outputs`, `needs` (scalar or list form).
- GitHub Actions expression evaluation over a real parsed AST (vendored
  from `rhysd/actionlint`, see `third_party/ghaexpr/NOTICE.md`) rather
  than a hand-rolled subset: `${{ }}`, `env`/`github`/`runner`/`steps`/
  `needs`/`matrix`/`vars` contexts (case-insensitive property access),
  parenthesized grouping, `==`/`!=`/`<`/`<=`/`>`/`>=`/`&&`/`||`/`!` with
  real short-circuit semantics (`&&`/`||` return the operand, not a
  coerced bool), and built-in functions `success()`, `failure()`,
  `always()`, `cancelled()`, `contains()`, `startsWith()`, `endsWith()`,
  `format()`, `join()`, `toJSON()`, `fromJSON()`.
- The real workflow-command file protocol: `GITHUB_ENV`, `GITHUB_PATH`,
  `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY`.
- Linux Docker execution backend — steps execute in a real `ubuntu:22.04`
  container, one container per job.
- Multi-job workflows: `needs:` dependency ordering, job `outputs:`
  propagated to dependents via `needs.<job>.outputs.<name>` and
  `needs.<job>.result`, a job skipped (not run) when its `needs:` didn't
  all succeed.
- `strategy.matrix` — cartesian product of matrix axes, `fail-fast`
  (defaults to `true`), each combination gets its own `matrix.<key>`
  context. `matrix.include`/`exclude` are parsed but rejected with a
  clear error rather than approximated.
- `timeout-minutes` at job and step level.
- `defaults.run.shell` / `defaults.run.working-directory` at workflow
  and job level, with GitHub Actions' real precedence (step > job >
  workflow).
- `mirror run <workflow.yml>` CLI command, running the full job DAG
  end-to-end.
- Runnable examples under `examples/workflows/` for every feature above.
- **`uses:` JS actions.** Marketplace (`owner/repo[/subpath]@ref`) and
  local (`./path`) actions execute for real: source fetched via
  `curl`/`tar` (not Go's `net/http` — see design spec) and cached at
  `~/.cache/mirror-gha/actions/`, run against one pinned Node build
  (`~/.cache/mirror-gha/node/`) copied into the job container via
  `docker cp` (`runner.Job.CopyToContainer`), `with:` inputs mapped to
  `INPUT_*` env vars exactly as GitHub Actions does. Docker and
  composite actions are rejected with a clear error. Verified for real
  against both a local fixture action and
  `actions/hello-world-javascript-action` (GitHub's own official demo
  action).
- **`uses:` Docker actions.** A raw `docker://image:tag` reference, or a
  Marketplace/local action whose `runs.image` names a Dockerfile, runs as
  its own sibling container (`docker run --rm`, not `docker exec` into the
  job's own container — matches act's real model, checked against its
  source). Dockerfile-based action images are built once and cached by
  tag (`mirror-gha-<sanitized-action-ref>:latest`). The container joins
  the job container's network namespace (`--network container:<id>`) so
  `localhost` service-container access keeps working, bind-mounts the job
  workspace and the action's own source (repo-based actions), and mounts
  `/var/run/docker.sock` unconditionally, matching real GitHub-hosted
  runners — a documented trust tradeoff, not an oversight. `with:` inputs
  map to `INPUT_*` env vars via the same transform JS actions already use.
  Composite actions are still rejected with a clear error. Verified for
  real against a raw `docker://alpine` step and a hand-written
  Dockerfile-based local action fixture, including confirming the image
  build is genuinely cached (not rebuilt) on a second run.
- **`uses:` composite actions.** A composite action's own `runs.steps:`
  execute through the same `runStep` logic every other step uses —
  recursively, so a composite can nest another composite (capped at 10
  levels deep, a mirror-gha-specific safety net act itself doesn't need,
  since its network-fetch model makes a self-referencing action rare;
  mirror-gha's local-path resolution makes it a real, easy-to-hit crash
  risk instead). Its own inputs are available to its nested steps via a
  new `inputs.*` expression context — derived from `INPUT_*` env vars
  generically, not new storage, matching act's own model. Its own
  `outputs:` (`value: ${{ steps.x.outputs.y }}` expressions) evaluate
  against the composite's own nested step scope and surface as the
  calling `uses:` step's output. Verified for real against a fixture
  nesting a `run:` step and a `uses:` step calling the existing local JS
  action fixture, confirming the full input → nested-step → output chain.
- **`--local-repository owner/repo[@ref]=local/path`** (repeatable),
  matching act's own flag — overrides a Marketplace-style action
  reference to resolve from a local directory, checked before the
  network fetch. Verified for real: a reference to a repo that cannot
  exist on GitHub resolves correctly with the flag and fails without it.
- **Real `vars.*` context source.** `--var NAME=VALUE` (repeatable, bare
  `--var NAME` for an empty value) and `--var-file <path>` (default
  `.vars`, one entry per line, missing file not an error) — matching
  act's own `--var`/`--var-file` flags exactly. `--var` overrides
  `--var-file` on conflict. Verified for real: an `if:` gated on
  `vars.ENVIRONMENT == 'staging'` is skipped with no flags, runs with
  `--var ENVIRONMENT=staging`, and runs identically via `--var-file`.
- **Real `actions/cache` support.** A new local HTTP server
  (`internal/cacheserver`) implements GitHub's actual cache API —
  `GET/POST/PATCH /_apis/artifactcache/...` — matching act's own
  `pkg/artifactcache` route surface and restore-keys matching
  (exact match, then anchored-prefix match, most-recently-created
  wins). Started once per `mirror run` invocation (not per-job,
  matching act's lifecycle), reachable from job containers via
  `host.docker.internal` (this project's actual dev/test environment
  is macOS Docker Desktop; plain Linux `dockerd` needs extra
  configuration not yet wired up). The cache store persists across
  separate `mirror run` invocations at `~/.cache/mirror-gha/action-cache/`
  (`~/Library/Caches/mirror-gha/action-cache/` on macOS) — ephemeral-
  per-run would defeat the entire point of caching. No new action-type
  dispatch was needed: `actions/cache` is itself a bundled JS action,
  so it runs through the existing JS-actions machinery unmodified once
  the right env vars (`ACTIONS_CACHE_URL`, `ACTIONS_RUNTIME_TOKEN`) are
  present. Verified for real against the actual, unmodified
  `actions/cache@v4` action: a save on one `mirror run` invocation is
  genuinely restored (confirmed `cache-hit: true`, real tar extraction,
  the populate-on-miss step correctly skipped, post correctly declining
  to re-save on a hit) on a second, separate invocation. Artifacts
  (`upload-artifact`/`download-artifact`, v3+v4) remain a separate,
  unscheduled follow-up.
- **JS action `post` entry points.** `runs.post` (and `runs.post-if`)
  from `action.yml` now execute — once per job, after all of that job's
  own top-level steps finish, in reverse step order — with state passed
  from the main run via a new `$GITHUB_STATE` file and `STATE_*` env
  vars on the post invocation, matching real GitHub Actions' main/post
  contract exactly. Found necessary via real end-to-end testing of
  `actions/cache@v4`: its `action.yml` declares `main:
  dist/restore/index.js` and `post: dist/save/index.js` — the actual
  save only ever happens in `post`, which mirror-gha previously never
  executed at all, so caching silently only ever restored and never
  saved. Composite-nested `uses:` steps' own post actions are a
  documented, accepted scope limit for now.
- **Real `actions/upload-artifact`/`download-artifact` support.** A new
  local HTTP server (`internal/artifactserver`) implements both act's
  real legacy v3 REST routes and its v4 routes — hand-written plain JSON
  (no `google.golang.org/protobuf` dependency) matching the real wire
  format confirmed against genuine client traffic this session. Shares
  one server with a fresh, non-persisted store per `mirror run`
  invocation (the opposite of the cache store, which deliberately does
  persist — artifacts belong to one run, not restored across later
  ones). Always-on alongside the cache server, no new flag — a
  deliberate divergence from act's own opt-in `--artifact-server-path`.
  Also adds `GITHUB_RUN_ID`/`GITHUB_RUN_NUMBER`/`GITHUB_RUN_ATTEMPT` as
  real env vars (fixed placeholder values, proactively closing the same
  bug class found with `GITHUB_REF` during Cache Runtime). Verified for
  real against both a current (`@v4`) and legacy pinned (`@v3`) version
  of the real, unmodified actions in a two-job upload-then-download
  workflow, confirming genuine content round-tripping through both
  protocols.

### Fixed

- **Homebrew cask silently killed on launch.** The `mirror-gha` cask
  installed fine but macOS Gatekeeper quarantined the unsigned binary,
  so every invocation died silently (exit 137, no error text) —
  confirmed for real on `v0.1.2`. Added a cask post-install hook that
  strips the quarantine attribute (`xattr -dr com.apple.quarantine`),
  GoReleaser's documented workaround for unsigned binaries — **confirmed
  fixed for real on `v0.1.3`** (fresh reinstall, no quarantine attribute,
  binary runs). Proper code signing/notarization is not set up yet
  (needs a paid Apple Developer account) — this is a stopgap, not a
  substitute for it.
- **Job container lifecycle.** Comparing against `nektos/act`'s source
  found a real fidelity bug: every step of a job now execs into one
  long-lived container for that job (matching how act and real GitHub
  Actions both work), instead of a fresh `docker run --rm` per step.
  Filesystem state — checked-out files, installed packages, `PATH`
  changes — now persists step to step within a job.
- **Temp directory leak.** Every job run left its host-side workflow-command
  files directory behind in the OS temp dir forever, since `Stop()` only
  removed the container. Now cleaned up alongside the container.
- **Composite action nested step output silently swallowed.** Found via
  real end-to-end testing of the first composite action example: a
  composite step's own `StepReport` carried no stdout/stderr at all, so
  `mirror run` printed nothing for any nested `run:`/`uses:` step inside
  it — the composite's own bridged `outputs:` still computed correctly,
  but its nested steps' console output vanished. Fixed by concatenating
  every nested step's stdout/stderr into the composite's own outer
  `StepReport`.
- **`github.*` context values never reached real steps as env vars.**
  Found via real end-to-end testing of `actions/cache@v4`: its save
  logic gates on `process.env.GITHUB_REF` existing at all, and
  mirror-gha only ever populated `github.*` values inside `${{ }}`
  expressions, never as actual `GITHUB_*` environment variables — a
  general correctness gap (any action reading `process.env.GITHUB_REF`
  directly hit this), not specific to cache. Every string-valued
  `github.*` context entry is now exported as its real `GITHUB_*` env
  var for every step, matching real GitHub Actions.
- **`GITHUB_STATE` pointed at a host path from inside the container.**
  Introduced alongside JS action post-entry-point support and caught
  before merging: unlike `GITHUB_ENV`/`OUTPUT`/`PATH`/`STEP_SUMMARY`,
  which the Docker backend already translates to the container's own
  bind-mounted path, `GITHUB_STATE`'s value was briefly set to the
  host filesystem path directly. Fixed by computing it the same way as
  the other three, at the same layer.
- **`ACTIONS_RUNTIME_TOKEN` crashed the real v4 artifact client before
  any request was made.** Found via real end-to-end testing:
  `actions/upload-artifact@v4`'s bundled client runs the token through a
  `jwt-decode` library first; mirror-gha's plain placeholder string (no
  `.` separators) made `token.split(".")[1]` return `undefined`,
  crashing with "Invalid token specified: Cannot read properties of
  undefined (reading 'replace')" — before the artifact server ever saw a
  single request. Neither server validates this token, so any
  JWT-*shaped* value works; fixed by generating one.
- **v4 artifact `size`/`artifactId` fields rejected as invalid JSON.**
  protojson encodes 64-bit integer fields as JSON strings, not numbers,
  to avoid precision loss in JS — confirmed from the real client's
  actual request body, not just inferred from act's server-side struct
  tags. Fixed via Go's built-in `json:",string"` tag option.
- **v4 artifact upload silently corrupted by its own finalize request.**
  The real v4 client speaks Azure Blob Storage's block-upload protocol
  against `UploadArtifact`: a `comp=block` request carries real content
  bytes, but a final `comp=blocklist` request carries an XML manifest,
  not content. mirror-gha wrote every `PUT` unconditionally, so that
  manifest silently overwrote the real uploaded zip with garbage —
  surfacing only as "Not a valid zip file" on download, several steps
  removed from the actual cause. Fixed by no-oping `comp=blocklist`.
- **v3 artifact download listed the wrong path, so downloads silently
  found nothing.** The container-item listing's `path` field used the
  run id (e.g. `"1"`) as its leading path component instead of the
  artifact name — the real `actions/download-artifact@v3` client didn't
  error on this, it just silently reported "No downloadable files were
  found for the artifact," which would have been easy to miss without
  running the real, unmodified action.

### Known limitations

See [`docs/usage.md`](docs/usage.md#whats-not-supported-yet) —
`matrix.include`/`exclude`, `services:`/`container:` job
fields, and Windows/macOS runners are not implemented yet, by design
and in that order (see the
[design spec](docs/design/specs/2026-09-14-mirror-gha-design.md)).
