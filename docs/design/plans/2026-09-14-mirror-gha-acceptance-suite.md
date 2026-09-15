# Real End-to-End Acceptance Test Suite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A permanent, CI-enforced regression suite that runs the real, compiled `mirror` binary against real workflow files (real Docker containers, real network fetches, real cache/artifact servers), asserting on real output — covering every feature `docs/usage.md` claims mirror-gha supports.

**Architecture:** A new `acceptance/` package at the repo root. A shared harness (`binary()`/`run()`) builds the real CLI once and shells out to it per test via `os/exec`, capturing real stdout/stderr/exit code — never calling internal Go functions directly. Three tiers: a self-maintaining corpus smoke test over `examples/workflows/*.yml`, detailed feature-scenario tests (reusing existing examples where an exact fit exists, new fixtures under `acceptance/testdata/` otherwise), and known-gap contract tests. Folds into the existing `make test`/CI `go test ./...` sweep — no new CI job.

**Tech Stack:** Go 1.27 stdlib only — `os/exec`, `sync.Once`, `context.WithTimeout`.

**Spec:** `docs/design/specs/2026-09-14-mirror-gha-design.md`, "Real End-to-End Acceptance Test Suite" section.

## Global Constraints

- Every test shells out to the real compiled `mirror` binary via `os/exec` — no test in this package calls `runCommand()`/`runMain()`/any `internal/` package function directly.
- Reuse an existing `examples/workflows/*.yml` file for a scenario wherever one already fits exactly; only add a new fixture under `acceptance/testdata/` for a combination with no existing example.
- Every Docker-touching test uses `requireDocker(t)`; network-fetching tests also use `requireNetwork(t)`; the macOS-specific test uses `requireDarwin(t)` for its success-path assertion — all duplicated per this package's own file, matching every other package's existing precedent in this codebase.
- Assertions target real, specific substrings of actual output (`strings.Contains`), not just exit codes — an assertion that would pass against a broken implementation is not a real test.
- When a test finds a real bug, fix the actual bug (with its own regression test in the relevant `internal/` package if the fix belongs there) before moving on — never weaken the acceptance assertion to make it pass.

---

### Task 1: Harness — build-once binary, real subprocess exec, output capture

**Files:**
- Create: `acceptance/harness_test.go`

**Interfaces:**
- Produces: `func binary(t *testing.T) string`, `type runResult struct { Stdout, Stderr string; ExitCode int }`, `func run(t *testing.T, workdir string, timeout time.Duration, args ...string) runResult`, `func repoRoot() string`, `func examplePath(name string) string`, `func requireDocker(t *testing.T)`, `func requireNetwork(t *testing.T)`, `func requireDarwin(t *testing.T)`. Every later task in this plan consumes these exact names/signatures.

