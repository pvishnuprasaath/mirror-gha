# mirror-gha

[![CI](https://github.com/pvishnuprasaath/mirror-gha/actions/workflows/ci.yml/badge.svg)](https://github.com/pvishnuprasaath/mirror-gha/actions/workflows/ci.yml)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.27%2B-00ADD8.svg)](go.mod)

**Test your GitHub Actions workflows locally — for real.** No more push-and-pray.

`mirror` runs your `.github/workflows/*.yml` files the way GitHub Actions
actually does: same expression syntax, same contexts, same step-by-step
semantics — executed in a real Docker container on your machine, in
seconds, before you ever open a PR.

```
$ mirror run .github/workflows/ci.yml
[build] checkout: success
[build] install dependencies: success
[build] run tests: success
```

## Why this exists

Today, the only way to know if a GitHub Actions change actually works is
to push a commit, open a PR, and wait for CI. Existing local runners (like
`nektos/act`) get you partway there, but fidelity breaks down fast: Docker
containers aren't real GitHub-hosted VMs, and matrix builds, artifacts,
and caching are only partially emulated.

`mirror` is being built from scratch as an exact local replica of the
GitHub Actions runtime — not a wrapper around an existing tool — so that
"it passed locally" actually means something. See
[the design spec](docs/design/specs/2026-09-14-mirror-gha-design.md) for
the full reasoning and roadmap.

## Install

Requires [Go](https://go.dev/) 1.27+ and [Docker](https://www.docker.com/).
No published binary release yet (pre-alpha) — build from source:

```bash
git clone git@github.com:pvishnuprasaath/mirror-gha.git
cd mirror-gha
make build          # produces ./bin/mirror
```

## Quickstart

```bash
./bin/mirror run examples/workflows/basic-run.yml
```

Then point it at a real workflow:

```bash
./bin/mirror run .github/workflows/ci.yml
```

Check what a workflow would do before actually running it:

```bash
./bin/mirror run --list examples/workflows/matrix-build.yml   # jobs + matrix combinations
./bin/mirror run --graph examples/workflows/job-dependencies.yml  # dependency order
./bin/mirror run --dryrun .github/workflows/ci.yml             # full needs/matrix/if logic, no Docker
```

## Usage

Full usage reference, what's supported today, what isn't yet, and best
practices while the project is early: **[docs/usage.md](docs/usage.md)**.

Runnable, documented sample workflows covering each supported feature
(conditionals, `continue-on-error`, output passing between steps):
**[examples/](examples/)**.

## Free and Pro

The CLI — everything in this repo — is free and stays free, Apache 2.0
licensed, no feature gate. A paid **Pro** tier is planned on top of it
(environments/approval-gate simulation, an OIDC test harness, a secrets
vault UI, a watch-mode daemon, and an advanced dashboard), not instead of
the free CLI. If you're only trying to test workflows locally, the free
CLI is the whole answer — Pro is for teams that want the ops/collaboration
layer once it ships. See the [design spec's monetization section](docs/design/specs/2026-09-14-mirror-gha-design.md#monetization-open-core)
for the exact split.

## Status

Early, pre-alpha — see [CHANGELOG.md](CHANGELOG.md) for what's shipped
and [the phased feature-parity matrix](docs/design/specs/2026-09-14-mirror-gha-design.md#phased-feature-parity-matrix)
for what's next.

## Contributing

Contributions are welcome — see **[CONTRIBUTING.md](CONTRIBUTING.md)** for
the development workflow, and the [Code of Conduct](CODE_OF_CONDUCT.md)
before participating.

## Security

Found a security issue? Please don't open a public issue — see
[SECURITY.md](SECURITY.md) for how to report it privately.

## License

[Apache 2.0](LICENSE).
