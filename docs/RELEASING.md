# Releasing

This project uses [GoReleaser](https://goreleaser.com/) driven by
[`.goreleaser.yaml`](../.goreleaser.yaml) and
[`.github/workflows/release.yml`](../.github/workflows/release.yml).
Pushing a `v*` tag is the only manual step — everything else (building
binaries for linux/darwin × amd64/arm64, checksums, changelog, GitHub
Release) happens in CI.

## Cutting a release

```bash
git tag v0.1.0
git push origin v0.1.0
```

That's it. The `release` workflow picks it up, runs
`goreleaser release --clean`, and publishes a GitHub Release with:

- `mirror-gha_<version>_<os>_<arch>.tar.gz` for each of linux/darwin ×
  amd64/arm64, each bundling the `mirror` binary + `LICENSE` + `README.md`
- `checksums.txt` (sha256, `install.sh` verifies against this)
- A changelog grouped into Fixed/Added/Other, generated from commit
  message prefixes (`fix:`, `feat:`) since the previous tag

Version, commit, and build date are baked into the binary itself
(`mirror version` prints them) via GoReleaser's default `ldflags` — no
custom flag configuration needed, the variable names in
`cmd/mirror/main.go` (`version`, `commit`, `date`, `builtBy`) already
match what GoReleaser injects automatically.

## Before the first real release

- [ ] Verify locally first: `goreleaser release --snapshot --clean`
      builds everything without needing a tag or publishing anything.
      Extract one of the `dist/*.tar.gz` archives and run the binary to
      confirm it actually works before trusting a real tag push.
- [ ] `install.sh` fetches from `https://api.github.com/repos/<repo>/releases/latest`
      and `.../releases/download/<tag>/...` — these only resolve once a
      real (non-prerelease) GitHub Release exists. Smoke-test it against
      the first real release: `curl -fsSL .../install.sh | sh`.

## Homebrew tap (currently disabled)

`.goreleaser.yaml`'s `homebrew_casks` section is fully configured but
`skip_upload: true` — GoReleaser would otherwise try to push a cask to
`pvishnuprasaath/homebrew-tap`, which doesn't exist yet. To turn it on:

1. Create the `pvishnuprasaath/homebrew-tap` repository (empty is fine —
   GoReleaser creates the `Casks/` directory and commits the formula
   file itself).
2. Create a GitHub personal access token with write access to that repo,
   add it as a repository secret named `TAP_GITHUB_TOKEN` on
   `mirror-gha` (Settings → Secrets and variables → Actions).
3. Set `skip_upload: false` in `.goreleaser.yaml`.
4. Cut the next release as normal. Users can then run
   `brew install --cask pvishnuprasaath/tap/mirror-gha`.

Note: this project deliberately uses `homebrew_casks`, not the legacy
`brews` (Formula) section — `brews` was fully deprecated in GoReleaser
v2.16 in favor of casks for distributing precompiled binaries.

## Not set up yet (future work, not blocking)

- Linux package formats (`.deb`/`.rpm` via `nfpm`) — no demand signal
  yet; add if/when users ask.
- Windows builds — the project doesn't support `windows-latest` as a
  runner yet either (see the design spec's Phase 2), so a Windows CLI
  binary would be premature.
- Code signing / notarization for macOS — worth doing before wide
  distribution, since an unsigned binary triggers Gatekeeper warnings;
  not done yet.
