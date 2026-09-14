# Changelog

All notable changes to this project are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project
doesn't have tagged releases yet (pre-alpha) — entries land under
Unreleased until the first `v0.1.0`.

## [Unreleased]

### Added

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

### Known limitations

See [`docs/usage.md`](docs/usage.md#whats-not-supported-yet) — `uses:`
actions, artifacts/caching, `matrix.include`/`exclude`,
`services:`/`container:` job fields, and Windows/macOS runners are not
implemented yet, by design and in that order (see the
[design spec](docs/design/specs/2026-09-14-mirror-gha-design.md)).
