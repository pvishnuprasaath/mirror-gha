# macOS Host Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Support `runs-on: macos-latest`/`macos-13`/`macos-14`/`macos-15` by executing jobs directly on the host process (no container) when mirror-gha itself runs on a Mac.

**Architecture:** A new `runner.HostBackend`/`hostJob` implement the existing `Backend`/`Job` interfaces using real `os/exec` instead of `docker`. The one real bridging problem — the engine layer's `/mirror-node`/`/mirror-actions/<id>` synthetic path convention, meaningful today only inside a Docker container's isolated filesystem — gets solved by rewriting any such path under a real per-job temp root before executing. `Job` gains a `Platform()` accessor so the Node-runtime downloader and the Docker-action dispatch path can each make backend-aware decisions without the engine layer needing to know which concrete backend is running.

**Tech Stack:** Go 1.27 stdlib only — `os/exec` for both host process execution and `cp -R` (macOS ships BSD `cp`, which supports the same trailing-`/.`-copies-contents idiom already used for `docker cp` elsewhere in this codebase).

**Spec:** `docs/design/specs/2026-09-14-mirror-gha-design.md`, "macOS Host Backend" section.

## Global Constraints

- Zero external Go dependencies.
- `HostBackend` only activates when `runtime.GOOS == "darwin"` — any other host OS gets a clear, specific error, never a silent wrong attempt.
- `container:`, `services:`, and `uses: docker://...`/Docker-action steps all hard-error on macOS jobs — matching real GitHub Actions' own documented Linux-only constraint for these features, not a mirror-gha-specific limitation.
- Every existing test must keep passing; the `Job` interface's new `Platform()` method must be added to every implementer in the same task that introduces it (never leave the tree non-compiling between tasks) — implementers found in this codebase: `dockerJob` (`internal/runner/docker_backend.go`), `dryRunJob` (`internal/runner/dryrun.go`), `fakeJob` (`internal/engine/executor_test.go`), `alwaysSucceedJob`/`alwaysFailJob` (`internal/engine/workflow_run_test.go`).

---

### Task 1: `Job.Platform()` and `EnsureNode`'s platform-aware download

**Files:**
- Modify: `internal/runner/backend.go`
- Modify: `internal/runner/docker_backend.go`
- Modify: `internal/runner/dryrun.go`
- Modify: `internal/engine/executor_test.go`
- Modify: `internal/engine/workflow_run_test.go`
- Modify: `internal/actions/node.go`
- Modify: `internal/actions/node_test.go`
- Modify: `internal/engine/uses_step.go`

