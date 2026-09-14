# JS Actions Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `uses:` steps resolve a JS action (Marketplace `owner/repo[/subpath]@ref` or local `./path`), fetch/cache its source and a pinned Node runtime, and execute it for real inside the job container — with `with:` inputs and `$GITHUB_OUTPUT` outputs working exactly like `run:` steps already do.

**Architecture:** A new `internal/actions` package owns everything about resolving/fetching/caching actions and the Node runtime (all via `curl`/`tar` shell-outs, not Go's `net/http`, per the spec's TLS-trust reasoning). `runner.Job` gains one new method, `CopyToContainer`, matching act's own on-demand `docker cp`-style injection rather than a pre-declared mount. `internal/engine`'s job executor gains a `uses:` branch that shares the exact same env-merging, workflow-command-file, and output-parsing code `run:` steps already use — no new plumbing needed there.

**Tech Stack:** Go 1.27 stdlib only in `internal/actions` (no new dependencies) + shelling out to `curl`/`tar`/`docker`, matching this repo's existing pattern (`install.sh`, `docker_backend.go`).

**Spec:** `docs/design/specs/2026-09-14-mirror-gha-design.md`, "JS Actions Runtime" section.

## Global Constraints

- Docker and composite actions (`runs.using: docker` / `composite`) are out of scope — reject with a clear error, never approximate.
- Action/Node fetches shell out to `curl` and `tar` — never Go's `net/http` (this machine's network breaks Go's own TLS trust; `curl` doesn't).
- Cache root: `os.UserCacheDir()/mirror-gha` (e.g. `~/.cache/mirror-gha` on Linux/macOS via `XDG_CACHE_HOME`/`~/Library/Caches`).
- Container paths: Node runtime always at `/mirror-node`; each step's action source at `/mirror-actions/<step-id>`.
- `INPUT_` env var transform: `"INPUT_" + regexp("[^A-Z0-9-]").ReplaceAllString(strings.ToUpper(name), "_")` — uppercase, dashes preserved, everything else non-alphanumeric becomes `_`.
- Pinned Node version: `20.11.1`.
- A step with both `run:` and `uses:` set is a hard error.
- `uses:` steps always execute in the job workspace — no `working-directory:` override.
- TDD throughout: every task writes the failing test before the implementation.
- Network-dependent tests skip gracefully when there's no connectivity, matching the existing `requireDocker(t)` pattern in `internal/runner`.

---

### Task 1: Step model — `uses:`/`with:` fields + mutual-exclusion check

**Files:**
- Modify: `internal/engine/workflow.go`
- Modify: `internal/engine/executor.go`
- Test: `internal/engine/workflow_test.go`, `internal/engine/executor_test.go`

**Interfaces:**
- Produces: `Step.Uses string`, `Step.With map[string]string`

- [ ] **Step 1: Write the failing tests**

```go
// internal/engine/workflow_test.go — add this test
func TestParse_UsesAndWith(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: greet
        id: greet
        uses: owner/repo@v1
        with:
          who-to-greet: World
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	step := wf.Jobs["build"].Steps[0]
	if step.Uses != "owner/repo@v1" {
		t.Errorf("Uses = %q, want %q", step.Uses, "owner/repo@v1")
	}
	if step.With["who-to-greet"] != "World" {
		t.Errorf(`With["who-to-greet"] = %q, want %q`, step.With["who-to-greet"], "World")
	}
}
```

```go
// internal/engine/executor_test.go — add this test
func TestRunJob_RejectsStepWithBothRunAndUses(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Run: "echo hi", Uses: "owner/repo@v1"}},
	}
	backend := &fakeBackend{}

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: t.TempDir()})
	if err == nil {
		t.Fatal("RunJob() error = nil, want error for a step with both run: and uses:")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/vishnu.prasaath/workspace/mirror-gha && go test ./internal/engine/... -run 'TestParse_UsesAndWith|TestRunJob_RejectsStepWithBothRunAndUses' -v`
