# mirror-gha

**Test your GitHub Actions workflows locally — for real.** No more push-and-pray.

`mirror` runs your `.github/workflows/*.yml` files exactly as GitHub Actions
would: same expression syntax, same contexts, same step-by-step semantics —
executed in a real Docker container on your machine, in seconds, before you
ever open a PR.

```
$ mirror run .github/workflows/ci.yml
[build] checkout: success
[build] install dependencies: success
[build] run tests: success
```

## Why

Today, the only way to know if a GitHub Actions change actually works is to
push a commit, open a PR, and wait for CI. Existing local runners (like
`nektos/act`) get you partway there, but fidelity breaks down fast: Docker
containers aren't real GitHub-hosted VMs, and matrix builds, artifacts, and
caching are only partially emulated.

`mirror` is being built from scratch as an exact local replica of the
GitHub Actions runtime — not a wrapper around an existing tool — so that
"it passed locally" actually means something.

## Status

Early, pre-alpha. The current vertical slice supports:

- Full GitHub Actions expression syntax (`${{ }}`, contexts, operators, status functions)
- `run:` steps executed in a real Ubuntu Docker container
- `if:` conditions, `continue-on-error`, step-to-step output passing
- The real workflow-command file protocol (`GITHUB_ENV`, `GITHUB_PATH`, `GITHUB_OUTPUT`, `GITHUB_STEP_SUMMARY`)

Not yet implemented: multi-job dependency graphs, matrix builds, `uses:`
actions (JS/Docker/composite), artifacts, caching, and the dashboard UI. See
[the design spec](docs/superpowers/specs/2026-09-14-mirror-gha-design.md)
for the full roadmap and [the plan](docs/superpowers/plans/2026-09-14-mirror-gha-phase1-vertical-slice.md)
for what's shipped so far.

## Install

Requires [Go](https://go.dev/) 1.27+ and [Docker](https://www.docker.com/)
(Linux-runner jobs execute inside a container).

```bash
git clone git@github.com:pvishnuprasaath/mirror-gha.git
cd mirror-gha
go build -o bin/mirror ./cmd/mirror
```

## Usage

```bash
./bin/mirror version
./bin/mirror run path/to/workflow.yml
```

## Development

```bash
go test ./...              # run the full test suite
go build -o bin/mirror ./cmd/mirror
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow.

## License

[Apache 2.0](LICENSE) — the core CLI is, and will stay, free and open
source. A paid Pro tier (environments/approvals, OIDC test harness, secrets
vault, watch-mode daemon, advanced dashboard) is planned on top, not instead.
