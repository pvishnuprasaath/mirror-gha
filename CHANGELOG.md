# Changelog

All notable changes to this project are documented here. Format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project
doesn't have tagged releases yet (pre-alpha) — entries land under
Unreleased until the first `v0.1.0`.

## [Unreleased]

### Added

- Workflow YAML parsing (`internal/engine`) — jobs, steps, `env`, `if`,
  `continue-on-error`, `working-directory`.
- Full GitHub Actions expression evaluator — `${{ }}`, `env`/`github`/
  `runner`/`steps` contexts, `==`/`!=`/`&&`/`||`/`!`, status functions
  (`success()`, `failure()`, `always()`, `cancelled()`).
- The real workflow-command file protocol: `GITHUB_ENV`, `GITHUB_PATH`,
  `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY`.
- Linux Docker execution backend — `run:` steps execute in a real
  `ubuntu:22.04` container.
- `mirror run <workflow.yml>` CLI command, wired end-to-end.
- Runnable examples under `examples/workflows/`.

### Known limitations

See [`docs/usage.md`](docs/usage.md#whats-not-supported-yet) — multi-job
`needs:`, matrix builds, `uses:` actions, artifacts/caching, and
Windows/macOS runners are not implemented yet, by design and in that
order (see the [design spec](docs/design/specs/2026-09-14-mirror-gha-design.md)).
