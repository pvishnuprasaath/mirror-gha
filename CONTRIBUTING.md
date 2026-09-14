# Contributing to mirror-gha

Thanks for taking a look. This project is early — the core engine is a
thin vertical slice, not a finished product — so the most useful
contributions right now are bug reports against real-world workflow
files and small, focused PRs.

## Development setup

Requires Go 1.27+ and Docker.

```bash
git clone git@github.com:pvishnuprasaath/mirror-gha.git
cd mirror-gha
go test ./...
go build -o bin/mirror ./cmd/mirror
```

## Workflow

1. Fork and branch off `main`.
2. Write a failing test before implementing (TDD — see existing `*_test.go`
   files for the pattern used throughout this codebase).
3. Keep commits small and focused; one logical change per commit.
4. Run `go test ./...` and confirm everything passes before opening a PR.
5. Open a PR against `main` describing what changed and why.

## Design docs and plans

Substantial changes should be preceded by a short design note under
`docs/superpowers/specs/`, following the existing files there as a
template. This keeps the reasoning behind architectural decisions
(especially around runner fidelity and what's explicitly out of scope)
discoverable later, not just in PR descriptions.

## Code style

- Run `gofmt -l .` before committing; there should be no output.
- Prefer small, single-responsibility files — this codebase deliberately
  keeps `internal/engine`, `internal/runner`, and `internal/commands`
  as separate packages with narrow interfaces between them.
- No placeholder implementations — a partial feature should fail loudly
  (a typed error, a documented `FidelityWarning`) rather than silently
  behave differently from real GitHub Actions.

## Reporting issues

Include the workflow YAML (or a minimal reproduction) and the actual vs.
expected behavior. If it's a fidelity gap versus real GitHub Actions,
say what GitHub Actions actually does — that's the bar this project is
held to.