- [ ] **Step 1: Write the harness itself (no separate test needed — see Global Constraints' note on testing the harness)**

Create `acceptance/harness_test.go`:

```go
package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// binary builds the real mirror CLI once per test run (shared by every
// test in this package) and returns its path. Every test in this suite
// shells out to this exact compiled artifact via os/exec — exactly as a
// real user invoking `mirror` from a shell would — rather than calling
// internal Go functions directly. This is a deliberate acceptance-test
// design choice: it exercises real CLI flag parsing and the real process
// boundary, and each subprocess gets its own real stdout/stderr, so these
// tests never need the fragile global os.Stdout swapping an in-process
// approach would require.
func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mirror-acceptance-bin-")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "mirror")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/mirror")
		cmd.Dir = repoRoot()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			buildErr = fmt.Errorf("build mirror binary: %w: %s", err, stderr.String())
		}
	})
	if buildErr != nil {
		t.Fatalf("binary() error = %v", buildErr)
	}
	return binPath
}

// repoRoot returns this repo's root, computed relative to this source
// file (acceptance/harness_test.go) so `go build ./cmd/mirror` and
// examplePath() resolve correctly regardless of the test runner's own
// working directory.
func repoRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(thisFile)) // acceptance/ -> repo root
}

// examplePath resolves a filename under examples/workflows/ to its real
// absolute path.
func examplePath(name string) string {
	return filepath.Join(repoRoot(), "examples", "workflows", name)
}

// testdataPath resolves a filename under acceptance/testdata/ to its
// real absolute path.
func testdataPath(name string) string {
	return filepath.Join(repoRoot(), "acceptance", "testdata", name)
}

// runResult is what run() captures from one real mirror invocation.
type runResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// run shells out to the real, compiled mirror binary with args, from
// workdir (the job workspace a real user would already be in) —
// capturing real stdout/stderr and the real process exit code. Fails the
// test outright (not just returning a non-zero ExitCode) only if the
// process couldn't be started/run at all (a real infrastructure problem,
// distinct from the workflow itself failing, which is a normal,
// assertable ExitCode).
func run(t *testing.T, workdir string, timeout time.Duration, args ...string) runResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary(t), args...)
	cmd.Dir = workdir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run mirror %v: %v (stderr: %s)", args, err, stderr.String())
		}
	}
	return runResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}
}

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping acceptance test")
	}
}

func requireNetwork(t *testing.T) {
	t.Helper()
	if err := exec.Command("curl", "-sS", "-o", os.DevNull, "--max-time", "5", "https://nodejs.org").Run(); err != nil {
		t.Skipf("no network connectivity, skipping: %v", err)
	}
}

func requireDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("not running on darwin, skipping")
	}
}
```

Create the (currently empty except for a placeholder) fixtures directory so later tasks have somewhere to write into:

Run: `mkdir -p acceptance/testdata`

- [ ] **Step 2: Verify the harness actually works with a trivial smoke check**

Add a temporary throwaway test to confirm the harness itself is wired correctly before building anything on top of it — this is deleted at the end of this step, not kept:

```go
func TestHarness_BuildsAndRunsRealBinary(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("basic-run.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
}
```

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run TestHarness_BuildsAndRunsRealBinary -v`
Expected: PASS — this proves `binary()` compiles the real CLI and `run()` correctly invokes it against a real example workflow for real.

Delete this temporary test now (its only purpose was proving the harness works before Task 2 builds the real, permanent corpus smoke test on top of it — keeping both would be a redundant, narrower duplicate of Task 2's own coverage).

- [ ] **Step 3: Commit**

```bash
git add acceptance/harness_test.go
git commit -m "test(acceptance): add real-binary-exec harness for end-to-end acceptance tests"
```

---

### Task 2: Corpus smoke test (Tier 1)

**Files:**
- Create: `acceptance/smoke_test.go`

**Interfaces:**
- Consumes: `binary`, `run`, `repoRoot`, `requireDocker`, `requireNetwork` (Task 1).

- [ ] **Step 1: Write the test**

Create `acceptance/smoke_test.go`:

```go
package acceptance

import (
	"path/filepath"
	"testing"
	"time"
)

// corpusExclusions lists examples/workflows/*.yml files this generic
// smoke loop must not run with default flags — each has its own reason,
// documented inline, and its own dedicated test elsewhere in this suite
// that exercises it correctly.
var corpusExclusions = map[string]string{
	"macos-job.yml": "correct exit code is host-OS-dependent (0 on a Mac, a specific error elsewhere) — see TestMacOSBackend in features_platform_test.go, not a generic 0-exit-code check",
}

// TestCorpus_EveryExampleWorkflowRunsSuccessfully globs every file under
// examples/workflows/ and runs each for real with default flags,
// asserting a clean exit. This is intentionally self-maintaining: a
// future new example file is automatically covered here with zero
// changes to this test, catching "did a change silently break any
// documented example" regressions for free as the corpus grows.
func TestCorpus_EveryExampleWorkflowRunsSuccessfully(t *testing.T) {
	requireDocker(t)
	requireNetwork(t) // several examples fetch real actions/images over the network

	matches, err := filepath.Glob(filepath.Join(repoRoot(), "examples", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("Glob() found zero example workflows — expected at least one")
	}

	for _, path := range matches {
		name := filepath.Base(path)
		if reason, excluded := corpusExclusions[name]; excluded {
			t.Logf("skipping %s: %s", name, reason)
			continue
		}
		t.Run(name, func(t *testing.T) {
			result := run(t, t.TempDir(), 90*time.Second, "run", path)
			if result.ExitCode != 0 {
				t.Errorf("ExitCode = %d, want 0\nstdout:\n%s\nstderr:\n%s", result.ExitCode, result.Stdout, result.Stderr)
			}
		})
	}
}
```

- [ ] **Step 2: Run it for real**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run TestCorpus -v -timeout 10m`
Expected: PASS for every example except the excluded `macos-job.yml`. If any real example fails, root-cause it for real (temporary logging if needed) and fix the actual bug — do not add it to `corpusExclusions` to route around a real failure; that list is only for cases where the *correct* expected result genuinely isn't a uniform `0` exit code, not an escape hatch for a broken example.

- [ ] **Step 3: Commit**

```bash
git add acceptance/smoke_test.go
git commit -m "test(acceptance): add self-maintaining corpus smoke test over examples/workflows"
```

---

### Task 3: Triggers scenario tests

**Files:**
- Create: `acceptance/features_triggers_test.go`

**Interfaces:**
- Consumes: harness (Task 1). Reuses `examples/workflows/triggers.yml` and `examples/event-payloads/custom-pull-request.json` — no new fixtures.

- [ ] **Step 1: Write the tests**

Create `acceptance/features_triggers_test.go`:

```go
package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestTriggers_DefaultResolvesToPushWhenOnHasTwoEntries(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("triggers.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "event_name=push") {
		t.Errorf("Stdout = %q, want event_name=push (triggers.yml's on: [push, pull_request] has 2 entries, so the default falls back to \"push\")", result.Stdout)
	}
	if strings.Contains(result.Stdout, "PR #") {
		t.Errorf("Stdout = %q, want the pull_request-only step to be skipped when event_name is push", result.Stdout)
	}
}

func TestTriggers_EventNameFlagSelectsSyntheticPullRequestPayload(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", "--event-name", "pull_request", examplePath("triggers.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "event_name=pull_request") {
		t.Errorf("Stdout = %q, want event_name=pull_request", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "PR #1: Local test pull request (feature-branch -> main)") {
		t.Errorf("Stdout = %q, want the synthetic default pull_request payload's exact values", result.Stdout)
	}
}

func TestTriggers_EventPathOverridesSyntheticDefault(t *testing.T) {
	requireDocker(t)
	payloadPath := repoRoot() + "/examples/event-payloads/custom-pull-request.json"
	result := run(t, t.TempDir(), 30*time.Second, "run", "--event-name", "pull_request", "--event-path", payloadPath, examplePath("triggers.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "PR #99: A real custom payload overriding the synthetic default (my-feature -> main)") {
		t.Errorf("Stdout = %q, want the real user-supplied payload's values, not the synthetic default's", result.Stdout)
	}
	if strings.Contains(result.Stdout, "PR #1: Local test pull request") {
		t.Errorf("Stdout = %q, the synthetic default's values leaked through — --event-path did not override it", result.Stdout)
	}
}
```

- [ ] **Step 2: Run tests, fix any real bug found, then commit**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run TestTriggers -v`
Expected: PASS (all 3) — this exact 3-invocation sequence was already manually verified for real during the Triggers sub-project; this task makes that verification permanent and automated rather than re-discovering it.

```bash
git add acceptance/features_triggers_test.go
git commit -m "test(acceptance): add real trigger/event-payload scenario tests"
```

---

### Task 4: Matrix scenario tests (cartesian, include/exclude, concurrency, fail-fast)

**Files:**
- Create: `acceptance/features_matrix_test.go`
- Create: `acceptance/testdata/matrix-fail-fast.yml`

**Interfaces:**
- Consumes: harness (Task 1). Reuses `matrix-build.yml`, `matrix-include-exclude.yml`, `matrix-concurrency.yml`; adds one new fixture for a real fail-fast proof (existing coverage of fail-fast's "skip" behavior is fake-backend-only in `internal/engine`, never proven against real Docker containers).

- [ ] **Step 1: Write the new fail-fast fixture**

Create `acceptance/testdata/matrix-fail-fast.yml`:

```yaml
name: matrix fail-fast (real)
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    strategy:
      max-parallel: 1
      matrix:
        n: ["1", "2", "3"]
    steps:
      - run: 'if [ "${{ matrix.n }}" = "1" ]; then exit 1; else echo "combo ${{ matrix.n }} ran"; fi'
```

- [ ] **Step 2: Write the tests**

Create `acceptance/features_matrix_test.go`:

```go
package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestMatrix_IncludeExcludeRealMergeSemantics(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("matrix-include-exclude.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	want := []string{
		"os=ubuntu-latest version=16 experimental=true",
		"os=ubuntu-latest version=18 experimental=true",
		"os=windows-latest version=18 experimental=",
		"os=macos-latest version=20 experimental=",
	}
	for _, w := range want {
		if !strings.Contains(result.Stdout, w) {
			t.Errorf("Stdout missing expected combination line %q\nfull stdout:\n%s", w, result.Stdout)
		}
	}
	if strings.Contains(result.Stdout, "os=windows-latest version=16") {
		t.Error("windows-latest/16 should have been excluded, but its output line appeared")
	}
}

func TestMatrix_RealConcurrentExecutionIsFasterThanSequential(t *testing.T) {
	requireDocker(t)
	start := time.Now()
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("matrix-concurrency.yml"))
	elapsed := time.Since(start)
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	// 3 combinations x 2s sleep each, max-parallel: 3 -> all run at once.
	// A generous threshold (well under the ~6s+ strictly-sequential total,
	// allowing real headroom for Docker container startup overhead) proves
	// genuine concurrency without being timing-brittle in CI.
	if elapsed > 5*time.Second {
		t.Errorf("elapsed = %s, want well under 5s (3x2s sequential would be 6s+) — combinations should run concurrently", elapsed)
	}
}

func TestMatrix_RealFailFastStopsUnstartedCombinations(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", testdataPath("matrix-fail-fast.yml"))
	if result.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want 1 (matrix combination n=1 fails)", result.ExitCode)
	}
	if strings.Contains(result.Stdout, "combo 2 ran") || strings.Contains(result.Stdout, "combo 3 ran") {
		t.Errorf("Stdout = %q, want combinations 2 and 3 skipped after combination 1's real failure (max-parallel: 1, fail-fast defaults true)", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "skipped") {
		t.Errorf("Stdout = %q, want at least one combination reported as skipped", result.Stdout)
	}
}
```

- [ ] **Step 3: Run tests, fix any real bug found, then commit**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run TestMatrix -v -timeout 3m`
Expected: PASS (all 3).

```bash
git add acceptance/features_matrix_test.go acceptance/testdata/matrix-fail-fast.yml
git commit -m "test(acceptance): add real matrix cartesian/include-exclude/concurrency/fail-fast scenario tests"
```

---

### Task 5: `needs:` job-graph scenario tests (linear, diamond, output propagation)

**Files:**
- Create: `acceptance/features_needs_test.go`
- Create: `acceptance/testdata/needs-diamond.yml`

**Interfaces:**
- Consumes: harness (Task 1). Reuses `job-dependencies.yml`, `output-passing.yml`; adds one new fixture for diamond dependency (fan-out then fan-in), which no existing example covers.

- [ ] **Step 1: Write the new diamond-dependency fixture**

Create `acceptance/testdata/needs-diamond.yml`:

```yaml
name: needs diamond dependency
on: push
jobs:
  a:
    runs-on: ubuntu-latest
    outputs:
      val: ${{ steps.s.outputs.val }}
    steps:
      - id: s
        run: echo "val=from-a" >> "$GITHUB_OUTPUT"
  b:
    runs-on: ubuntu-latest
    needs: a
    steps:
      - run: echo "b saw ${{ needs.a.outputs.val }}"
  c:
    runs-on: ubuntu-latest
    needs: a
    steps:
      - run: echo "c saw ${{ needs.a.outputs.val }}"
  d:
    runs-on: ubuntu-latest
    needs: [b, c]
    steps:
      - run: echo "d running after b and c"
```

- [ ] **Step 2: Write the tests**

Create `acceptance/features_needs_test.go`:

```go
package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestNeeds_LinearDependencyPassesJobOutput(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("job-dependencies.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Deploying version 1.2.3") {
		t.Errorf("Stdout = %q, want the deploy job to read build's declared output via needs.build.outputs.version", result.Stdout)
	}
}

func TestNeeds_StepOutputPassingWithinOneJob(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("output-passing.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Building version 1.2.3") {
		t.Errorf("Stdout = %q, want a later step to read an earlier step's $GITHUB_OUTPUT value", result.Stdout)
	}
}

func TestNeeds_DiamondDependencyFanOutFanIn(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", testdataPath("needs-diamond.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	for _, want := range []string{"b saw from-a", "c saw from-a", "d running after b and c"} {
		if !strings.Contains(result.Stdout, want) {
			t.Errorf("Stdout missing %q\nfull stdout:\n%s", want, result.Stdout)
		}
	}
}
```

- [ ] **Step 3: Run tests, fix any real bug found, then commit**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run TestNeeds -v -timeout 2m`
Expected: PASS (all 3).

```bash
git add acceptance/features_needs_test.go acceptance/testdata/needs-diamond.yml
git commit -m "test(acceptance): add real needs: job-graph scenario tests including diamond dependency"
```

---

### Task 6: Actions scenario tests (JS, Docker, composite, marketplace, local, `--local-repository`)

**Files:**
- Create: `acceptance/features_actions_test.go`

**Interfaces:**
- Consumes: harness (Task 1). Reuses `uses-docker-image.yml`, `uses-docker-action.yml`, `uses-composite-action.yml`, `uses-marketplace-action.yml`, `uses-local-repository.yml` — no new fixtures. These already run in Task 2's smoke test; this task adds precise content assertions the smoke test deliberately doesn't (it only checks exit codes).

- [ ] **Step 1: Write the tests**

Create `acceptance/features_actions_test.go`:

```go
package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestActions_RawDockerImageStepReadsWorkspaceFile(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("uses-docker-image.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "written by a run: step") {
		t.Errorf("Stdout = %q, want the docker://alpine step's cat output of the marker file a prior run: step wrote", result.Stdout)
	}
}

func TestActions_CompositeActionBridgesNestedJSActionOutput(t *testing.T) {
	requireDocker(t)
	requireNetwork(t) // the composite action's nested step is itself a local JS action needing the pinned Node runtime download
	result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("uses-composite-action.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Composite action starting for mirror-gha") {
		t.Errorf("Stdout = %q, want the composite's own nested run: step output", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "Got greeting: Hello, mirror-gha!") {
		t.Errorf("Stdout = %q, want the nested JS action's output bridged up through the composite's outputs: block", result.Stdout)
	}
}

func TestActions_RealMarketplaceActionFetchedAndRun(t *testing.T) {
	requireDocker(t)
	requireNetwork(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("uses-marketplace-action.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Hello mirror-gha!") {
		t.Errorf("Stdout = %q, want the real actions/hello-world-javascript-action's real greeting", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "Action ran at:") {
		t.Errorf("Stdout = %q, want the step reading the action's own time output back", result.Stdout)
	}
}

func TestActions_LocalRepositoryOverrideSkipsRealFetch(t *testing.T) {
	requireDocker(t)
	// Deliberately NOT calling requireNetwork(t) — the whole point of this
	// scenario is that mirror-gha/does-not-exist@v1 is not a real,
	// fetchable repository; --local-repository must resolve it locally
	// instead of ever attempting a network fetch.
	overrideFlag := "mirror-gha/does-not-exist@v1=" + repoRootRelative("examples/workflows/actions/hello-action")
	result := run(t, t.TempDir(), 30*time.Second, "run", "--local-repository", overrideFlag, examplePath("uses-local-repository.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Got greeting: Hello, override-works!") {
		t.Errorf("Stdout = %q, want the local override's action source used instead of a real (nonexistent) fetch", result.Stdout)
	}
}
```

Add a small helper next to the others in `acceptance/harness_test.go` (append, don't replace anything):

```go
// repoRootRelative resolves rel against repoRoot() — used where a flag
// value itself must be an absolute path (e.g. --local-repository's
// local/path side), unlike examplePath/testdataPath which resolve a
// workflow file argument directly.
func repoRootRelative(rel string) string {
	return filepath.Join(repoRoot(), rel)
}
```

- [ ] **Step 2: Run tests, fix any real bug found, then commit**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run TestActions -v -timeout 3m`
Expected: PASS (all 4).

```bash
git add acceptance/features_actions_test.go acceptance/harness_test.go
git commit -m "test(acceptance): add real action-type scenario tests with precise output assertions"
```

---

### Task 7: Cache real persistence test (two real sequential invocations)

**Files:**
- Create: `acceptance/features_cache_test.go`

**Interfaces:**
- Consumes: harness (Task 1). Reuses `uses-cache.yml` — no new fixture.

- [ ] **Step 1: Write the test**

Create `acceptance/features_cache_test.go`:

```go
package acceptance

import (
	"strings"
	"testing"
	"time"
)

// TestCache_RealSaveThenRestoreAcrossTwoInvocations is the one scenario
// in this suite that runs the real binary TWICE against the SAME
// workdir, on purpose: actions/cache@v4's real save only happens via its
// post: entry point after the job's own steps finish, and its real
// restore only succeeds if a PRIOR mirror run invocation actually saved
// something to the persistent, cross-invocation cache store mirror-gha
// deliberately maintains (unlike the artifact store, which is
// deliberately fresh per invocation) — this can only be proven by two
// genuinely separate process invocations, not by asserting anything
// within a single run.
func TestCache_RealSaveThenRestoreAcrossTwoInvocations(t *testing.T) {
	requireDocker(t)
	requireNetwork(t)

	workdir := t.TempDir()

	first := run(t, workdir, 60*time.Second, "run", examplePath("uses-cache.yml"))
	if first.ExitCode != 0 {
		t.Fatalf("first run ExitCode = %d, want 0 (stderr: %s)", first.ExitCode, first.Stderr)
	}
	if !strings.Contains(first.Stdout, "cache-hit output: []") {
		t.Errorf("first run Stdout = %q, want cache-hit output: [] (a cold cache — this exact key has never been saved before)", first.Stdout)
	}

	second := run(t, workdir, 60*time.Second, "run", examplePath("uses-cache.yml"))
	if second.ExitCode != 0 {
		t.Fatalf("second run ExitCode = %d, want 0 (stderr: %s)", second.ExitCode, second.Stderr)
	}
	if !strings.Contains(second.Stdout, "cache-hit output: [true]") {
		t.Errorf("second run Stdout = %q, want cache-hit output: [true] — the first run's real post: save should make this a real restore hit, proving actual cross-invocation persistence, not simulated", second.Stdout)
	}
}
```

- [ ] **Step 2: Run the test, fix any real bug found, then commit**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run TestCache_RealSaveThenRestore -v -timeout 2m`
Expected: PASS. If the cache store already has a stale entry under `mirror-gha-demo-cache-v1` from prior manual testing on this machine, the FIRST run's assertion (`cache-hit output: []`) could fail — if so, clear the real persistent cache store first (`rm -rf` whatever `cacheserver.StoreRoot()` resolves to — check `internal/cacheserver`'s `StoreRoot()` for the exact path, likely under `~/Library/Caches/mirror-gha/` on this machine) and re-run, rather than weakening the assertion.

```bash
git add acceptance/features_cache_test.go
git commit -m "test(acceptance): add real cache save-then-restore test across two invocations"
```

---

### Task 8: Artifacts real round-trip test (v3 and v4)

**Files:**
- Create: `acceptance/features_artifacts_test.go`

**Interfaces:**
- Consumes: harness (Task 1). Reuses `uses-artifact-v3.yml`, `uses-artifact-v4.yml` — no new fixtures.

- [ ] **Step 1: Write the tests**

Create `acceptance/features_artifacts_test.go`:

```go
package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestArtifacts_V4RealUploadThenDownloadAcrossJobs(t *testing.T) {
	requireDocker(t)
	requireNetwork(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("uses-artifact-v4.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "hello from build (v4)") {
		t.Errorf("Stdout = %q, want the uploaded file's real content to round-trip through the download job", result.Stdout)
	}
}

func TestArtifacts_V3RealUploadThenDownloadAcrossJobs(t *testing.T) {
	requireDocker(t)
	requireNetwork(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("uses-artifact-v3.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	// uses-artifact-v3.yml follows the same "build job uploads, deploy job
	// downloads and cats" pattern as its v4 sibling — check its own file
	// first if this exact substring assertion needs adjusting to match
	// its real content string.
	if !strings.Contains(result.Stdout, "hello from build") {
		t.Errorf("Stdout = %q, want the uploaded file's real content to round-trip through the download job via the legacy v3 protocol", result.Stdout)
	}
}
```

- [ ] **Step 2: Verify the v3 fixture's exact content string, adjust the assertion if needed, then run**

Run: `cat examples/workflows/uses-artifact-v3.yml` — confirm the exact string the build job writes (likely `"hello from build (v3)"` or similar, matching the v4 sibling's own `"hello from build (v4)"` pattern) and use that exact string in Step 1's second test if it differs from the generic `"hello from build"` substring already written there.

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run TestArtifacts -v -timeout 2m`
Expected: PASS (both).

- [ ] **Step 3: Commit**

```bash
git add acceptance/features_artifacts_test.go
git commit -m "test(acceptance): add real artifact v3/v4 upload-download round-trip tests"
```

---

### Task 9: Cross-cutting scenario tests — workflow commands, `GITHUB_TOKEN`/`secrets`/`permissions`/`environment:`

**Files:**
- Create: `acceptance/features_commands_and_token_test.go`
- Create: `acceptance/testdata/workflow-commands.yml`
- Create: `acceptance/testdata/token-permissions-environment.yml`

**Interfaces:**
- Consumes: harness (Task 1). Both fixtures are new — no existing example covers either combination (confirmed: none of the 22 examples exercises `::group::`/`::error::`/`::add-mask::`/`::debug::`, and none combines `GITHUB_TOKEN`/`secrets`/`permissions:`/`environment:` together).

- [ ] **Step 1: Write the new fixtures**

Create `acceptance/testdata/workflow-commands.yml` (this exact content was already manually verified for real during the workflow-commands sub-project this session — this task makes that verification permanent):

```yaml
name: workflow commands check
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: |
          echo "::group::setup"
          echo "::add-mask::topsecret123"
          echo "the token is topsecret123"
          echo "::endgroup::"
          echo "::error::deliberate error annotation"
          echo "::warning::deliberate warning annotation"
          echo "::notice::deliberate notice annotation"
          echo "::debug::hidden by default"
      - run: echo "second step also mentions topsecret123"
```

Create `acceptance/testdata/token-permissions-environment.yml`:

```yaml
name: token permissions environment
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    environment: production
    permissions:
      contents: read
    steps:
      - run: 'echo "env=$GITHUB_TOKEN expr=${{ secrets.GITHUB_TOKEN }} environment=$GITHUB_ENVIRONMENT"'
```

- [ ] **Step 2: Write the tests**

Create `acceptance/features_commands_and_token_test.go`:

```go
package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestWorkflowCommands_RenderedMaskedAndDebugHiddenByDefault(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", testdataPath("workflow-commands.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (an ::error:: annotation must not fail the step by itself; stderr: %s)", result.ExitCode, result.Stderr)
	}
	for _, want := range []string{
		"▶ setup",
		"the token is ***",
		"❌ deliberate error annotation",
		"⚠️  deliberate warning annotation",
		"ℹ️  deliberate notice annotation",
		"second step also mentions ***",
	} {
		if !strings.Contains(result.Stdout, want) {
			t.Errorf("Stdout missing %q\nfull stdout:\n%s", want, result.Stdout)
		}
	}
	if strings.Contains(result.Stdout, "topsecret123") {
		t.Error("Stdout contains the unmasked secret value — ::add-mask:: redaction failed")
	}
	if strings.Contains(result.Stdout, "hidden by default") {
		t.Error("Stdout contains the ::debug:: line without --debug — it should be hidden by default")
	}
}

func TestWorkflowCommands_DebugShownWithFlag(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", "--debug", testdataPath("workflow-commands.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "🐛 hidden by default") {
		t.Errorf("Stdout = %q, want the ::debug:: line rendered when --debug is set", result.Stdout)
	}
}

func TestGitHubToken_SecretsContextAndEnvironmentName(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", testdataPath("token-permissions-environment.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 — permissions: must be a no-op shim, not something that breaks the run (stderr: %s)", result.ExitCode, result.Stderr)
	}
	want := "env=ghs_mirror_gha_local_placeholder_token expr=ghs_mirror_gha_local_placeholder_token environment=production"
	if !strings.Contains(result.Stdout, want) {
		t.Errorf("Stdout = %q, want %q", result.Stdout, want)
	}
}
```

- [ ] **Step 3: Run tests, fix any real bug found, then commit**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run "TestWorkflowCommands|TestGitHubToken" -v -timeout 2m`
Expected: PASS (all 3).

```bash
git add acceptance/features_commands_and_token_test.go acceptance/testdata/workflow-commands.yml acceptance/testdata/token-permissions-environment.yml
git commit -m "test(acceptance): add real workflow-command and GITHUB_TOKEN/secrets/permissions/environment scenario tests"
```

---

### Task 10: Remaining job/step features — `container:`/`services:`, macOS backend, `timeout-minutes`, `continue-on-error`, `defaults.run`

**Files:**
- Create: `acceptance/features_platform_test.go`
- Create: `acceptance/testdata/timeout-minutes.yml`
- Create: `acceptance/testdata/defaults-precedence.yml`

**Interfaces:**
- Consumes: harness (Task 1). Reuses `services-container.yml`, `macos-job.yml`, `continue-on-error.yml`; adds two new fixtures (`timeout-minutes` and `defaults.run` precedence have never been verified against real Docker execution anywhere in this project — both were previously tested only against fake backends in `internal/engine`).

- [ ] **Step 1: Write the new fixtures**

Create `acceptance/testdata/timeout-minutes.yml` — note the fractional `timeout-minutes` value (`0.02`, ~1.2 real seconds) is not valid real GitHub Actions YAML (GitHub requires a whole-minute integer); it's used here deliberately, since mirror-gha's own parser (a `float64` field, not validated as a whole number) accepts it, to keep this test's real wall-clock runtime short rather than waiting a genuine full minute in CI for every run of this suite:

```yaml
name: timeout minutes (real)
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 0.02
    steps:
      - run: sleep 30 && echo "should never print"
```

Create `acceptance/testdata/defaults-precedence.yml`:

```yaml
name: defaults precedence (real)
on: push
defaults:
  run:
    shell: sh
jobs:
  build:
    runs-on: ubuntu-latest
    defaults:
      run:
        shell: bash
    steps:
      - run: 'echo "bash_version=$BASH_VERSION"'
```

- [ ] **Step 2: Write the tests**

Create `acceptance/features_platform_test.go`:

```go
package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestContainerAndServices_RealNetworkReachabilityAndImageSwap(t *testing.T) {
	requireDocker(t)
	requireNetwork(t)
	result := run(t, t.TempDir(), 120*time.Second, "run", examplePath("services-container.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "v20.") {
		t.Errorf("Stdout = %q, want a real Node 20.x version string (proving container: node:20 actually swapped the image)", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "accepting connections") {
		t.Errorf("Stdout = %q, want pg_isready's real success message (proving the postgres service is reachable by hostname)", result.Stdout)
	}
}

// TestMacOSBackend_HostOSDependentBehavior is the one test in this suite
// with a genuinely different expected outcome depending on the machine
// running it — matching macos-job.yml's own real, documented behavior:
// it only succeeds when mirror-gha itself is running on a Mac, and fails
// with a clear, specific error everywhere else. Both branches are real
// assertions, not a skip in one direction.
func TestMacOSBackend_HostOSDependentBehavior(t *testing.T) {
	if isDarwinHost() {
		requireNetwork(t) // the example's second step is a real JS action needing the pinned darwin Node download
		result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("macos-job.yml"))
		if result.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0 on a real Mac host (stderr: %s)", result.ExitCode, result.Stderr)
		}
		if !strings.Contains(result.Stdout, "Darwin") {
			t.Errorf("Stdout = %q, want real uname -s output showing Darwin (proving no container was involved)", result.Stdout)
		}
		if !strings.Contains(result.Stdout, "Hello mirror-gha!") {
			t.Errorf("Stdout = %q, want the real JS action's greeting, run natively", result.Stdout)
		}
		return
	}
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("macos-job.yml"))
	if result.ExitCode == 0 {
		t.Fatal("ExitCode = 0, want a non-zero exit on a non-Mac host — macos-latest requires running mirror-gha on a Mac")
	}
	if !strings.Contains(result.Stderr, "requires running mirror-gha on a Mac host") {
		t.Errorf("Stderr = %q, want the specific cross-host macOS error message", result.Stderr)
	}
}

func isDarwinHost() bool {
	return runtimeGOOS() == "darwin"
}

func TestTimeoutMinutes_RealEnforcementKillsLongRunningStep(t *testing.T) {
	requireDocker(t)
	start := time.Now()
	result := run(t, t.TempDir(), 30*time.Second, "run", testdataPath("timeout-minutes.yml"))
	elapsed := time.Since(start)
	if result.ExitCode == 0 {
		t.Fatal("ExitCode = 0, want a non-zero exit — the step's 30s sleep should never complete within a ~1.2s timeout-minutes")
	}
	if elapsed > 15*time.Second {
		t.Errorf("elapsed = %s, want well under 15s — a real timeout must actually stop the running step, not merely give up waiting on it after the fact", elapsed)
	}
	if strings.Contains(result.Stdout, "should never print") {
		t.Error("Stdout contains \"should never print\" — the timed-out step's command completed anyway, meaning the real subprocess was never actually killed")
	}
}

func TestDefaultsRunShell_JobLevelOverridesWorkflowLevel(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", testdataPath("defaults-precedence.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if strings.Contains(result.Stdout, "bash_version=\n") || !strings.Contains(result.Stdout, "bash_version=") {
		t.Errorf("Stdout = %q, want a non-empty BASH_VERSION (job-level defaults.run.shell: bash must override the workflow-level shell: sh)", result.Stdout)
	}
}

func TestContinueOnError_RealFailingStepDoesNotStopJob(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("continue-on-error.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "We got here even though the previous step failed") {
		t.Errorf("Stdout = %q, want the step after the continue-on-error: true failure to have actually run", result.Stdout)
	}
}
```

Add `runtimeGOOS` to `acceptance/harness_test.go` (a one-line indirection purely so this file doesn't need its own `"runtime"` import just for one call — append, don't replace anything):

```go
func runtimeGOOS() string { return runtime.GOOS }
```

- [ ] **Step 2: Run tests — the timeout-minutes test is the one most likely to surface a real, previously-unverified bug**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run "TestContainerAndServices|TestMacOSBackend|TestTimeoutMinutes|TestDefaultsRunShell|TestContinueOnError" -v -timeout 5m`

Expected: PASS (all 5) — but if `TestTimeoutMinutes_RealEnforcementKillsLongRunningStep` fails (either the exit code stays 0, or `elapsed` is close to the full 30s, or `"should never print"` shows up), this proves a real, previously-undiscovered bug: `context.WithTimeout` cancelling `exec.CommandContext(ctx, "docker", "exec", ...)`'s Go-side context kills the `docker exec` **client** process, but that does NOT automatically stop the **container-side** process it was attached to (a real, well-known Docker behavior gap — `docker exec` has no built-in "kill the exec'd process when the client disconnects" semantics without `--detach`-adjacent handling). If this is what's found: the real fix belongs in `internal/runner/docker_backend.go`'s `Exec` — on context cancellation, actually stop the container-side process (e.g., capture the `docker exec`'s own PID inside the container via a wrapper, or use `docker exec <id> kill <pid>`, or simplest: also `docker stop`/restart the affected step's container-level construct if the architecture allows it) — root-cause and fix this for real, with its own regression test in `internal/runner`, rather than weakening this acceptance assertion. This is exactly the kind of gap this whole suite exists to find.

- [ ] **Step 3: Commit**

```bash
git add acceptance/features_platform_test.go acceptance/testdata/timeout-minutes.yml acceptance/testdata/defaults-precedence.yml acceptance/harness_test.go
git commit -m "test(acceptance): add real container/services, macOS backend, timeout-minutes, defaults, continue-on-error scenario tests"
```

(If Step 2 found and required a real bug fix in `internal/runner`, that fix — plus its own unit-level regression test in `internal/runner/docker_backend_test.go` — should already be committed as its own separate commit before this one, matching this session's established discipline for every prior sub-project's real bugs found during verification.)

---

### Task 11: Known-gap contracts, Makefile convenience target, docs

**Files:**
- Create: `acceptance/features_known_gaps_test.go`
- Create: `acceptance/testdata/windows-unsupported.yml`
- Create: `acceptance/testdata/branches-filter.yml`
- Modify: `Makefile`
- Modify: `docs/usage.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Write the new fixtures**

Create `acceptance/testdata/windows-unsupported.yml`:

```yaml
name: windows unsupported
on: push
jobs:
  build:
    runs-on: windows-latest
    steps:
      - run: echo hi
```

Create `acceptance/testdata/branches-filter.yml`:

```yaml
name: branches filter behavior
on:
  push:
    branches: [main]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo "ran despite an on: push: branches: filter — mirror-gha doesn't evaluate these, matching act's own choice"
```

- [ ] **Step 2: Write the tests**

Create `acceptance/features_known_gaps_test.go`:

```go
package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestKnownGap_WindowsRunnerReturnsClearError(t *testing.T) {
	result := run(t, t.TempDir(), 10*time.Second, "run", testdataPath("windows-unsupported.yml"))
	if result.ExitCode == 0 {
		t.Fatal("ExitCode = 0, want a non-zero exit — windows-latest has no backend")
	}
	if !strings.Contains(result.Stderr, "windows-latest") || !strings.Contains(result.Stderr, "not supported yet") {
		t.Errorf("Stderr = %q, want a clear error naming windows-latest as unsupported, not a silent wrong result", result.Stderr)
	}
}

func TestKnownGap_BranchesFilterIsAcceptedButNotEnforced(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", testdataPath("branches-filter.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 — on: push: branches: must parse without error even though it isn't enforced (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "ran despite an on: push: branches: filter") {
		t.Errorf("Stdout = %q, want the job to have run — documenting today's real, accepted behavior (branches:/paths:/types: sub-filters are parsed but not evaluated, matching act's own choice) as an explicit, enforced contract rather than an undocumented accident", result.Stdout)
	}
}
```

- [ ] **Step 3: Run tests**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./acceptance/... -run TestKnownGap -v`
Expected: PASS (both).

- [ ] **Step 4: Add a Makefile convenience target**

In `Makefile`, add a target for running just this suite quickly during development (it already runs as part of the existing `test` target's `go test ./...`, so this is a convenience addition, not new CI wiring):

```makefile
test-acceptance:
	go test ./acceptance/... -v
```

- [ ] **Step 5: Run the full project test suite one final time**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l . | grep -v third_party; go vet ./... && go test ./... -race -timeout 15m 2>&1 | tail -40`
Expected: clean build, no unformatted files, no vet errors, every package (including the new `acceptance` package) `ok`.

- [ ] **Step 6: Update docs/usage.md**

Add a new section after "What's not supported yet" (or wherever fits best stylistically — check the file's current structure first):

```markdown
## Testing this project itself

`acceptance/` is a real, CI-enforced end-to-end regression suite — every
test shells out to the actual compiled `mirror` binary (never calling
internal Go functions directly) and asserts on real output from real
Docker containers, real network fetches, and real cache/artifact
servers. It covers every feature in "What's supported today" above,
runs automatically as part of `go test ./...` (and therefore in CI on
every push/PR), and is intentionally self-maintaining for the corpus
smoke test tier: a new file added to `examples/workflows/` is
automatically covered with no test-file changes needed. Run it alone
during development with `make test-acceptance`.
```

- [ ] **Step 7: Update CHANGELOG.md**

Add to the `### Added` section, above the most recent entry:

```markdown
- **Real end-to-end acceptance test suite (`acceptance/`).** Closes a
  confirmed, complete gap: every one of the ~20 files under
  `examples/workflows/` had only ever been run manually all session —
  zero automated coverage existed. Every test shells out to the real,
  compiled `mirror` binary via `os/exec` (never calling internal Go
  functions directly) — a deliberate choice over extending
  `cmd/mirror/main_test.go`'s existing in-process pattern, both for
  fidelity (exercises real CLI flag parsing and the real process
  boundary) and safety (each subprocess gets its own real stdout/stderr,
  no fragile global-file-descriptor swapping). Three tiers: a
  self-maintaining corpus smoke test (globs `examples/workflows/*.yml`
  automatically — a future new example is covered with zero test
  changes), detailed feature-scenario tests with precise output
  assertions across triggers, matrix (cartesian/include-exclude/real
  concurrency/fail-fast), `needs:` job graphs (including diamond
  dependency), every action type, cache (two real sequential
  invocations proving genuine cross-run persistence), artifacts v3/v4,
  every workflow command, `GITHUB_TOKEN`/`secrets`/`permissions`/
  `environment:`, `container:`/`services:`, the macOS backend,
  `timeout-minutes`, `defaults.run`, and `continue-on-error`, and
  known-gap contracts (Windows returns a clear error; `branches:`/
  `paths:` filters are accepted but not enforced, documented as an
  explicit checked contract rather than a silent accident). Folds into
  the existing `make test`/CI `go test ./...` sweep — no new CI job
  needed. Found and fixed a real, small stale-documentation bug along
  the way: `ErrUnsupportedRunner`'s error message still said "only
  ubuntu-latest/ubuntu-22.04/ubuntu-24.04 run today," inaccurate since
  the macOS host backend shipped after that message was originally
  written.
```

(If Task 10's timeout-minutes test found and required a real fix in `internal/runner`, add a sentence here summarizing that fix too, matching this session's established CHANGELOG discipline of documenting every real bug found during verification — write this after Task 10 actually runs, not before.)

- [ ] **Step 8: Commit**

```bash
git add acceptance/features_known_gaps_test.go acceptance/testdata/windows-unsupported.yml acceptance/testdata/branches-filter.yml Makefile docs/usage.md CHANGELOG.md
git commit -m "test(acceptance): add known-gap contract tests; wire make test-acceptance; document the suite"
```