Expected: FAIL — `Uses`/`With` undefined on `Step`, and the mutual-exclusion test fails because nothing currently rejects it (the fake backend's `Exec` gets called and panics on an empty `results` slice, or the step just runs `Run` silently — either way, not the error we want)

- [ ] **Step 3: Implement**

```go
// internal/engine/workflow.go — add these two fields to the Step struct
type Step struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Run              string            `yaml:"run"`
	Uses             string            `yaml:"uses"`
	With             map[string]string `yaml:"with"`
	Shell            string            `yaml:"shell"`
	Env              map[string]string `yaml:"env"`
	If               string            `yaml:"if"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	WorkingDirectory string            `yaml:"working-directory"`
	TimeoutMinutes   float64           `yaml:"timeout-minutes"`
}
```

In `internal/engine/executor.go`, inside `RunJob`'s step loop, right after computing `id` and before the `if step.If != ""` block, add:

```go
		if step.Run != "" && step.Uses != "" {
			return nil, fmt.Errorf("step %s: cannot set both run: and uses:", id)
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/engine/... -run 'TestParse_UsesAndWith|TestRunJob_RejectsStepWithBothRunAndUses' -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/engine/workflow.go internal/engine/executor.go internal/engine/workflow_test.go internal/engine/executor_test.go
git commit -m "feat(engine): add uses:/with: fields to Step"
```

---

### Task 2: `internal/actions` — parse `uses:` references

**Files:**
- Create: `internal/actions/resolve.go`
- Test: `internal/actions/resolve_test.go`

**Interfaces:**
- Produces: `type ActionRef struct{ Local bool; LocalPath, Owner, Repo, Subpath, Ref string }`, `func ResolveUsesRef(uses string) (ActionRef, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/actions/resolve_test.go
package actions

import "testing"

func TestResolveUsesRef_LocalPath(t *testing.T) {
	ref, err := ResolveUsesRef("./local/action")
	if err != nil {
		t.Fatalf("ResolveUsesRef() error = %v", err)
	}
	if !ref.Local {
		t.Error("Local = false, want true")
	}
	if ref.LocalPath != "./local/action" {
		t.Errorf("LocalPath = %q, want %q", ref.LocalPath, "./local/action")
	}
}

func TestResolveUsesRef_MarketplaceSimple(t *testing.T) {
	ref, err := ResolveUsesRef("actions/checkout@v4")
	if err != nil {
		t.Fatalf("ResolveUsesRef() error = %v", err)
	}
	if ref.Local {
		t.Error("Local = true, want false")
	}
	if ref.Owner != "actions" || ref.Repo != "checkout" || ref.Ref != "v4" {
		t.Errorf("Owner/Repo/Ref = %q/%q/%q, want %q/%q/%q", ref.Owner, ref.Repo, ref.Ref, "actions", "checkout", "v4")
	}
	if ref.Subpath != "" {
		t.Errorf("Subpath = %q, want empty", ref.Subpath)
	}
}

func TestResolveUsesRef_MarketplaceWithSubpath(t *testing.T) {
	ref, err := ResolveUsesRef("owner/repo/some/nested/action@v1")
	if err != nil {
		t.Fatalf("ResolveUsesRef() error = %v", err)
	}
	if ref.Owner != "owner" || ref.Repo != "repo" || ref.Ref != "v1" {
		t.Errorf("Owner/Repo/Ref = %q/%q/%q, want %q/%q/%q", ref.Owner, ref.Repo, ref.Ref, "owner", "repo", "v1")
	}
	if ref.Subpath != "some/nested/action" {
		t.Errorf("Subpath = %q, want %q", ref.Subpath, "some/nested/action")
	}
}

func TestResolveUsesRef_MissingRefIsError(t *testing.T) {
	_, err := ResolveUsesRef("actions/checkout")
	if err == nil {
		t.Fatal("ResolveUsesRef() error = nil, want error for a reference missing @ref")
	}
}

func TestResolveUsesRef_MissingRepoIsError(t *testing.T) {
	_, err := ResolveUsesRef("actions@v4")
	if err == nil {
		t.Fatal("ResolveUsesRef() error = nil, want error for a reference missing owner/repo")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/actions/... -v`
Expected: FAIL — package `internal/actions` doesn't exist yet

- [ ] **Step 3: Implement**

```go
// internal/actions/resolve.go
package actions

import (
	"fmt"
	"strings"
)

// ActionRef is a parsed `uses:` reference — either a local, workspace-relative
// path, or a Marketplace action identified by owner/repo (and an optional
// subpath, for actions that don't live at their repo's root) plus a ref
// (tag, branch, or commit SHA).
type ActionRef struct {
	Local     bool
	LocalPath string

	Owner   string
	Repo    string
	Subpath string
	Ref     string
}

// ResolveUsesRef parses a step's `uses:` value.
func ResolveUsesRef(uses string) (ActionRef, error) {
	if strings.HasPrefix(uses, "./") || strings.HasPrefix(uses, "../") {
		return ActionRef{Local: true, LocalPath: uses}, nil
	}

	atIdx := strings.LastIndex(uses, "@")
	if atIdx == -1 {
		return ActionRef{}, fmt.Errorf("uses %q is missing a @ref (e.g. owner/repo@v1)", uses)
	}
	repoPart, ref := uses[:atIdx], uses[atIdx+1:]
	if ref == "" {
		return ActionRef{}, fmt.Errorf("uses %q has an empty @ref", uses)
	}

	parts := strings.Split(repoPart, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ActionRef{}, fmt.Errorf("uses %q must be in the form owner/repo[/subpath]@ref", uses)
	}

	ref2 := ActionRef{Owner: parts[0], Repo: parts[1], Ref: ref}
	if len(parts) > 2 {
		ref2.Subpath = strings.Join(parts[2:], "/")
	}
	return ref2, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/actions/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/actions/resolve.go internal/actions/resolve_test.go
git commit -m "feat(actions): parse uses: references"
```

---

### Task 3: `internal/actions` — parse `action.yml`

**Files:**
- Create: `internal/actions/metadata.go`
- Test: `internal/actions/metadata_test.go`

**Interfaces:**
- Consumes: `mirror-gha/third_party/ghaexpr` is NOT needed here — this task only parses YAML via `gopkg.in/yaml.v3` (already a dependency via `internal/engine`).
- Produces: `type ActionInput struct{ Description string; Required bool; Default string }`, `type ActionRuns struct{ Using, Main string }`, `type ActionMetadata struct{ Name string; Inputs map[string]ActionInput; Outputs map[string]interface{}; Runs ActionRuns }`, `func ParseMetadata(dir string) (*ActionMetadata, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/actions/metadata_test.go
package actions

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestParseMetadata_ActionYml(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "action.yml", `
name: 'Hello Action'
description: 'says hello'
inputs:
  who-to-greet:
    description: 'who to greet'
    required: true
    default: 'World'
outputs:
  greeting:
    description: 'the greeting'
runs:
  using: 'node20'
  main: 'index.js'
`)

	meta, err := ParseMetadata(dir)
	if err != nil {
		t.Fatalf("ParseMetadata() error = %v", err)
	}
	if meta.Name != "Hello Action" {
		t.Errorf("Name = %q, want %q", meta.Name, "Hello Action")
	}
	input, ok := meta.Inputs["who-to-greet"]
	if !ok {
		t.Fatal(`Inputs["who-to-greet"] not found`)
	}
	if input.Default != "World" || !input.Required {
		t.Errorf("input = %+v, want Default=World Required=true", input)
	}
	if meta.Runs.Using != "node20" || meta.Runs.Main != "index.js" {
		t.Errorf("Runs = %+v, want Using=node20 Main=index.js", meta.Runs)
	}
}

func TestParseMetadata_ActionYamlExtension(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "action.yaml", `
name: 'Yaml Extension Action'
runs:
  using: 'node20'
  main: 'index.js'
`)

	meta, err := ParseMetadata(dir)
	if err != nil {
		t.Fatalf("ParseMetadata() error = %v", err)
	}
	if meta.Name != "Yaml Extension Action" {
		t.Errorf("Name = %q, want %q", meta.Name, "Yaml Extension Action")
	}
}

func TestParseMetadata_MissingFileIsError(t *testing.T) {
	_, err := ParseMetadata(t.TempDir())
	if err == nil {
		t.Fatal("ParseMetadata() error = nil, want error when neither action.yml nor action.yaml exists")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/actions/... -run TestParseMetadata -v`
Expected: FAIL — `ParseMetadata` undefined

- [ ] **Step 3: Implement**

```go
// internal/actions/metadata.go
package actions

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// ActionInput is one entry in an action.yml's `inputs:` map.
type ActionInput struct {
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	Default     string `yaml:"default"`
}

// ActionRuns is an action.yml's `runs:` block.
type ActionRuns struct {
	Using string `yaml:"using"`
	Main  string `yaml:"main"`
}

// ActionMetadata is a parsed action.yml/action.yaml.
type ActionMetadata struct {
	Name    string                 `yaml:"name"`
	Inputs  map[string]ActionInput `yaml:"inputs"`
	Outputs map[string]interface{} `yaml:"outputs"`
	Runs    ActionRuns             `yaml:"runs"`
}

// ParseMetadata reads action.yml or action.yaml from dir's root.
func ParseMetadata(dir string) (*ActionMetadata, error) {
	for _, name := range []string{"action.yml", "action.yaml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var meta ActionMetadata
		if err := yaml.Unmarshal(data, &meta); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		return &meta, nil
	}
	return nil, fmt.Errorf("neither action.yml nor action.yaml found in %s", dir)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/actions/... -run TestParseMetadata -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/actions/metadata.go internal/actions/metadata_test.go
git commit -m "feat(actions): parse action.yml metadata"
```

---

### Task 4: `internal/actions` — `INPUT_*` env var transform

**Files:**
- Create: `internal/actions/input_env.go`
- Test: `internal/actions/input_env_test.go`

**Interfaces:**
- Consumes: `ActionMetadata`, `ActionInput` (Task 3)
- Produces: `func InputEnv(metadata *ActionMetadata, with map[string]string) map[string]string`

- [ ] **Step 1: Write the failing test**

```go
// internal/actions/input_env_test.go
package actions

import "testing"

func TestInputEnv_UsesDefaultWhenNotOverridden(t *testing.T) {
	meta := &ActionMetadata{
		Inputs: map[string]ActionInput{
			"who-to-greet": {Default: "World"},
		},
	}
	env := InputEnv(meta, map[string]string{})
	if env["INPUT_WHO-TO-GREET"] != "World" {
		t.Errorf(`env["INPUT_WHO-TO-GREET"] = %q, want %q`, env["INPUT_WHO-TO-GREET"], "World")
	}
}

func TestInputEnv_WithOverridesDefault(t *testing.T) {
	meta := &ActionMetadata{
		Inputs: map[string]ActionInput{
			"who-to-greet": {Default: "World"},
		},
	}
	env := InputEnv(meta, map[string]string{"who-to-greet": "mirror-gha"})
	if env["INPUT_WHO-TO-GREET"] != "mirror-gha" {
		t.Errorf(`env["INPUT_WHO-TO-GREET"] = %q, want %q`, env["INPUT_WHO-TO-GREET"], "mirror-gha")
	}
}

func TestInputEnv_NonAlphanumericBecomesUnderscore(t *testing.T) {
	meta := &ActionMetadata{Inputs: map[string]ActionInput{}}
	env := InputEnv(meta, map[string]string{"my input.name": "value"})
	if env["INPUT_MY_INPUT_NAME"] != "value" {
		t.Errorf("env = %v, want INPUT_MY_INPUT_NAME=value", env)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/actions/... -run TestInputEnv -v`
Expected: FAIL — `InputEnv` undefined

- [ ] **Step 3: Implement**

```go
// internal/actions/input_env.go
package actions

import (
	"regexp"
	"strings"
)

var inputEnvSanitizer = regexp.MustCompile(`[^A-Z0-9-]`)

func inputEnvKey(name string) string {
	return "INPUT_" + inputEnvSanitizer.ReplaceAllString(strings.ToUpper(name), "_")
}

// InputEnv computes the INPUT_* environment variables for an action
// invocation: action.yml's declared defaults, overridden by whatever the
// step's `with:` block set. with's values must already be fully resolved
// (expression-substituted) by the caller.
func InputEnv(metadata *ActionMetadata, with map[string]string) map[string]string {
	env := map[string]string{}
	for name, input := range metadata.Inputs {
		if input.Default != "" {
			env[inputEnvKey(name)] = input.Default
		}
	}
	for name, val := range with {
		env[inputEnvKey(name)] = val
	}
	return env
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/actions/... -run TestInputEnv -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/actions/input_env.go internal/actions/input_env_test.go
git commit -m "feat(actions): compute INPUT_ env vars from action.yml + with:"
```

---

### Task 5: `internal/actions` — cache root + remote action fetch

**Files:**
- Create: `internal/actions/cache.go`
- Create: `internal/actions/fetch.go`
- Test: `internal/actions/fetch_test.go`

**Interfaces:**
- Produces: `func CacheRoot() (string, error)`, `func FetchRemote(owner, repo, ref, cacheRoot string) (string, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/actions/fetch_test.go
package actions

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// requireNetwork skips the test if there's no working internet connection —
// mirrors internal/runner's requireDocker(t) pattern for an external
// dependency the test needs but can't assume is present.
func requireNetwork(t *testing.T) {
	t.Helper()
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Head("https://codeload.github.com")
	if err != nil {
		t.Skipf("no network connectivity, skipping: %v", err)
	}
	resp.Body.Close()
}

func TestFetchRemote_DownloadsAndCaches(t *testing.T) {
	requireNetwork(t)

	cacheRoot := t.TempDir()
	// actions/hello-world-javascript-action is GitHub's own small, stable
	// official demo action — a real, public, unauthenticated fetch target.
	dir, err := FetchRemote("actions", "hello-world-javascript-action", "v1", cacheRoot)
	if err != nil {
		t.Fatalf("FetchRemote() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "action.yml")); err != nil {
		t.Errorf("expected action.yml in %s: %v", dir, err)
	}

	// Second call must hit the cache, not fetch again — prove this by
	// checking the directory is returned identically and still valid.
	dir2, err := FetchRemote("actions", "hello-world-javascript-action", "v1", cacheRoot)
	if err != nil {
		t.Fatalf("FetchRemote() second call error = %v", err)
	}
	if dir2 != dir {
		t.Errorf("second FetchRemote() = %q, want same path %q", dir2, dir)
	}
}

func TestFetchRemote_UnknownRepoIsError(t *testing.T) {
	requireNetwork(t)

	_, err := FetchRemote("mirror-gha-nonexistent-owner-xyz", "nonexistent-repo-xyz", "v1", t.TempDir())
	if err == nil {
		t.Fatal("FetchRemote() error = nil, want error for a nonexistent repo")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/actions/... -run TestFetchRemote -v`
Expected: FAIL — `FetchRemote` undefined

- [ ] **Step 3: Implement**

```go
// internal/actions/cache.go
package actions

import (
	"os"
	"path/filepath"
)

// CacheRoot is where mirror-gha caches downloaded action source and the
// pinned Node runtime — os.UserCacheDir()/mirror-gha (e.g. ~/.cache/mirror-gha
// on Linux, ~/Library/Caches/mirror-gha on macOS).
func CacheRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mirror-gha"), nil
}
```

```go
// internal/actions/fetch.go
package actions

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// FetchRemote downloads and caches a Marketplace action's source at
// cacheRoot/actions/<owner>/<repo>/<ref>/, keyed by the literal ref string
// (a mutable branch ref won't auto-refresh — accepted, matches act's own
// cache-by-ref behavior). Uses curl + tar rather than Go's net/http/
// archive packages: this machine's network already broke Go's own TLS
// trust this session, while curl's Security-framework-backed stack
// handles it fine — see the design spec for the full reasoning.
func FetchRemote(owner, repo, ref, cacheRoot string) (string, error) {
	dest := filepath.Join(cacheRoot, "actions", owner, repo, ref)
	if _, err := os.Stat(filepath.Join(dest, "action.yml")); err == nil {
		return dest, nil
	}
	if _, err := os.Stat(filepath.Join(dest, "action.yaml")); err == nil {
		return dest, nil
	}

	tmpFile, err := os.CreateTemp("", "mirror-action-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	url := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/%s", owner, repo, ref)
	curl := exec.Command("curl", "-fsSL", "-o", tmpPath, url)
	if out, err := curl.CombinedOutput(); err != nil {
		return "", fmt.Errorf("download %s: %w: %s", url, err, out)
	}

	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("clear stale cache dir %s: %w", dest, err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir %s: %w", dest, err)
	}

	// codeload tarballs contain one top-level dir (e.g. repo-ref/) —
	// strip-components=1 flattens the action's own files directly into dest.
	tarCmd := exec.Command("tar", "-xzf", tmpPath, "-C", dest, "--strip-components=1")
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("extract %s: %w: %s", tmpPath, err, out)
	}

	return dest, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/actions/... -run TestFetchRemote -v`
Expected: PASS (skips cleanly if there's no network)

- [ ] **Step 5: Commit**

```bash
git add internal/actions/cache.go internal/actions/fetch.go internal/actions/fetch_test.go
git commit -m "feat(actions): fetch and cache remote action source via curl/tar"
```

---

### Task 6: `internal/actions` — pinned Node runtime

**Files:**
- Create: `internal/actions/node.go`
- Test: `internal/actions/node_test.go`

**Interfaces:**
- Consumes: `CacheRoot` pattern established in Task 5 (caller supplies cacheRoot directly)
- Produces: `const PinnedNodeVersion = "20.11.1"`, `const ContainerNodePath = "/mirror-node"`, `func ContainerActionPath(stepID string) string`, `func EnsureNode(cacheRoot string) (string, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/actions/node_test.go
package actions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureNode_DownloadsAndCaches(t *testing.T) {
	requireNetwork(t)

	cacheRoot := t.TempDir()
	dir, err := EnsureNode(cacheRoot)
	if err != nil {
		t.Fatalf("EnsureNode() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "node")); err != nil {
		t.Errorf("expected bin/node in %s: %v", dir, err)
	}

	dir2, err := EnsureNode(cacheRoot)
	if err != nil {
		t.Fatalf("EnsureNode() second call error = %v", err)
	}
	if dir2 != dir {
		t.Errorf("second EnsureNode() = %q, want same path %q", dir2, dir)
	}
}

func TestContainerActionPath(t *testing.T) {
	got := ContainerActionPath("emit")
	want := "/mirror-actions/emit"
	if got != want {
		t.Errorf("ContainerActionPath(emit) = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/actions/... -run 'TestEnsureNode|TestContainerActionPath' -v`
Expected: FAIL — `EnsureNode`/`ContainerActionPath` undefined

- [ ] **Step 3: Implement**

```go
// internal/actions/node.go
package actions

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// PinnedNodeVersion is the single Node build used for every JS action,
// regardless of that action's declared runs.using (node16/node20/etc.) —
// matches act's own behavior (verified against its source: it doesn't
// resolve per-version Node binaries either, it just needs *a* Node
// present and uses it uniformly).
const PinnedNodeVersion = "20.11.1"

// ContainerNodePath is where the pinned Node runtime is copied inside a
// job container, once per job (see internal/engine's uses: step handling).
const ContainerNodePath = "/mirror-node"

// ContainerActionPath is where a given step's action source is copied
// inside the job container — one per uses: step, keyed by that step's ID
// so distinct actions in the same job never collide.
func ContainerActionPath(stepID string) string {
	return "/mirror-actions/" + stepID
}

func nodeArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture for the Node runtime: %s", runtime.GOARCH)
	}
}

// EnsureNode downloads and caches the pinned Node build at
// cacheRoot/node/<version>/<arch>/, returning that directory. Assumes the
// job container's architecture matches the host's (true for default
// Docker Desktop behavior) — a documented, not-yet-configurable assumption.
func EnsureNode(cacheRoot string) (string, error) {
	arch, err := nodeArch()
	if err != nil {
		return "", err
	}

	dest := filepath.Join(cacheRoot, "node", PinnedNodeVersion, arch)
	if _, err := os.Stat(filepath.Join(dest, "bin", "node")); err == nil {
		return dest, nil
	}

	tmpFile, err := os.CreateTemp("", "mirror-node-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	url := fmt.Sprintf("https://nodejs.org/dist/v%s/node-v%s-linux-%s.tar.gz", PinnedNodeVersion, PinnedNodeVersion, arch)
	curl := exec.Command("curl", "-fsSL", "-o", tmpPath, url)
	if out, err := curl.CombinedOutput(); err != nil {
		return "", fmt.Errorf("download %s: %w: %s", url, err, out)
	}

	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("clear stale node cache dir %s: %w", dest, err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("create node cache dir %s: %w", dest, err)
	}

	tarCmd := exec.Command("tar", "-xzf", tmpPath, "-C", dest, "--strip-components=1")
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("extract %s: %w: %s", tmpPath, err, out)
	}

	return dest, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/actions/... -v`
Expected: PASS — full package suite (skips network-dependent tests cleanly if offline)

- [ ] **Step 5: Commit**

```bash
git add internal/actions/node.go internal/actions/node_test.go
git commit -m "feat(actions): fetch and cache the pinned Node runtime"
```

---

### Task 7: `runner.Job` — `CopyToContainer`

**Files:**
- Modify: `internal/runner/backend.go`
- Modify: `internal/runner/docker_backend.go`
- Modify: `internal/runner/dryrun.go`
- Test: `internal/runner/docker_backend_test.go`, `internal/runner/dryrun_test.go`

**Interfaces:**
- Produces: `Job.CopyToContainer(ctx context.Context, hostPath, containerPath string) error`

- [ ] **Step 1: Write the failing tests**

```go
// internal/runner/docker_backend_test.go — add this test
func TestLinuxDockerBackend_CopyToContainer(t *testing.T) {
	requireDocker(t)

	hostDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostDir, "hello.txt"), []byte("copied\n"), 0o644); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	if err := job.CopyToContainer(context.Background(), hostDir, "/mirror-copy-test"); err != nil {
		t.Fatalf("CopyToContainer() error = %v", err)
	}

	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "cat /mirror-copy-test/hello.txt",
		Shell:    "sh",
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "copied") {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, "copied")
	}
}
```

```go
// internal/runner/dryrun_test.go — add this test
func TestDryRunBackend_CopyToContainerIsNoOp(t *testing.T) {
	backend := DryRunBackend{}
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	if err := job.CopyToContainer(context.Background(), t.TempDir(), "/anywhere"); err != nil {
		t.Errorf("CopyToContainer() error = %v, want nil (no-op in dry-run mode)", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runner/... -run 'CopyToContainer' -v`
Expected: FAIL — `CopyToContainer` undefined on `Job`

- [ ] **Step 3: Implement**

In `internal/runner/backend.go`, add to the `Job` interface (after `WorkspacePath`):

```go
	// CopyToContainer injects hostPath into the running environment at
	// containerPath — the mechanism uses: steps use to stage a JS action's
	// source and the pinned Node runtime, matching act's own on-demand
	// docker-cp-style injection rather than a mount declared at StartJob.
	CopyToContainer(ctx context.Context, hostPath, containerPath string) error
```

In `internal/runner/docker_backend.go`, add:

```go
func (j *dockerJob) CopyToContainer(ctx context.Context, hostPath, containerPath string) error {
	cmd := exec.CommandContext(ctx, "docker", "cp", hostPath, j.containerID+":"+containerPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker cp %s -> %s:%s: %w: %s", hostPath, j.containerID, containerPath, err, stderr.String())
	}
	return nil
}
```

In `internal/runner/dryrun.go`, add:

```go
func (j *dryRunJob) CopyToContainer(ctx context.Context, hostPath, containerPath string) error {
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runner/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/runner/backend.go internal/runner/docker_backend.go internal/runner/dryrun.go internal/runner/docker_backend_test.go internal/runner/dryrun_test.go
git commit -m "feat(runner): add Job.CopyToContainer"
```

---

### Task 8: Wire `uses:` steps into the job executor

**Files:**
- Create: `internal/engine/uses_step.go`
- Modify: `internal/engine/executor.go`
- Test: `internal/engine/uses_step_test.go`, `internal/engine/executor_test.go`

**Interfaces:**
- Consumes: `actions.ResolveUsesRef`, `actions.CacheRoot`, `actions.FetchRemote`, `actions.ParseMetadata`, `actions.EnsureNode`, `actions.InputEnv`, `actions.ContainerNodePath`, `actions.ContainerActionPath` (Tasks 2-6), `runner.Job.CopyToContainer` (Task 7)
- Produces: `func prepareUsesStep(ctx context.Context, job runner.Job, workspaceDir, stepID string, step Step, actx *Context, nodeReady *bool) (command string, extraEnv map[string]string, err error)`

Update `executor_test.go`'s `fakeJob` (from earlier tasks) to implement the new `CopyToContainer` method — it's a no-op like `DryRunBackend`'s, since these tests exercise orchestration, not real Docker.

- [ ] **Step 1: Write the failing test**

```go
// internal/engine/uses_step_test.go
package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"mirror-gha/internal/runner"
)

func TestPrepareUsesStep_LocalAction(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "my-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := `
name: 'Test Action'
inputs:
  greeting:
    default: 'hello'
runs:
  using: 'node20'
  main: 'index.js'
`
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./my-action", With: map[string]string{"greeting": "hi"}}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := false

	command, env, err := prepareUsesStep(context.Background(), job, workspaceDir, "greet", step, actx, &nodeReady)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v", err)
	}

	wantCommand := "/mirror-node/bin/node /mirror-actions/greet/index.js"
	if command != wantCommand {
		t.Errorf("command = %q, want %q", command, wantCommand)
	}
	if env["INPUT_GREETING"] != "hi" {
		t.Errorf(`env["INPUT_GREETING"] = %q, want %q`, env["INPUT_GREETING"], "hi")
	}
	if env["GITHUB_ACTION_PATH"] != "/mirror-actions/greet" {
		t.Errorf(`env["GITHUB_ACTION_PATH"] = %q, want %q`, env["GITHUB_ACTION_PATH"], "/mirror-actions/greet")
	}
	if !nodeReady {
		t.Error("nodeReady = false, want true after the first uses: step")
	}
}

func TestPrepareUsesStep_RejectsNonNodeRuntime(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "docker-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := `
name: 'Docker Action'
runs:
  using: 'docker'
  image: 'Dockerfile'
`
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./docker-action"}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := true // already true, so we know the rejection isn't a Node-setup failure

	_, _, err := prepareUsesStep(context.Background(), job, workspaceDir, "one", step, actx, &nodeReady)
	if err == nil {
		t.Fatal("prepareUsesStep() error = nil, want error for runs.using: docker")
	}
}
```

Also add a `CopyToContainer` no-op to `fakeJob` in `executor_test.go`, and a test proving `uses:` steps ignore job/workflow working-directory defaults:

```go
func (j *fakeJob) CopyToContainer(ctx context.Context, hostPath, containerPath string) error {
	return nil
}
```

```go
// internal/engine/executor_test.go — add this test
func TestRunJob_UsesStepIgnoresWorkingDirectoryDefaults(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "my-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := "name: 'Test'\nruns:\n  using: 'node20'\n  main: 'index.js'\n"
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn:   "ubuntu-latest",
		Defaults: &Defaults{Run: RunDefaults{WorkingDirectory: "/should-not-be-used"}},
		Steps:    []Step{{ID: "one", Uses: "./my-action"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: workspaceDir})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}

	spec := backend.lastJob.execSpecs[0]
	if spec.WorkingDirectory != workspaceDir {
		t.Errorf("WorkingDirectory = %q, want %q (uses: steps must ignore defaults.run.working-directory)", spec.WorkingDirectory, workspaceDir)
	}
}
```

This test needs `"os"` and `"path/filepath"` imported in `executor_test.go` if not already present (they are, from earlier tasks' workspace tests).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/... -run 'TestPrepareUsesStep|TestRunJob_UsesStepIgnoresWorkingDirectoryDefaults' -v`
Expected: FAIL — `prepareUsesStep` undefined, and `fakeJob` doesn't implement `runner.Job` yet (missing `CopyToContainer`)

- [ ] **Step 3: Implement**

```go
// internal/engine/uses_step.go
package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"mirror-gha/internal/actions"
	"mirror-gha/internal/runner"
)

// prepareUsesStep resolves and stages a uses: step's action inside the
// running job — copying the pinned Node runtime in once per job, and this
// step's action source in fresh every time — and returns the exec command
// plus the extra env vars (INPUT_*, GITHUB_ACTION_PATH) to merge into the
// step's environment. actx.Steps/actx.Env etc. are used only for
// expression substitution inside with: values; the caller still owns the
// workflow-command file protocol and output parsing, identical to run:
// steps.
func prepareUsesStep(ctx context.Context, job runner.Job, workspaceDir, stepID string, step Step, actx *Context, nodeReady *bool) (string, map[string]string, error) {
	with := map[string]string{}
	for k, v := range step.With {
		val, err := SubstituteExpressions(v, actx)
		if err != nil {
			return "", nil, fmt.Errorf("substitute with.%s: %w", k, err)
		}
		with[k] = val
	}

	ref, err := actions.ResolveUsesRef(step.Uses)
	if err != nil {
		return "", nil, err
	}

	cacheRoot, err := actions.CacheRoot()
	if err != nil {
		return "", nil, fmt.Errorf("resolve cache root: %w", err)
	}

	var hostSourceDir string
	if ref.Local {
		hostSourceDir = filepath.Join(workspaceDir, ref.LocalPath)
	} else {
		actionDir, err := actions.FetchRemote(ref.Owner, ref.Repo, ref.Ref, cacheRoot)
		if err != nil {
			return "", nil, fmt.Errorf("fetch action %s: %w", step.Uses, err)
		}
		hostSourceDir = actionDir
		if ref.Subpath != "" {
			hostSourceDir = filepath.Join(actionDir, ref.Subpath)
		}
	}

	metadata, err := actions.ParseMetadata(hostSourceDir)
	if err != nil {
		return "", nil, fmt.Errorf("parse action metadata for %s: %w", step.Uses, err)
	}

	if !strings.HasPrefix(metadata.Runs.Using, "node") {
		return "", nil, fmt.Errorf("action %s has runs.using=%q, which isn't supported yet (only JS/node actions run today)", step.Uses, metadata.Runs.Using)
	}

	if !*nodeReady {
		nodeDir, err := actions.EnsureNode(cacheRoot)
		if err != nil {
			return "", nil, fmt.Errorf("ensure node runtime: %w", err)
		}
		if err := job.CopyToContainer(ctx, nodeDir, actions.ContainerNodePath); err != nil {
			return "", nil, fmt.Errorf("copy node runtime into job: %w", err)
		}
		*nodeReady = true
	}

	containerActionPath := actions.ContainerActionPath(stepID)
	if err := job.CopyToContainer(ctx, hostSourceDir, containerActionPath); err != nil {
		return "", nil, fmt.Errorf("copy action %s into job: %w", step.Uses, err)
	}

	env := actions.InputEnv(metadata, with)
	env["GITHUB_ACTION_PATH"] = containerActionPath

	command := fmt.Sprintf("%s/bin/node %s/%s", actions.ContainerNodePath, containerActionPath, metadata.Runs.Main)
	return command, env, nil
}
```

Now wire it into `internal/engine/executor.go`'s `RunJob`. Add `nodeReady := false` right after `actx.GitHub["workspace"] = ...`, then replace the command-computation block inside the step loop:

```go
		command, err := SubstituteExpressions(step.Run, actx)
		if err != nil {
			return nil, fmt.Errorf("substitute expressions for step %s: %w", id, err)
		}
```

with:

```go
		var command string
		var usesEnv map[string]string
		if step.Uses != "" {
			command, usesEnv, err = prepareUsesStep(ctx, runnerJob, opts.WorkspaceDir, id, step, actx, &nodeReady)
			if err != nil {
				return nil, fmt.Errorf("prepare uses: step %s: %w", id, err)
			}
		} else {
			command, err = SubstituteExpressions(step.Run, actx)
			if err != nil {
				return nil, fmt.Errorf("substitute expressions for step %s: %w", id, err)
			}
		}
```

And where `env` is built (the `env := map[string]string{}` block), add `usesEnv`'s entries after the existing merges:

```go
		env := map[string]string{}
		for k, v := range actx.Env {
			env[k] = v
		}
		for k, v := range step.Env {
			env[k] = v
		}
		env["GITHUB_WORKSPACE"] = runnerJob.WorkspacePath()
		for k, v := range usesEnv {
			env[k] = v
		}
```

`uses:` steps must always run in the job workspace, ignoring even job/workflow `defaults.run.working-directory` — real GitHub Actions doesn't let `uses:` steps override their working directory at all. Find this existing line further down in the step loop:

```go
		workingDirectory := effectiveWorkingDirectory(step, job, wf)
		if workingDirectory == "" {
			workingDirectory = runnerJob.WorkspacePath()
		}
```

and replace it with:

```go
		workingDirectory := runnerJob.WorkspacePath()
		if step.Uses == "" {
			if wd := effectiveWorkingDirectory(step, job, wf); wd != "" {
				workingDirectory = wd
			}
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/engine/... -v`
Expected: PASS — full engine package suite

- [ ] **Step 5: Commit**

```bash
git add internal/engine/uses_step.go internal/engine/uses_step_test.go internal/engine/executor.go internal/engine/executor_test.go
git commit -m "feat(engine): execute uses: steps"
```

---

### Task 9: Real end-to-end verification — local fixture + Marketplace action

**Files:**
- Create: `examples/workflows/actions/hello-action/action.yml`
- Create: `examples/workflows/actions/hello-action/index.js`
- Create: `examples/workflows/uses-local-action.yml`
- Create: `examples/workflows/uses-marketplace-action.yml`
- Modify: `examples/README.md`, `docs/usage.md`, `CHANGELOG.md`, `docs/design/specs/2026-09-14-mirror-gha-design.md`

**Interfaces:**
- None new — this task is the real, no-fakes proof that Tasks 1-8 work together end-to-end.

- [ ] **Step 1: Create the local action fixture**

```yaml
# examples/workflows/actions/hello-action/action.yml
name: 'Hello Action'
description: 'A minimal local JS action, for testing uses: with mirror-gha'
inputs:
  who-to-greet:
    description: 'Who to greet'
    required: true
    default: 'World'
outputs:
  greeting:
    description: 'The greeting message'
runs:
  using: 'node20'
  main: 'index.js'
```

```js
// examples/workflows/actions/hello-action/index.js
// No npm dependencies on purpose — this fixture has to run with nothing
// but the Node runtime mirror-gha copies in. It uses the exact same
// GITHUB_OUTPUT file-append mechanism @actions/core's setOutput() uses
// internally, just written by hand.
const fs = require('fs');

const who = process.env['INPUT_WHO-TO-GREET'] || 'World';
const greeting = `Hello, ${who}!`;
console.log(greeting);

const outputFile = process.env['GITHUB_OUTPUT'];
if (outputFile) {
  fs.appendFileSync(outputFile, `greeting=${greeting}\n`);
}
```

- [ ] **Step 2: Create the local-action example workflow**

```yaml
# examples/workflows/uses-local-action.yml
# Demonstrates uses: with a local action — resolves ./path relative to the
# job workspace (repo root, by default), executes its Node entry point for
# real, and reads its output back via steps.<id>.outputs.<name>.
#
# Try it (from the repo root):
#   mirror run examples/workflows/uses-local-action.yml
name: uses local action
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: greet
        id: greet
        uses: ./examples/workflows/actions/hello-action
        with:
          who-to-greet: 'mirror-gha'
      - name: use the output
        run: echo "Got greeting: ${{ steps.greet.outputs.greeting }}"
```

- [ ] **Step 3: Run it for real and verify the output**

Run: `cd /Users/vishnu.prasaath/workspace/mirror-gha && go build -o bin/mirror ./cmd/mirror && ./bin/mirror run examples/workflows/uses-local-action.yml`
Expected: both steps report `success`; output includes `Hello, mirror-gha!` and `Got greeting: Hello, mirror-gha!`

- [ ] **Step 4: Create the Marketplace-action example workflow**

```yaml
# examples/workflows/uses-marketplace-action.yml
# Demonstrates uses: with a real Marketplace action — actions/hello-world-javascript-action
# is GitHub's own small, stable, official demo JS action. Requires network
# access (fetches the action's source from GitHub on first run, then caches it).
#
# Try it:
#   mirror run examples/workflows/uses-marketplace-action.yml
name: uses marketplace action
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: greet
        id: greet
        uses: actions/hello-world-javascript-action@v1
        with:
          who-to-greet: 'mirror-gha'
      - name: use the output
        run: echo "Action ran at ${{ steps.greet.outputs.time }}"
```

- [ ] **Step 5: Run it for real and verify the output**

Run: `./bin/mirror run examples/workflows/uses-marketplace-action.yml`
Expected: both steps report `success`; output includes `Hello mirror-gha!` (from the action's own console.log) and `Action ran at <timestamp>`

- [ ] **Step 6: Re-run the full existing example suite to check for regressions**

Run:
```bash
for f in examples/workflows/*.yml examples/workflows/*/*.yml; do
  [ -f "$f" ] || continue
  echo "=== $f ==="
  ./bin/mirror run "$f" || echo "FAILED: $f"
done
```
Expected: every workflow prints `success` for all its steps; no `FAILED` lines. (The glob `examples/workflows/*/*.yml` would also match the fixture action's own files if it had a `.yml` workflow in it, which it doesn't — only `action.yml`, which `mirror run` will reject as an invalid workflow if accidentally targeted; skip it, it's not a workflow file.)

- [ ] **Step 7: Update documentation**

In `examples/README.md`, add to the table:

```markdown
| [`uses-local-action.yml`](workflows/uses-local-action.yml) | `uses:` with a local JS action — real Node execution, inputs via `with:`, output read back via `steps.<id>.outputs` |
| [`uses-marketplace-action.yml`](workflows/uses-marketplace-action.yml) | `uses:` with a real Marketplace action (`actions/hello-world-javascript-action`) — fetched and cached from GitHub |
```

And update its "What's not shown here (yet)" paragraph to drop `uses:` actions from the unsupported list (Docker and composite actions remain unsupported — update the wording to say so specifically instead of a blanket "uses: actions").

In `docs/usage.md`, add a bullet under "What's supported today":

```markdown
- **`uses:` JS actions** — Marketplace (`owner/repo[/subpath]@ref`) and local
  (`./path`) actions, executed for real against a pinned Node runtime.
  `with:` inputs and outputs via `$GITHUB_OUTPUT` work exactly like `run:`
  steps. Docker and composite actions (`runs.using: docker`/`composite`)
  are rejected with a clear error, not approximated.
```

And remove `uses:` actions from the "What's not supported yet" list, replacing it with:

```markdown
- Docker and composite actions (`uses:` JS actions and local paths work; `runs.using: docker`/`composite` do not)
```

In `CHANGELOG.md`, add under `### Added`:

```markdown
- **`uses:` JS actions.** Marketplace (`owner/repo[/subpath]@ref`) and
  local (`./path`) actions execute for real: source fetched via
  `curl`/`tar` (not Go's `net/http` — see design spec) and cached at
  `~/.cache/mirror-gha/actions/`, run against one pinned Node build
  (`~/.cache/mirror-gha/node/`) copied into the job container via
  `docker cp` (`runner.Job.CopyToContainer`), `with:` inputs mapped to
  `INPUT_*` env vars exactly as GitHub Actions does. Docker and
  composite actions are rejected with a clear error. Verified for real
  against both a local fixture action and `actions/hello-world-javascript-action`
  (GitHub's own official demo action).
```

In `docs/design/specs/2026-09-14-mirror-gha-design.md`, change the "JS Actions Runtime" section's opening line from `**Scope:**` to note implementation status — add right after the heading:

```markdown
**Implemented.**
```

- [ ] **Step 8: Full verification pass**

Run:
```bash
go build ./... && go test ./... && make fmt-check && go vet ./...
```
Expected: build succeeds, all tests pass (network/Docker-dependent ones skip cleanly if unavailable), `fmt-check` and `vet` produce no output/errors.

- [ ] **Step 9: Commit**

```bash
git add examples/workflows/actions examples/workflows/uses-local-action.yml examples/workflows/uses-marketplace-action.yml examples/README.md docs/usage.md CHANGELOG.md docs/design/specs/2026-09-14-mirror-gha-design.md
git commit -m "feat: verify JS actions end-to-end with local + Marketplace examples"
```

## Self-Review Notes

- **Spec coverage:** Every piece of the "JS Actions Runtime" spec section maps to a task — Step model (Task 1), action resolution (Task 2), metadata parsing (Task 3), INPUT_ transform (Task 4), fetch/cache (Task 5), Node runtime (Task 6), container injection (Task 7), execution wiring (Task 8), end-to-end proof (Task 9). Docker/composite actions are explicitly out of scope per the spec and rejected, not implemented.
- **Placeholder scan:** No TBD/TODO; every step has complete, real code.
- **Type consistency:** `ActionRef`, `ActionMetadata`, `ActionInput`, `ActionRuns` (Tasks 2-3) are used with identical field names in Tasks 4, 5, 8. `prepareUsesStep`'s signature in Task 8's interface block matches its actual definition and its call site in `executor.go`. `Job.CopyToContainer`'s signature (Task 7) matches its use in Task 8's `prepareUsesStep` and its fixture in `executor_test.go`'s `fakeJob`.