**Interfaces:**
- Produces: `Job.Platform() string` (every implementer returns `"linux"` except the new `hostJob` in Task 2, which will return `"darwin"`). `func EnsureNode(cacheRoot, platform string) (string, error)` (breaking signature change — both call sites in this task's scope).

- [ ] **Step 1: Add `Platform()` to the `Job` interface**

In `internal/runner/backend.go`, add to the `Job` interface (after `Stop`):

```go
	// Platform reports the OS the job's steps actually execute under —
	// "linux" for the Docker backend (regardless of what OS mirror-gha
	// itself runs on) or "darwin" for the macOS host backend. Used by the
	// engine layer to make backend-aware decisions (which Node runtime to
	// download, whether a Docker action step is even possible) without
	// needing to know which concrete backend is running.
	Platform() string
```

- [ ] **Step 2: Implement `Platform()` on every existing `Job` implementer**

In `internal/runner/docker_backend.go`, add after `dockerJob.Stop`:

```go
// Platform always reports "linux" — the Docker backend's job container is
// always a Linux image, regardless of what OS mirror-gha's own process is
// running on (this project is routinely developed and tested on a Mac
// host that runs Linux job containers via Docker Desktop).
func (j *dockerJob) Platform() string { return "linux" }
```

In `internal/runner/dryrun.go`, add after `dryRunJob.Stop`:

```go
// Platform reports "linux" — dry-run mode never actually downloads or
// executes anything, so this value is never load-bearing, but it must
// match some real backend's convention rather than an invented one.
func (j *dryRunJob) Platform() string { return "linux" }
```

In `internal/engine/executor_test.go`, add a `platform string` field to `fakeJob` and a `Platform()` method:

```go
type fakeJob struct {
	results      []runner.StepResult
	calls        int
	dir          string
	workspaceDir string
	execSpecs    []runner.StepSpec
	dockerSpecs  []runner.DockerActionSpec
	platform     string // Platform() returns "linux" if this is empty
}
```

```go
func (j *fakeJob) Platform() string {
	if j.platform == "" {
		return "linux"
	}
	return j.platform
}
```

In `internal/engine/workflow_run_test.go`, add to both `alwaysSucceedJob` and `alwaysFailJob`:

```go
func (j *alwaysSucceedJob) Platform() string { return "linux" }
```

```go
func (j *alwaysFailJob) Platform() string { return "linux" }
```

- [ ] **Step 3: Build to confirm the interface change compiles everywhere**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && go vet ./...`
Expected: clean — this step only added methods, no signatures changed yet.

- [ ] **Step 4: Write the failing test for `EnsureNode`'s new signature**

Replace `internal/actions/node_test.go`'s `TestEnsureNode_DownloadsAndCaches` and add a darwin-platform test and an invalid-platform test:

```go
func TestEnsureNode_DownloadsAndCaches(t *testing.T) {
	requireNetwork(t)

	cacheRoot := t.TempDir()
	dir, err := EnsureNode(cacheRoot, "linux")
	if err != nil {
		t.Fatalf("EnsureNode() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "node")); err != nil {
		t.Errorf("expected bin/node in %s: %v", dir, err)
	}

	dir2, err := EnsureNode(cacheRoot, "linux")
	if err != nil {
		t.Fatalf("EnsureNode() second call error = %v", err)
	}
	if dir2 != dir {
		t.Errorf("second EnsureNode() = %q, want same path %q", dir2, dir)
	}
}

func TestEnsureNode_DarwinPlatform(t *testing.T) {
	requireNetwork(t)

	cacheRoot := t.TempDir()
	dir, err := EnsureNode(cacheRoot, "darwin")
	if err != nil {
		t.Fatalf("EnsureNode() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "node")); err != nil {
		t.Errorf("expected bin/node in %s: %v", dir, err)
	}
}

func TestEnsureNode_InvalidPlatform(t *testing.T) {
	_, err := EnsureNode(t.TempDir(), "windows")
	if err == nil {
		t.Fatal("EnsureNode() error = nil, want error for an unsupported platform")
	}
}
```

- [ ] **Step 5: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/actions/... -run TestEnsureNode -v`
Expected: FAIL — `EnsureNode` still takes one argument, not two.

- [ ] **Step 6: Update `EnsureNode` to take and validate a platform parameter**

Replace `internal/actions/node.go`'s `EnsureNode` function and add a small validator:

```go
// EnsureNode downloads and caches the pinned Node build at
// cacheRoot/node/<version>/<platform>/<arch>/, returning that directory.
// platform must be "linux" (Docker backend — the container's own OS,
// independent of whatever OS mirror-gha's own process runs on) or
// "darwin" (macOS host backend — mirror-gha's own process OS, since
// there's no container to target a different one). Assumes the target
// architecture matches the host's (true for default Docker Desktop
// behavior on Linux jobs, and trivially true for host-mode darwin jobs
// since there's no cross-arch concept there at all) — a documented,
// not-yet-configurable assumption.
func EnsureNode(cacheRoot, platform string) (string, error) {
	if platform != "linux" && platform != "darwin" {
		return "", fmt.Errorf("unsupported Node runtime platform: %q (want \"linux\" or \"darwin\")", platform)
	}

	arch, err := nodeArch()
	if err != nil {
		return "", err
	}

	dest := filepath.Join(cacheRoot, "node", PinnedNodeVersion, platform, arch)
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

	url := fmt.Sprintf("https://nodejs.org/dist/v%s/node-v%s-%s-%s.tar.gz", PinnedNodeVersion, PinnedNodeVersion, platform, arch)
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

- [ ] **Step 7: Update `uses_step.go`'s call site**

In `internal/engine/uses_step.go`, change:

```go
			nodeDir, err := actions.EnsureNode(cacheRoot)
```

to:

```go
			nodeDir, err := actions.EnsureNode(cacheRoot, p.RunnerJob.Platform())
```

- [ ] **Step 8: Run the new tests**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/actions/... -run TestEnsureNode -v`
Expected: PASS (all 3 — the two network tests actually download both a linux and a darwin Node tarball; the invalid-platform test needs no network).

- [ ] **Step 9: Add the Docker-action-requires-Linux check**

Real GitHub Actions only supports Docker container actions on Linux runners — `HostBackend`'s jobs (Task 2) will report `Platform() == "darwin"`, so this check, added now, makes the constraint enforced as soon as `Platform()` exists, ahead of `HostBackend` itself existing.

In `internal/engine/uses_step.go`, in the `ref.Docker` branch near the top of `prepareUsesStep` (the raw `docker://image:tag` case), add the check as the first line inside `if ref.Docker {`:

```go
	if ref.Docker {
		if p.RunnerJob.Platform() != "linux" {
			return usesStepPlan{}, fmt.Errorf("docker actions require a Linux job (this job is running on %s) — real GitHub Actions only supports Docker container actions on Linux runners", p.RunnerJob.Platform())
		}
		spec := &runner.DockerActionSpec{Image: ref.DockerImage}
```

And in the `case metadata.Runs.Using == "docker":` branch further down, add the same check as its first line:

```go
	case metadata.Runs.Using == "docker":
		if p.RunnerJob.Platform() != "linux" {
			return usesStepPlan{}, fmt.Errorf("docker actions require a Linux job (this job is running on %s) — real GitHub Actions only supports Docker container actions on Linux runners", p.RunnerJob.Platform())
		}
		image, err := actions.ResolveDockerImage(ctx, hostSourceDir, step.Uses, metadata.Runs)
```

- [ ] **Step 10: Write a failing test for the Docker-action-requires-Linux check**

Add to `internal/engine/uses_step_test.go` (read the file first to match its existing helper/fixture style, then add):

```go
func TestPrepareUsesStep_DockerActionRejectedOnNonLinuxJob(t *testing.T) {
	p := runStepParams{
		RunnerJob: &fakeJob{platform: "darwin"},
		NodeReady: new(bool),
	}
	actx := NewContext(&Workflow{}, &Job{})
	_, err := prepareUsesStep(context.Background(), p, "step", Step{Uses: "docker://alpine:3.19"}, actx)
	if err == nil {
		t.Fatal("prepareUsesStep() error = nil, want error rejecting a docker:// step on a non-Linux job")
	}
}
```

(If `uses_step_test.go` already imports `context`/uses a different construction pattern for `runStepParams`/`Context`, match its existing style instead of the above — the assertion that matters is: a `darwin`-platform job + a `docker://` step produces a non-nil error.)

- [ ] **Step 11: Run test to verify it fails, then verify it passes**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestPrepareUsesStep_DockerActionRejectedOnNonLinuxJob -v`
Expected: PASS immediately, since Step 9 already implemented the check — this test locks in that behavior rather than driving new implementation (the check needed to land before `fakeJob.platform` existed to test it against, which Step 2 already added).

- [ ] **Step 12: Run the full test suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l . | grep -v third_party; go vet ./... && go test ./... 2>&1 | tail -20`
Expected: clean build, no unformatted files, no vet errors, every package `ok`.

- [ ] **Step 13: Commit**

```bash
git add internal/runner/backend.go internal/runner/docker_backend.go internal/runner/dryrun.go internal/engine/executor_test.go internal/engine/workflow_run_test.go internal/actions/node.go internal/actions/node_test.go internal/engine/uses_step.go internal/engine/uses_step_test.go
git commit -m "feat: add Job.Platform(), platform-aware Node download, Linux-only Docker actions"
```

---

### Task 2: `runner.HostBackend`/`hostJob` — real `os/exec` execution, `/mirror-*` path translation

**Files:**
- Create: `internal/runner/host_backend.go`
- Test: `internal/runner/host_backend_test.go`

**Interfaces:**
- Consumes: `Job`/`Backend` interfaces (Task 1's `Platform()` included).
- Produces: `type HostBackend struct{}`, `func NewHostBackend() *HostBackend`, `hostJob` (implements `Job`, `Platform() string { return "darwin" }`). `func translateMirrorPath(root, p string) string` — internal helper, also directly unit-tested.

- [ ] **Step 1: Write a `requireDarwin` test helper and the failing tests**

Create `internal/runner/host_backend_test.go`:

```go
package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func requireDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("not running on darwin, skipping host backend test")
	}
}

func TestTranslateMirrorPath_RewritesMirrorPrefixedPaths(t *testing.T) {
	got := translateMirrorPath("/tmp/job-root", "/mirror-node")
	want := filepath.Join("/tmp/job-root", "/mirror-node")
	if got != want {
		t.Errorf("translateMirrorPath() = %q, want %q", got, want)
	}
}

func TestTranslateMirrorPath_LeavesOtherPathsAlone(t *testing.T) {
	got := translateMirrorPath("/tmp/job-root", "/usr/bin/node")
	if got != "/usr/bin/node" {
		t.Errorf("translateMirrorPath() = %q, want unchanged /usr/bin/node", got)
	}
}

func TestHostBackend_StartJob_RejectsContainerSpec(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	_, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), &ContainerSpec{Image: "node:20"}, nil)
	if err == nil {
		t.Fatal("StartJob() error = nil, want error rejecting container: on a macOS job")
	}
}

func TestHostBackend_StartJob_RejectsServices(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	_, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, map[string]ContainerSpec{
		"redis": {Image: "redis:7"},
	})
	if err == nil {
		t.Fatal("StartJob() error = nil, want error rejecting services: on a macOS job")
	}
}

func TestHostBackend_StartJob_PlatformIsDarwin(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	if job.Platform() != "darwin" {
		t.Errorf("Platform() = %q, want darwin", job.Platform())
	}
}

func TestHostBackend_Exec_RunsRealShellCommand(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	workspaceDir := t.TempDir()
	job, err := backend.StartJob(context.Background(), "test-job", workspaceDir, nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	filesDir, err := os.MkdirTemp(job.FilesRoot(), "step-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}

	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "echo hello from host",
		Env:      map[string]string{},
		FilesDir: filesDir,
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "hello from host") {
		t.Errorf("Stdout = %q, want it to contain %q", result.Stdout, "hello from host")
	}
}

func TestHostBackend_Exec_DefaultsToRealBash(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	filesDir, err := os.MkdirTemp(job.FilesRoot(), "step-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}

	// BASH_VERSION is only set inside an actual bash process — this
	// fails under sh, proving the no-shell-specified default is bash,
	// not sh (the Docker backend's own default).
	result, err := job.Exec(context.Background(), StepSpec{
		Command:  `echo "version=$BASH_VERSION"`,
		Env:      map[string]string{},
		FilesDir: filesDir,
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if strings.Contains(result.Stdout, "version=\n") || !strings.Contains(result.Stdout, "version=") {
		t.Errorf("Stdout = %q, want a non-empty BASH_VERSION (proving the default shell is bash)", result.Stdout)
	}
}

func TestHostBackend_CopyToContainer_RewritesMirrorPath(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "marker.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := job.CopyToContainer(context.Background(), srcDir, "/mirror-node"); err != nil {
		t.Fatalf("CopyToContainer() error = %v", err)
	}

	hj := job.(*hostJob)
	copied := filepath.Join(hj.root, "/mirror-node", "marker.txt")
	data, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("expected copied file at %s: %v", copied, err)
	}
	if string(data) != "hi" {
		t.Errorf("copied content = %q, want %q", data, "hi")
	}
}

func TestHostBackend_Exec_TranslatesMirrorPrefixedArgs(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	// Stage a tiny "action" so Args[0] can be a real, executable file
	// under the translated /mirror-node path, proving Exec rewrites
	// Args entries the same way CopyToContainer rewrites its destination.
	srcDir := t.TempDir()
	scriptPath := filepath.Join(srcDir, "bin")
	if err := os.MkdirAll(scriptPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptPath, "node"), []byte("#!/bin/sh\necho ran-fake-node\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := job.CopyToContainer(context.Background(), srcDir, "/mirror-node"); err != nil {
		t.Fatalf("CopyToContainer() error = %v", err)
	}

	filesDir, err := os.MkdirTemp(job.FilesRoot(), "step-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	result, err := job.Exec(context.Background(), StepSpec{
		Args:     []string{"/mirror-node/bin/node"},
		Env:      map[string]string{},
		FilesDir: filesDir,
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "ran-fake-node") {
		t.Errorf("Stdout = %q, want it to contain %q (proving /mirror-node/bin/node resolved to the real translated path)", result.Stdout, "ran-fake-node")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/runner/... -run "TestTranslateMirrorPath|TestHostBackend" -v`
Expected: FAIL — `translateMirrorPath`, `NewHostBackend`, `hostJob` all undefined.

- [ ] **Step 3: Implement `HostBackend`/`hostJob`**

Create `internal/runner/host_backend.go`:

```go
package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HostBackend runs a job directly on the host process, with no container
// at all — the only architecturally honest option for macOS, which
// cannot be virtualized or containerized on non-Apple hardware. Callers
// (runner.SelectBackend) are responsible for only constructing this when
// runtime.GOOS == "darwin"; HostBackend itself doesn't re-check that,
// matching the precedent that backend selection lives in SelectBackend,
// not in each Backend implementation.
type HostBackend struct{}

func NewHostBackend() *HostBackend { return &HostBackend{} }

// StartJob rejects container: and services: outright — real GitHub
// Actions only supports Docker container/service jobs on Linux runners,
// a constraint this mirrors rather than invents.
func (HostBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *ContainerSpec, services map[string]ContainerSpec) (Job, error) {
	if hostWorkspaceDir == "" {
		return nil, fmt.Errorf("hostWorkspaceDir must not be empty")
	}
	if containerSpec != nil {
		return nil, fmt.Errorf("container: is not supported on macOS jobs — real GitHub Actions only supports container jobs on Linux runners")
	}
	if len(services) > 0 {
		return nil, fmt.Errorf("services: is not supported on macOS jobs — real GitHub Actions only supports service containers on Linux runners")
	}

	root, err := os.MkdirTemp("", "mirror-host-job-")
	if err != nil {
		return nil, fmt.Errorf("create job root: %w", err)
	}
	filesRoot := filepath.Join(root, "files")
	if err := os.MkdirAll(filesRoot, 0o755); err != nil {
		os.RemoveAll(root)
		return nil, fmt.Errorf("create files root: %w", err)
	}

	return &hostJob{root: root, filesRoot: filesRoot, hostWorkspaceDir: hostWorkspaceDir}, nil
}

// translateMirrorPath rewrites the engine layer's synthetic "/mirror-*"
// path convention (ContainerNodePath, ContainerActionPath — meaningful
// today only because the Docker backend bind-mounts them inside an
// isolated container filesystem namespace) onto a real path under this
// job's own temp root. A bare host process has no such namespace: writing
// to the literal path "/mirror-node" on a real Mac would need root and
// would collide across concurrent or repeated runs. Any path NOT using
// this project's own "/mirror-" convention is returned unchanged — this
// only ever rewrites strings mirror-gha itself generates with that exact
// prefix, never workflow-author-controlled content.
func translateMirrorPath(root, p string) string {
	if !strings.HasPrefix(p, "/mirror-") {
		return p
	}
	return filepath.Join(root, p)
}

// hostJob is one job's execution environment when running on HostBackend —
// a real host temp directory tree, not a container.
type hostJob struct {
	root             string // per-job real temp root; "/mirror-*" paths translate under here
	filesRoot        string // real host dir backing workflow-command files directly — no translation needed, there's no container-side/host-side split to bridge
	hostWorkspaceDir string
}

func (j *hostJob) FilesRoot() string { return j.filesRoot }

// WorkspacePath returns the real workspace directory unchanged — there is
// nothing to bind-mount into, matching how a real self-hosted/macOS
// runner operates directly against the real checkout.
func (j *hostJob) WorkspacePath() string { return j.hostWorkspaceDir }

func (j *hostJob) Platform() string { return "darwin" }

func (j *hostJob) CopyToContainer(ctx context.Context, hostPath, containerPath string) error {
	dest := translateMirrorPath(j.root, containerPath)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dest, err)
	}
	// The trailing "/." on the source copies hostPath's *contents* into
	// dest rather than nesting hostPath's own basename one level deeper —
	// the same idiom dockerJob.CopyToContainer already relies on for
	// docker cp, and one BSD cp (macOS's default) supports identically.
	cmd := exec.CommandContext(ctx, "cp", "-R", hostPath+"/.", dest)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cp %s -> %s: %w: %s", hostPath, dest, err, stderr.String())
	}
	return nil
}

func (j *hostJob) Exec(ctx context.Context, spec StepSpec) (StepResult, error) {
	env := map[string]string{}
	for k, v := range spec.Env {
		if k == "GITHUB_ACTION_PATH" {
			v = translateMirrorPath(j.root, v)
		}
		env[k] = v
	}
	env["GITHUB_ENV"] = filepath.Join(spec.FilesDir, "github_env")
	env["GITHUB_PATH"] = filepath.Join(spec.FilesDir, "github_path")
	env["GITHUB_OUTPUT"] = filepath.Join(spec.FilesDir, "github_output")
	env["GITHUB_STEP_SUMMARY"] = filepath.Join(spec.FilesDir, "github_step_summary")
	env["GITHUB_STATE"] = filepath.Join(spec.FilesDir, "github_state")

	var cmd *exec.Cmd
	if len(spec.Args) > 0 {
		args := make([]string, len(spec.Args))
		for i, a := range spec.Args {
			args[i] = translateMirrorPath(j.root, a)
		}
		cmd = exec.CommandContext(ctx, args[0], args[1:]...)
	} else {
		shell := spec.Shell
		if shell == "" {
			// Real GitHub Actions' own documented default for both Linux
			// and macOS runners is bash, and every real macOS ships one
			// at a fixed path — unlike the Docker backend, which defaults
			// to sh (the lowest common denominator on a bare ubuntu:22.04
			// image). This divergence is scoped to this backend only.
			shell = "bash"
		}
		cmd = exec.CommandContext(ctx, shell, "-c", spec.Command)
	}
	if spec.WorkingDirectory != "" {
		cmd.Dir = spec.WorkingDirectory
	}
	// Real host env (PATH, HOME, etc.) is inherited on top of — a
	// deliberate divergence from the Docker backend's clean-container
	// env: a real self-hosted/macOS runner also executes as the logged-in
	// user with their real environment, and a run: step's command (e.g.
	// `brew`, `npm`) is expected to resolve via the real host PATH.
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return StepResult{}, fmt.Errorf("exec: %w", err)
		}
	}
	return StepResult{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// RunDockerAction always errors — real GitHub Actions only supports
// Docker container actions on Linux runners. internal/engine's
// prepareUsesStep already rejects these steps earlier via Platform(),
// before ever reaching here; this is a defense-in-depth backstop, not the
// primary enforcement point.
func (j *hostJob) RunDockerAction(ctx context.Context, spec DockerActionSpec) (StepResult, error) {
	return StepResult{}, fmt.Errorf("docker actions require a Linux job — real GitHub Actions only supports Docker container actions on Linux runners, not macOS")
}

func (j *hostJob) Stop(ctx context.Context) error {
	return os.RemoveAll(j.root)
}
```

- [ ] **Step 4: Run the tests**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/runner/... -run "TestTranslateMirrorPath|TestHostBackend" -v`
Expected: PASS (all tests — on this Mac dev machine `requireDarwin` never skips).

- [ ] **Step 5: Run the full test suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l . | grep -v third_party; go vet ./... && go test ./... 2>&1 | tail -20`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/runner/host_backend.go internal/runner/host_backend_test.go
git commit -m "feat(runner): add HostBackend for macOS jobs (no container, real os/exec)"
```

---

### Task 3: `SelectBackend` wiring, docs, example workflow, real end-to-end verification

**Files:**
- Modify: `internal/runner/backend.go`
- Test: `internal/runner/backend_test.go` (create if it doesn't exist — check first)
- Create: `examples/workflows/macos-job.yml`
- Modify: `docs/usage.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Check for an existing backend_test.go**

Run: `ls internal/runner/backend_test.go 2>&1`

If it exists, read it first to match its style before adding tests in Step 2.

- [ ] **Step 2: Write the failing tests**

Add to `internal/runner/backend_test.go` (create the file with `package runner` + needed imports if it doesn't exist):

```go
func TestSelectBackend_MacOSOnDarwinHost(t *testing.T) {
	requireDarwin(t)

	for _, label := range []string{"macos-latest", "macos-15", "macos-14", "macos-13"} {
		backend, err := SelectBackend(label)
		if err != nil {
			t.Errorf("SelectBackend(%q) error = %v, want a HostBackend on a darwin host", label, err)
			continue
		}
		if _, ok := backend.(*HostBackend); !ok {
			t.Errorf("SelectBackend(%q) = %T, want *HostBackend", label, backend)
		}
	}
}

func TestSelectBackend_UbuntuStillReturnsLinuxDockerBackend(t *testing.T) {
	backend, err := SelectBackend("ubuntu-latest")
	if err != nil {
		t.Fatalf("SelectBackend(ubuntu-latest) error = %v", err)
	}
	if _, ok := backend.(*LinuxDockerBackend); !ok {
		t.Errorf("SelectBackend(ubuntu-latest) = %T, want *LinuxDockerBackend", backend)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/runner/... -run TestSelectBackend -v`
Expected: FAIL — `SelectBackend("macos-latest")` currently returns `*ErrUnsupportedRunner`.

- [ ] **Step 4: Wire macOS labels into `SelectBackend`**

In `internal/runner/backend.go`, add `"runtime"` to the imports, then change `SelectBackend`:

```go
// SelectBackend maps a job's `runs-on` value to a concrete Backend.
func SelectBackend(runsOn string) (Backend, error) {
	switch runsOn {
	case "ubuntu-latest", "ubuntu-24.04", "ubuntu-22.04":
		return NewLinuxDockerBackend(), nil
	case "macos-latest", "macos-15", "macos-14", "macos-13":
		if runtime.GOOS != "darwin" {
			return nil, fmt.Errorf("runner %q requires running mirror-gha on a Mac host (this host is %s) — macOS cannot be virtualized on non-Apple hardware, so cross-host macOS execution isn't possible", runsOn, runtime.GOOS)
		}
		return NewHostBackend(), nil
	default:
		return nil, &ErrUnsupportedRunner{RunsOn: runsOn}
	}
}
```

- [ ] **Step 5: Run the tests**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/runner/... -run TestSelectBackend -v`
Expected: PASS.

- [ ] **Step 6: Run the full test suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l . | grep -v third_party; go vet ./... && go test ./... 2>&1 | tail -20`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add internal/runner/backend.go internal/runner/backend_test.go
git commit -m "feat(runner): wire macos-latest/-13/-14/-15 to HostBackend"
```

- [ ] **Step 8: Write the example workflow**

Create `examples/workflows/macos-job.yml`:

```yaml
# Demonstrates runs-on: macos-latest — executed directly on the host
# process (no container, no Docker at all), since macOS can't be
# virtualized or containerized on non-Apple hardware. Only runs for real
# when mirror-gha itself is running on a Mac; otherwise a clear error
# names the limitation.
#
# Try it:
#   mirror run examples/workflows/macos-job.yml
name: macos job
on: push
jobs:
  build:
    runs-on: macos-latest
    steps:
      - name: real host shell step
        run: echo "running natively on $(uname -s)"
      - name: real js action
        uses: actions/hello-world-javascript-action@v1
        with:
          who-to-greet: mirror-gha
```

(If `actions/hello-world-javascript-action@v1` isn't already used elsewhere in `examples/workflows/`, confirm it's a real, still-existing public action before relying on it — grep `examples/workflows/*.yml` for the JS action already used in `uses-composite-action.yml`/similar and reuse that exact same one for consistency instead, if one exists.)

- [ ] **Step 9: Run it for real**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go run ./cmd/mirror run examples/workflows/macos-job.yml 2>&1 | tail -40`
Expected: both steps succeed; the first step's output shows `Darwin` (not `Linux`) proving no container was involved; the JS action step actually runs via a real, natively-executed Node binary (no `docker` process involved at all — confirm with `ps aux | grep docker` showing no new container-related processes during the run if there's any doubt).

If anything fails, root-cause it for real (temporary logging if needed) and fix the actual bug — likely candidates: the `/mirror-*` path translation missing a spot, `GITHUB_ACTION_PATH` not being rewritten correctly, or the Node darwin tarball's internal layout differing from what `EnsureNode` assumes (verify by inspecting the actual extracted directory under `~/Library/Caches/mirror-gha/` or wherever `CacheRoot()` resolves to on this machine).

- [ ] **Step 10: Update docs/usage.md**

In `docs/usage.md`, remove `macos-latest`/macOS from the "Windows and macOS runners" bullet under "What's not supported yet" — check its exact current wording first (`grep -n "Windows and macOS" docs/usage.md`) and rewrite it to name only Windows, since macOS now works when the host itself is a Mac. Add a new bullet to "What's supported today":

```
- **`runs-on: macos-latest`/`macos-13`/`macos-14`/`macos-15`** — executes
  directly on the host process, no container at all, since macOS cannot
  be virtualized or containerized on non-Apple hardware. Only works when
  mirror-gha itself is running on a Mac — from any other host OS this
  returns a clear, specific error rather than a silent wrong attempt.
  `container:`, `services:`, and `uses: docker://...`/Docker-action steps
  all error clearly on macOS jobs, matching real GitHub Actions' own
  documented Linux-only constraint for these features. The default shell
  for a `run:` step with no `shell:` is `bash` here (matching real GitHub
  Actions' own default for macOS runners), not `sh` (the Docker backend's
  default). Job steps inherit mirror-gha's own real host environment
  (`PATH`, `HOME`, etc.) — unlike the Docker backend's clean-container
  environment — matching how a real self-hosted/macOS runner operates as
  the logged-in user.
```

- [ ] **Step 11: Update CHANGELOG.md**

Add to the `### Added` section, above the most recent entry:

```markdown
- **`runs-on: macos-latest`/`macos-13`/`macos-14`/`macos-15` (macOS host
  backend).** Checked against act's own host-execution mode
  (`pkg/container/host_environment.go`, `pkg/runner/run_context.go`)
  rather than guessed — though act's version is a generic, undocumented
  `-P label=-self-hosted` escape hatch with no macOS awareness at all,
  and it still silently requires Docker for a `uses: docker://...` step
  even in that mode. mirror-gha's macOS backend is a real, first-class
  backend: no container at all, real `os/exec` directly on the host,
  gated on `runtime.GOOS == "darwin"` (any other host OS gets a clear
  error, not a silent wrong attempt — cross-host macOS execution isn't
  possible, matching this project's documented hard constraint that
  macOS can't be virtualized on non-Apple hardware). `container:`,
  `services:`, and Docker-action steps all error clearly here, matching
  real GitHub Actions' own documented Linux-only constraint for those
  features. Required a real bug-shaped fix along the way: the pinned
  Node.js runtime downloader hardcoded `linux` in its download URL,
  correct only because the existing Docker backend always targets a
  Linux container regardless of what OS mirror-gha's own process runs on
  (this project is routinely developed on a Mac already) — naively
  switching that to the host OS would have broken the *existing* Docker
  backend on exactly this kind of machine. Fixed by adding `Job.Platform()`
  so each backend reports its own actual execution OS explicitly. Verified
  for real on this machine: a `runs-on: macos-latest` job running a plain
  shell step and a real, unmodified JS action, with no Docker process
  involved anywhere in the path.
```

- [ ] **Step 12: Final full-suite check**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l . | grep -v third_party && go vet ./... && go test ./... 2>&1 | tail -20`
Expected: clean build, no unformatted files, no vet errors, every package `ok`.

- [ ] **Step 13: Commit**

```bash
git add examples/workflows/macos-job.yml docs/usage.md CHANGELOG.md
git commit -m "docs: document macOS host backend support, add example workflow"
```

(If Step 9 surfaced and required a real bug fix, that fix should already be committed as its own commit before this one, with its own regression test — same discipline as every prior sub-project's final verification task this session.)
