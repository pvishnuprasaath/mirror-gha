# Docker Actions Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `uses:` steps resolve a Docker action — either a raw `docker://image:tag` reference, or a Marketplace/local action whose `runs.image` names a Dockerfile — and execute it for real as its own sibling container, with `with:`/`INPUT_*`, `$GITHUB_OUTPUT`, and the job workspace all working the same way they already do for JS actions.

**Architecture:** Docker actions run as a genuinely separate container per step (`docker run --rm`, blocking until exit), not `docker exec`'d into the job's own long-lived container — confirmed as act's real model too (`pkg/runner/action.go`'s `execAsDocker`, `pkg/runner/step_docker.go`'s `runUsesContainer`), not a mirror-gha-specific compromise. `runner.Job` gains one new method, `RunDockerAction`, alongside the existing `Exec`/`CopyToContainer`. `internal/engine/uses_step.go`'s `prepareUsesStep` is restructured to return a small plan type carrying either the JS-action argv (today's path, unchanged) or a `*runner.DockerActionSpec` — `RunJob`'s step loop branches once on which came back, and everything downstream (output-file parsing, legacy stdout-output parsing, `GITHUB_ENV` merge) stays untouched.

**Tech Stack:** Go 1.27 stdlib only, shelling out to `docker` (build/inspect/run), matching `internal/runner/docker_backend.go`'s and `internal/actions/fetch.go`'s existing shell-out patterns. No new dependencies.

**Spec:** `docs/design/specs/2026-09-14-mirror-gha-design.md`, "Docker Actions Runtime" section.

## Global Constraints

- Composite actions (`runs.using: composite`) stay out of scope — still rejected with a clear error.
- A raw `docker://image:tag` `uses:` reference has no `action.yml` at all — no fetch, no metadata, no `GITHUB_ACTION_PATH`. `entrypoint`/`args` come only from the step's own `with:` block.
- A Marketplace/local Docker action's `runs.image` is either `docker://image:tag` (used directly, no build) or a Dockerfile path relative to the action's own source directory (built via `docker build`, never against the job workspace).
- Image tag for a built action image: `mirror-gha-<sanitized-action-ref>:latest`, where sanitizing replaces every run of non-alphanumeric characters with `-`. Cache-checked via `docker image inspect <tag>` before rebuilding — no arch-mismatch detection, no force-rebuild flag (YAGNI, single Linux backend).
- `with:` inputs become `INPUT_*` env vars via the existing `actions.InputEnv` (already generic over `runs.using` — no changes needed there).
- `runs.entrypoint`/`runs.args` (action.yml) are overridden by `with: {entrypoint, args}` when set. `with.args`/`with.entrypoint` are plain strings split on whitespace (`strings.Fields`) — not a real shellwords split. This is a known, accepted simplification for v1 (documented in the design spec); revisit only if a real action needs quoted-argument splitting.
- `runs.args` (the action.yml array, when not overridden by `with.args`) is used as literal strings — **not** expression-substituted. GitHub's own `${{ inputs.x }}` substitution for `runs.args` references the action's own local `inputs` context, which mirror-gha's expression evaluator doesn't model at all (only `env`/`github`/`runner`/`steps`/`needs`/`matrix`/`vars`). Adding a fifth, action-scoped context for one field with no current caller is out of scope — `with.args`, which already gets substituted like every other `with:` value against the *workflow's* contexts, is the documented way to parameterize a Docker action's arguments.
- A Docker action container: bind-mounts the job's own workspace read-write at `/github/workspace` (same host path and container path as the job container), the action's own source read-only (repo-based actions only — nothing to mount for a raw `docker://` step), this step's `FilesDir` at `/mirror-files` (same convention `Exec` already uses for workflow-command files), `/var/run/docker.sock` unconditionally, and joins the job container's network namespace (`--network container:<jobContainerID>`) so `localhost` service-container access keeps working.
- TDD throughout: every task writes the failing test before the implementation.
- Docker-daemon-dependent tests skip gracefully via the existing `requireDocker(t)` pattern (duplicated per-package, matching how `requireNetwork(t)` is already duplicated across `internal/actions` and `internal/engine`).

---

### Task 1: `action.yml` Docker fields

**Files:**
- Modify: `internal/actions/metadata.go`
- Test: `internal/actions/metadata_test.go`

**Interfaces:**
- Produces: `ActionRuns.Image string`, `ActionRuns.Entrypoint string`, `ActionRuns.Args []string`, `ActionRuns.Env map[string]string`

- [ ] **Step 1: Write the failing test**

```go
// internal/actions/metadata_test.go — add this test
func TestParseMetadata_DockerRuns(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "action.yml", `
name: 'Docker Action'
runs:
  using: 'docker'
  image: 'Dockerfile'
  entrypoint: '/entrypoint.sh'
  args:
    - '--verbose'
    - '--name'
    - 'mirror-gha'
  env:
    GREETING_STYLE: 'formal'
`)

	meta, err := ParseMetadata(dir)
	if err != nil {
		t.Fatalf("ParseMetadata() error = %v", err)
	}
	if meta.Runs.Using != "docker" || meta.Runs.Image != "Dockerfile" {
		t.Errorf("Runs.Using/Image = %q/%q, want docker/Dockerfile", meta.Runs.Using, meta.Runs.Image)
	}
	if meta.Runs.Entrypoint != "/entrypoint.sh" {
		t.Errorf("Runs.Entrypoint = %q, want /entrypoint.sh", meta.Runs.Entrypoint)
	}
	wantArgs := []string{"--verbose", "--name", "mirror-gha"}
	if len(meta.Runs.Args) != len(wantArgs) {
		t.Fatalf("Runs.Args = %v, want %v", meta.Runs.Args, wantArgs)
	}
	for i, a := range wantArgs {
		if meta.Runs.Args[i] != a {
			t.Errorf("Runs.Args[%d] = %q, want %q", i, meta.Runs.Args[i], a)
		}
	}
	if meta.Runs.Env["GREETING_STYLE"] != "formal" {
		t.Errorf(`Runs.Env["GREETING_STYLE"] = %q, want %q`, meta.Runs.Env["GREETING_STYLE"], "formal")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/vishnu.prasaath/workspace/mirror-gha && go test ./internal/actions/... -run TestParseMetadata_DockerRuns -v`
Expected: FAIL — `meta.Runs.Image` (and `Entrypoint`/`Args`/`Env`) undefined on `ActionRuns`

- [ ] **Step 3: Implement**

```go
// internal/actions/metadata.go — replace the ActionRuns struct
type ActionRuns struct {
	Using      string            `yaml:"using"`
	Main       string            `yaml:"main"`
	Image      string            `yaml:"image"`
	Entrypoint string            `yaml:"entrypoint"`
	Args       []string          `yaml:"args"`
	Env        map[string]string `yaml:"env"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/actions/... -v`
Expected: PASS — full package suite

- [ ] **Step 5: Commit**

```bash
git add internal/actions/metadata.go internal/actions/metadata_test.go
git commit -m "feat(actions): parse Docker action.yml fields (image/entrypoint/args/env)"
```

---

### Task 2: Recognize raw `docker://image:tag` references

**Files:**
- Modify: `internal/actions/resolve.go`
- Test: `internal/actions/resolve_test.go`

**Interfaces:**
- Consumes: nothing new
- Produces: `ActionRef.Docker bool`, `ActionRef.DockerImage string`

A raw `docker://image:tag` `uses:` value has no `action.yml`, no repo, no local path — the image itself *is* the action. This must be recognized before the existing local-path/Marketplace parsing, since it doesn't fit either shape.

- [ ] **Step 1: Write the failing test**

```go
// internal/actions/resolve_test.go — add this test
func TestResolveUsesRef_DockerImage(t *testing.T) {
	ref, err := ResolveUsesRef("docker://alpine:3.19")
	if err != nil {
		t.Fatalf("ResolveUsesRef() error = %v", err)
	}
	if !ref.Docker {
		t.Error("Docker = false, want true")
	}
	if ref.DockerImage != "alpine:3.19" {
		t.Errorf("DockerImage = %q, want %q", ref.DockerImage, "alpine:3.19")
	}
	if ref.Local {
		t.Error("Local = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/actions/... -run TestResolveUsesRef_DockerImage -v`
Expected: FAIL — `ref.Docker`/`ref.DockerImage` undefined on `ActionRef`

- [ ] **Step 3: Implement**

```go
// internal/actions/resolve.go — replace the ActionRef struct
type ActionRef struct {
	Local     bool
	LocalPath string

	// Docker is set for a raw `docker://image:tag` reference — no
	// action.yml, no repo to fetch, no source directory. DockerImage is
	// the image reference with the docker:// prefix stripped.
	Docker      bool
	DockerImage string

	Owner   string
	Repo    string
	Subpath string
	Ref     string
}
```

Add this check as the *first* branch inside `ResolveUsesRef`, before the existing `./`/`../` check:

```go
	if strings.HasPrefix(uses, "docker://") {
		return ActionRef{Docker: true, DockerImage: strings.TrimPrefix(uses, "docker://")}, nil
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/actions/... -v`
Expected: PASS — full package suite, including the existing local/Marketplace tests unaffected

- [ ] **Step 5: Commit**

```bash
git add internal/actions/resolve.go internal/actions/resolve_test.go
git commit -m "feat(actions): recognize raw docker:// uses: references"
```

---

### Task 3: Resolve/build a Docker action's image

**Files:**
- Create: `internal/actions/docker.go`
- Test: `internal/actions/docker_test.go`

**Interfaces:**
- Consumes: `ActionRuns` (Task 1)
- Produces: `func ResolveDockerImage(ctx context.Context, hostSourceDir, actionRef string, runs ActionRuns) (string, error)`, `func BuildActionImage(ctx context.Context, hostSourceDir, dockerfileRelPath, actionRef string) (string, error)`

This is for a **repo-based** Docker action's `runs.image` (which can itself be `docker://...` or a Dockerfile path) — distinct from Task 2's raw `uses: docker://...` case, which has no `action.yml`/`runs` block at all.

- [ ] **Step 1: Write the failing tests**

```go
// internal/actions/docker_test.go
package actions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireDocker skips the test if the docker CLI isn't installed — mirrors
// internal/runner's requireDocker(t) pattern (duplicated per package, same
// as requireNetwork already is between internal/actions and internal/engine).
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping Docker action test")
	}
}

func TestResolveDockerImage_DockerPrefixUsedDirectly(t *testing.T) {
	image, err := ResolveDockerImage(context.Background(), t.TempDir(), "some/action@v1", ActionRuns{Image: "docker://alpine:3.19"})
	if err != nil {
		t.Fatalf("ResolveDockerImage() error = %v", err)
	}
	if image != "alpine:3.19" {
		t.Errorf("image = %q, want %q", image, "alpine:3.19")
	}
}

func TestResolveDockerImage_EmptyImageIsError(t *testing.T) {
	_, err := ResolveDockerImage(context.Background(), t.TempDir(), "some/action@v1", ActionRuns{})
	if err == nil {
		t.Fatal("ResolveDockerImage() error = nil, want error when runs.image is empty")
	}
}

func TestBuildActionImage_BuildsAndCaches(t *testing.T) {
	requireDocker(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	actionRef := "test/docker-action@v1-" + t.Name() // unique tag per test run, avoids cross-test cache collisions
	t.Cleanup(func() {
		tag, _ := imageTagFor(actionRef)
		exec.Command("docker", "rmi", tag).Run()
	})

	image, err := BuildActionImage(context.Background(), dir, "Dockerfile", actionRef)
	if err != nil {
		t.Fatalf("BuildActionImage() error = %v", err)
	}

	inspect := exec.Command("docker", "image", "inspect", image)
	if err := inspect.Run(); err != nil {
		t.Errorf("docker image inspect %s failed after build: %v", image, err)
	}

	image2, err := BuildActionImage(context.Background(), dir, "Dockerfile", actionRef)
	if err != nil {
		t.Fatalf("BuildActionImage() second call error = %v", err)
	}
	if image2 != image {
		t.Errorf("second BuildActionImage() = %q, want same tag %q", image2, image)
	}
}

func TestResolveDockerImage_DockerfilePathBuilds(t *testing.T) {
	requireDocker(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	actionRef := "test/via-resolve@v1-" + t.Name()
	t.Cleanup(func() {
		tag, _ := imageTagFor(actionRef)
		exec.Command("docker", "rmi", tag).Run()
	})

	image, err := ResolveDockerImage(context.Background(), dir, actionRef, ActionRuns{Image: "Dockerfile"})
	if err != nil {
		t.Fatalf("ResolveDockerImage() error = %v", err)
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Errorf("docker image inspect %s failed: %v", image, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/actions/... -run 'TestResolveDockerImage|TestBuildActionImage' -v`
Expected: FAIL — `ResolveDockerImage`/`BuildActionImage`/`imageTagFor` undefined

- [ ] **Step 3: Implement**

```go
// internal/actions/docker.go
package actions

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var dockerImageTagSanitizer = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// imageTagFor computes the tag a built action image is cached under —
// mirror-gha-<sanitized-actionRef>:latest, closely matching act's own
// tagging pattern (act-<sanitized>-dockeraction:latest, action.go) so
// `docker images` stays equally easy to cross-reference back to the
// action that owns a given local image.
func imageTagFor(actionRef string) (string, error) {
	if actionRef == "" {
		return "", fmt.Errorf("actionRef must not be empty")
	}
	return "mirror-gha-" + dockerImageTagSanitizer.ReplaceAllString(actionRef, "-") + ":latest", nil
}

// ResolveDockerImage returns the image to run for a repo-based Docker
// action (runs.using: docker). runs.Image is either a docker://image:tag
// reference — used exactly as given, since `docker run` auto-pulls a
// missing image on demand (no explicit Pull() step, unlike act's
// ForcePull-gated one — mirror-gha has no force-pull flag yet, YAGNI) —
// or a Dockerfile path relative to hostSourceDir (the action's own source
// directory, never the job workspace), which gets built.
func ResolveDockerImage(ctx context.Context, hostSourceDir, actionRef string, runs ActionRuns) (string, error) {
	if strings.HasPrefix(runs.Image, "docker://") {
		return strings.TrimPrefix(runs.Image, "docker://"), nil
	}
	if runs.Image == "" {
		return "", fmt.Errorf("docker action has no runs.image set")
	}
	return BuildActionImage(ctx, hostSourceDir, runs.Image, actionRef)
}

// BuildActionImage builds dockerfileRelPath (relative to hostSourceDir)
// into an image tagged via imageTagFor(actionRef). A cache hit — an image
// already exists under that tag — skips the build entirely; no arch check
// (mirror-gha has a single Linux Docker backend) and no force-rebuild
// flag (YAGNI until a real need shows up).
func BuildActionImage(ctx context.Context, hostSourceDir, dockerfileRelPath, actionRef string) (string, error) {
	tag, err := imageTagFor(actionRef)
	if err != nil {
		return "", err
	}

	inspect := exec.CommandContext(ctx, "docker", "image", "inspect", tag)
	if err := inspect.Run(); err == nil {
		return tag, nil
	}

	dockerfile := filepath.Join(hostSourceDir, dockerfileRelPath)
	build := exec.CommandContext(ctx, "docker", "build", "-f", dockerfile, "-t", tag, hostSourceDir)
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker build %s: %w: %s", dockerfile, err, out)
	}
	return tag, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/actions/... -v`
Expected: PASS — full package suite (Docker-dependent tests skip cleanly if `docker` isn't installed)

- [ ] **Step 5: Commit**

```bash
git add internal/actions/docker.go internal/actions/docker_test.go
git commit -m "feat(actions): resolve and build Docker action images"
```

---

### Task 4: `runner.Job.RunDockerAction`

**Files:**
- Modify: `internal/runner/backend.go`
- Modify: `internal/runner/docker_backend.go`
- Modify: `internal/runner/dryrun.go`
- Test: `internal/runner/docker_backend_test.go`, `internal/runner/dryrun_test.go`

**Interfaces:**
- Produces: `type DockerActionSpec struct{ Image string; Entrypoint []string; Args []string; Env map[string]string; ActionSourceDir string; ActionPathInContainer string; FilesDir string }`, `Job.RunDockerAction(ctx context.Context, spec DockerActionSpec) (StepResult, error)`

- [ ] **Step 1: Write the failing tests**

```go
// internal/runner/docker_backend_test.go — add these tests
func TestLinuxDockerBackend_RunDockerAction_WorkspaceMountAndArgs(t *testing.T) {
	requireDocker(t)

	workspaceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceDir, "marker.txt"), []byte("marker-content\n"), 0o644); err != nil {
		t.Fatalf("write marker file: %v", err)
	}

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), workspaceDir)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	result, err := job.RunDockerAction(context.Background(), DockerActionSpec{
		Image:    "alpine:3.19",
		Args:     []string{"cat", "/github/workspace/marker.txt"},
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("RunDockerAction() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "marker-content") {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, "marker-content")
	}
}

func TestLinuxDockerBackend_RunDockerAction_EntrypointOverride(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	result, err := job.RunDockerAction(context.Background(), DockerActionSpec{
		Image:      "alpine:3.19",
		Entrypoint: []string{"sh", "-c"},
		Args:       []string{"echo entrypoint-override"},
		FilesDir:   mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("RunDockerAction() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "entrypoint-override") {
		t.Errorf("Stdout = %q, want to contain %q", result.Stdout, "entrypoint-override")
	}
}

func TestLinuxDockerBackend_RunDockerAction_EnvAndOutputFile(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	filesDir := mkStepDir(t, job.FilesRoot())
	result, err := job.RunDockerAction(context.Background(), DockerActionSpec{
		Image:      "alpine:3.19",
		Entrypoint: []string{"sh", "-c"},
		Args:       []string{`echo "greeting=hello $INPUT_NAME" >> "$GITHUB_OUTPUT"`},
		Env:        map[string]string{"INPUT_NAME": "mirror-gha"},
		FilesDir:   filesDir,
	})
	if err != nil {
		t.Fatalf("RunDockerAction() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	outputData, err := os.ReadFile(filepath.Join(filesDir, "github_output"))
	if err != nil {
		t.Fatalf("read github_output: %v", err)
	}
	if !strings.Contains(string(outputData), "greeting=hello mirror-gha") {
		t.Errorf("github_output = %q, want to contain %q", outputData, "greeting=hello mirror-gha")
	}
}
```

```go
// internal/runner/dryrun_test.go — add this test
func TestDryRunBackend_RunDockerActionIsNoOp(t *testing.T) {
	backend := DryRunBackend{}
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	result, err := job.RunDockerAction(context.Background(), DockerActionSpec{Image: "whatever:latest"})
	if err != nil {
		t.Errorf("RunDockerAction() error = %v, want nil (no-op in dry-run mode)", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/runner/... -run 'RunDockerAction' -v`
Expected: FAIL — `RunDockerAction`/`DockerActionSpec` undefined

- [ ] **Step 3: Implement**

In `internal/runner/backend.go`, add after `StepSpec`:

```go
// DockerActionSpec is everything dockerJob.RunDockerAction needs to run a
// uses: step whose action has runs.using: docker — a genuinely separate
// sibling container, not an exec into the job's own long-lived container.
// A Docker action's image is frequently a completely different base OS
// than the job's own ubuntu:22.04, so it can't be docker-cp'd/exec'd into
// the job container the way a JS action's Node runtime can be. Matches
// act's own model: even act, which also runs one persistent container per
// job, spins up Docker actions as their own container
// (pkg/runner/action.go's execAsDocker).
type DockerActionSpec struct {
	Image      string
	Entrypoint []string
	Args       []string
	Env        map[string]string

	// ActionSourceDir is the host path to a repo-based action's own
	// source, bind-mounted read-only at ActionPathInContainer. Empty for
	// a raw docker://image:tag step, which has no action source at all.
	ActionSourceDir       string
	ActionPathInContainer string

	// FilesDir is this step's host dir for the workflow-command file
	// protocol (GITHUB_ENV/PATH/OUTPUT/STEP_SUMMARY) — same convention as
	// StepSpec.FilesDir, must be created under Job.FilesRoot().
	FilesDir string
}
```

Add to the `Job` interface, right after `Exec`:

```go
	// RunDockerAction runs a Docker action (runs.using: docker) as its own
	// container, joined to the job container's network namespace so
	// localhost service-container access keeps working — see
	// DockerActionSpec's doc comment for why this can't just be an Exec.
	RunDockerAction(ctx context.Context, spec DockerActionSpec) (StepResult, error)
```

In `internal/runner/docker_backend.go`, add a `hostWorkspaceDir` field to `dockerJob` and store it in `StartJob`:

```go
type dockerJob struct {
	containerID      string
	hostFilesRoot    string
	hostWorkspaceDir string
}
```

```go
	return &dockerJob{
		containerID:      strings.TrimSpace(stdout.String()),
		hostFilesRoot:    hostFilesRoot,
		hostWorkspaceDir: hostWorkspaceDir,
	}, nil
```

Then add the new method:

```go
// RunDockerAction runs spec.Image as its own container — docker run --rm,
// blocking until it exits — rather than docker exec into the job's own
// container (see DockerActionSpec's doc comment for why). Binds the same
// host workspace directory the job container itself uses, this step's
// FilesDir for the workflow-command file protocol, the action's own
// source (repo-based actions only), and /var/run/docker.sock
// unconditionally — matching act's and real GitHub-hosted runners' own
// behavior. --network container:<jobContainerID> joins the job
// container's network namespace so localhost service-container access
// keeps working, matching act's NetworkMode exactly.
func (j *dockerJob) RunDockerAction(ctx context.Context, spec DockerActionSpec) (StepResult, error) {
	args := []string{"run", "--rm",
		"-v", j.hostWorkspaceDir + ":" + containerWorkspaceMount,
		"-w", containerWorkspaceMount,
		"-v", spec.FilesDir + ":" + containerFilesMount,
		"-e", "GITHUB_WORKSPACE=" + containerWorkspaceMount,
		"-e", "GITHUB_ENV=" + containerFilesMount + "/github_env",
		"-e", "GITHUB_PATH=" + containerFilesMount + "/github_path",
		"-e", "GITHUB_OUTPUT=" + containerFilesMount + "/github_output",
		"-e", "GITHUB_STEP_SUMMARY=" + containerFilesMount + "/github_step_summary",
	}
	if spec.ActionSourceDir != "" {
		args = append(args, "-v", spec.ActionSourceDir+":"+spec.ActionPathInContainer+":ro")
		args = append(args, "-e", "GITHUB_ACTION_PATH="+spec.ActionPathInContainer)
	}
	for k, v := range spec.Env {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args,
		"--network", "container:"+j.containerID,
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
	)

	var command []string
	if len(spec.Entrypoint) > 0 {
		args = append(args, "--entrypoint", spec.Entrypoint[0])
		command = append(append([]string{}, spec.Entrypoint[1:]...), spec.Args...)
	} else {
		command = spec.Args
	}
	args = append(args, spec.Image)
	args = append(args, command...)

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return StepResult{}, fmt.Errorf("docker run (docker action %s): %w", spec.Image, err)
		}
	}
	return StepResult{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}
```

In `internal/runner/dryrun.go`, add:

```go
func (j *dryRunJob) RunDockerAction(ctx context.Context, spec DockerActionSpec) (StepResult, error) {
	return StepResult{ExitCode: 0, Stdout: "(dry run: not executed)\n"}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/runner/... -v`
Expected: PASS (Docker-dependent tests skip cleanly if `docker` isn't installed)

- [ ] **Step 5: Commit**

```bash
git add internal/runner/backend.go internal/runner/docker_backend.go internal/runner/dryrun.go internal/runner/docker_backend_test.go internal/runner/dryrun_test.go
git commit -m "feat(runner): add Job.RunDockerAction"
```

---

### Task 5: Wire Docker actions into the job executor

**Files:**
- Modify: `internal/engine/uses_step.go`
- Modify: `internal/engine/executor.go`
- Modify: `internal/engine/executor_test.go` (add `fakeJob.RunDockerAction`)
- Test: `internal/engine/uses_step_test.go`

**Interfaces:**
- Consumes: `actions.ResolveDockerImage` (Task 3), `runner.DockerActionSpec`/`Job.RunDockerAction` (Task 4), `ActionRef.Docker`/`DockerImage` (Task 2)
- Produces: `type usesStepPlan struct{ Args []string; Env map[string]string; Docker *runner.DockerActionSpec }`, `func prepareUsesStep(...) (usesStepPlan, error)` (signature otherwise unchanged from today)

This task changes `prepareUsesStep`'s return shape, so the two existing tests in `uses_step_test.go` need rewriting alongside the new ones — not just additions.

- [ ] **Step 1: Write the failing tests**

Replace `uses_step_test.go`'s two existing tests and add three new ones:

```go
// internal/engine/uses_step_test.go — replace TestPrepareUsesStep_LocalAction and
// TestPrepareUsesStep_RejectsNonNodeRuntime with the following five tests

func TestPrepareUsesStep_LocalAction(t *testing.T) {
	requireNetwork(t)

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

	plan, err := prepareUsesStep(context.Background(), job, workspaceDir, "greet", step, actx, &nodeReady)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v", err)
	}

	wantArgs := []string{"/mirror-node/bin/node", "/mirror-actions/greet/index.js"}
	if len(plan.Args) != len(wantArgs) || plan.Args[0] != wantArgs[0] || plan.Args[1] != wantArgs[1] {
		t.Errorf("Args = %v, want %v", plan.Args, wantArgs)
	}
	if plan.Env["INPUT_GREETING"] != "hi" {
		t.Errorf(`Env["INPUT_GREETING"] = %q, want %q`, plan.Env["INPUT_GREETING"], "hi")
	}
	if plan.Env["GITHUB_ACTION_PATH"] != "/mirror-actions/greet" {
		t.Errorf(`Env["GITHUB_ACTION_PATH"] = %q, want %q`, plan.Env["GITHUB_ACTION_PATH"], "/mirror-actions/greet")
	}
	if plan.Docker != nil {
		t.Error("Docker = non-nil, want nil for a node action")
	}
	if !nodeReady {
		t.Error("nodeReady = false, want true after the first uses: step")
	}
}

func TestPrepareUsesStep_RejectsCompositeRuntime(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "composite-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := "name: 'Composite Action'\nruns:\n  using: 'composite'\n"
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./composite-action"}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := true

	_, err := prepareUsesStep(context.Background(), job, workspaceDir, "one", step, actx, &nodeReady)
	if err == nil {
		t.Fatal("prepareUsesStep() error = nil, want error for runs.using: composite")
	}
}

func TestPrepareUsesStep_RawDockerImage(t *testing.T) {
	workspaceDir := t.TempDir()
	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{
		Uses: "docker://alpine:3.19",
		With: map[string]string{"entrypoint": "sh", "args": "-c echo-hi"},
	}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := false

	plan, err := prepareUsesStep(context.Background(), job, workspaceDir, "raw", step, actx, &nodeReady)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v", err)
	}
	if plan.Docker == nil {
		t.Fatal("Docker = nil, want non-nil for a raw docker:// step")
	}
	if plan.Docker.Image != "alpine:3.19" {
		t.Errorf("Docker.Image = %q, want %q", plan.Docker.Image, "alpine:3.19")
	}
	wantEntrypoint := []string{"sh"}
	if len(plan.Docker.Entrypoint) != 1 || plan.Docker.Entrypoint[0] != wantEntrypoint[0] {
		t.Errorf("Docker.Entrypoint = %v, want %v", plan.Docker.Entrypoint, wantEntrypoint)
	}
	wantArgs := []string{"-c", "echo-hi"}
	if len(plan.Docker.Args) != len(wantArgs) || plan.Docker.Args[0] != wantArgs[0] || plan.Docker.Args[1] != wantArgs[1] {
		t.Errorf("Docker.Args = %v, want %v", plan.Docker.Args, wantArgs)
	}
	if plan.Docker.ActionSourceDir != "" {
		t.Errorf("Docker.ActionSourceDir = %q, want empty (no action.yml for a raw docker:// step)", plan.Docker.ActionSourceDir)
	}
}

func TestPrepareUsesStep_RepoBasedDockerAction(t *testing.T) {
	requireDocker(t)

	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "docker-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := `
name: 'Docker Action'
inputs:
  who-to-greet:
    default: 'World'
runs:
  using: 'docker'
  image: 'docker://alpine:3.19'
  env:
    STATIC_VAR: 'set-by-action-yml'
`
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	job := &fakeJob{dir: t.TempDir(), workspaceDir: workspaceDir}
	step := Step{Uses: "./docker-action", With: map[string]string{"who-to-greet": "mirror-gha"}}
	actx := NewContext(&Workflow{}, &Job{})
	nodeReady := false

	plan, err := prepareUsesStep(context.Background(), job, workspaceDir, "greet", step, actx, &nodeReady)
	if err != nil {
		t.Fatalf("prepareUsesStep() error = %v", err)
	}
	if plan.Docker == nil {
		t.Fatal("Docker = nil, want non-nil for a docker-using action")
	}
	if plan.Docker.Image != "alpine:3.19" {
		t.Errorf("Docker.Image = %q, want %q", plan.Docker.Image, "alpine:3.19")
	}
	if plan.Docker.ActionSourceDir != actionDir {
		t.Errorf("Docker.ActionSourceDir = %q, want %q", plan.Docker.ActionSourceDir, actionDir)
	}
	if plan.Docker.ActionPathInContainer != "/mirror-actions/greet" {
		t.Errorf("Docker.ActionPathInContainer = %q, want %q", plan.Docker.ActionPathInContainer, "/mirror-actions/greet")
	}
	if plan.Env["INPUT_WHO-TO-GREET"] != "mirror-gha" {
		t.Errorf(`Env["INPUT_WHO-TO-GREET"] = %q, want %q`, plan.Env["INPUT_WHO-TO-GREET"], "mirror-gha")
	}
	if plan.Env["STATIC_VAR"] != "set-by-action-yml" {
		t.Errorf(`Env["STATIC_VAR"] = %q, want %q`, plan.Env["STATIC_VAR"], "set-by-action-yml")
	}
	if nodeReady {
		t.Error("nodeReady = true, want false — a Docker action must never trigger Node setup")
	}
}
```

Add `RunDockerAction` to `fakeJob` in `executor_test.go`:

```go
// internal/engine/executor_test.go — add this field to fakeJob and this method
type fakeJob struct {
	results      []runner.StepResult
	calls        int
	dir          string
	workspaceDir string
	execSpecs    []runner.StepSpec
	dockerSpecs  []runner.DockerActionSpec
}

func (j *fakeJob) RunDockerAction(ctx context.Context, spec runner.DockerActionSpec) (runner.StepResult, error) {
	j.dockerSpecs = append(j.dockerSpecs, spec)
	r := j.results[j.calls]
	j.calls++
	return r, nil
}
```

And a `RunJob`-level orchestration test proving the branch actually fires:

```go
// internal/engine/executor_test.go — add this test
func TestRunJob_DockerActionStepCallsRunDockerAction(t *testing.T) {
	workspaceDir := t.TempDir()
	actionDir := filepath.Join(workspaceDir, "docker-action")
	if err := os.MkdirAll(actionDir, 0o755); err != nil {
		t.Fatalf("mkdir action dir: %v", err)
	}
	actionYML := "name: 'Docker Action'\nruns:\n  using: 'docker'\n  image: 'docker://alpine:3.19'\n"
	if err := os.WriteFile(filepath.Join(actionDir, "action.yml"), []byte(actionYML), 0o644); err != nil {
		t.Fatalf("write action.yml: %v", err)
	}

	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Uses: "./docker-action"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}

	result, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{WorkspaceDir: workspaceDir})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if result.Conclusion != "success" {
		t.Errorf("Conclusion = %q, want success", result.Conclusion)
	}
	if len(backend.lastJob.dockerSpecs) != 1 {
		t.Fatalf("dockerSpecs = %d entries, want 1", len(backend.lastJob.dockerSpecs))
	}
	if backend.lastJob.dockerSpecs[0].Image != "alpine:3.19" {
		t.Errorf("dockerSpecs[0].Image = %q, want %q", backend.lastJob.dockerSpecs[0].Image, "alpine:3.19")
	}
	if len(backend.lastJob.execSpecs) != 0 {
		t.Errorf("execSpecs = %d entries, want 0 (a Docker action step must never call Exec)", len(backend.lastJob.execSpecs))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/engine/... -run 'TestPrepareUsesStep|TestRunJob_DockerActionStepCallsRunDockerAction' -v`
Expected: FAIL — `prepareUsesStep` still returns 3 values, `fakeJob` doesn't implement `RunDockerAction` yet, `usesStepPlan` undefined

- [ ] **Step 3: Implement**

Replace `internal/engine/uses_step.go` in full:

```go
package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"mirror-gha/internal/actions"
	"mirror-gha/internal/runner"
)

// usesStepPlan is what prepareUsesStep resolves a uses: step down to.
// Exactly one execution shape applies per step: a node action returns
// Args (exec'd directly into the job container, see runner.StepSpec's own
// doc comment for why no shell), a Docker action returns Docker (run as
// its own sibling container instead — see DockerActionSpec's doc comment
// for why it can't just be an Exec). Env carries INPUT_*/GITHUB_ACTION_PATH
// (plus runs.env for Docker actions) either way, merged into the step's
// env by the caller exactly like today.
type usesStepPlan struct {
	Args   []string
	Env    map[string]string
	Docker *runner.DockerActionSpec
}

// prepareUsesStep resolves and stages a uses: step's action. actx is used
// only for expression substitution inside with: values; the caller still
// owns the workflow-command file protocol and output parsing, identical
// to run: steps.
func prepareUsesStep(ctx context.Context, job runner.Job, workspaceDir, stepID string, step Step, actx *Context, nodeReady *bool) (usesStepPlan, error) {
	with := map[string]string{}
	for k, v := range step.With {
		val, err := SubstituteExpressions(v, actx)
		if err != nil {
			return usesStepPlan{}, fmt.Errorf("substitute with.%s: %w", k, err)
		}
		with[k] = val
	}

	ref, err := actions.ResolveUsesRef(step.Uses)
	if err != nil {
		return usesStepPlan{}, err
	}

	// A raw docker://image:tag reference has no action.yml, no repo, no
	// source directory at all — the image itself is the action.
	// entrypoint/args come only from this step's own with: block.
	if ref.Docker {
		spec := &runner.DockerActionSpec{Image: ref.DockerImage}
		if v, ok := with["entrypoint"]; ok && v != "" {
			spec.Entrypoint = strings.Fields(v)
		}
		if v, ok := with["args"]; ok {
			spec.Args = strings.Fields(v)
		}
		return usesStepPlan{Docker: spec}, nil
	}

	cacheRoot, err := actions.CacheRoot()
	if err != nil {
		return usesStepPlan{}, fmt.Errorf("resolve cache root: %w", err)
	}

	var hostSourceDir string
	if ref.Local {
		hostSourceDir = filepath.Join(workspaceDir, ref.LocalPath)
	} else {
		actionDir, err := actions.FetchRemote(ref.Owner, ref.Repo, ref.Ref, cacheRoot)
		if err != nil {
			return usesStepPlan{}, fmt.Errorf("fetch action %s: %w", step.Uses, err)
		}
		hostSourceDir = actionDir
		if ref.Subpath != "" {
			hostSourceDir = filepath.Join(actionDir, ref.Subpath)
		}
	}

	metadata, err := actions.ParseMetadata(hostSourceDir)
	if err != nil {
		return usesStepPlan{}, fmt.Errorf("parse action metadata for %s: %w", step.Uses, err)
	}

	containerActionPath := actions.ContainerActionPath(stepID)

	switch {
	case strings.HasPrefix(metadata.Runs.Using, "node"):
		if !*nodeReady {
			nodeDir, err := actions.EnsureNode(cacheRoot)
			if err != nil {
				return usesStepPlan{}, fmt.Errorf("ensure node runtime: %w", err)
			}
			if err := job.CopyToContainer(ctx, nodeDir, actions.ContainerNodePath); err != nil {
				return usesStepPlan{}, fmt.Errorf("copy node runtime into job: %w", err)
			}
			*nodeReady = true
		}
		if err := job.CopyToContainer(ctx, hostSourceDir, containerActionPath); err != nil {
			return usesStepPlan{}, fmt.Errorf("copy action %s into job: %w", step.Uses, err)
		}
		env := actions.InputEnv(metadata, with)
		env["GITHUB_ACTION_PATH"] = containerActionPath
		args := []string{actions.ContainerNodePath + "/bin/node", containerActionPath + "/" + metadata.Runs.Main}
		return usesStepPlan{Args: args, Env: env}, nil

	case metadata.Runs.Using == "docker":
		image, err := actions.ResolveDockerImage(ctx, hostSourceDir, step.Uses, metadata.Runs)
		if err != nil {
			return usesStepPlan{}, fmt.Errorf("resolve docker image for %s: %w", step.Uses, err)
		}

		entrypoint := metadata.Runs.Entrypoint
		if v, ok := with["entrypoint"]; ok {
			entrypoint = v
		}
		args := metadata.Runs.Args
		if v, ok := with["args"]; ok {
			args = strings.Fields(v)
		}

		env := actions.InputEnv(metadata, with)
		for k, v := range metadata.Runs.Env {
			env[k] = v
		}

		spec := &runner.DockerActionSpec{
			Image:                 image,
			Args:                  args,
			ActionSourceDir:       hostSourceDir,
			ActionPathInContainer: containerActionPath,
		}
		if entrypoint != "" {
			spec.Entrypoint = strings.Fields(entrypoint)
		}
		return usesStepPlan{Env: env, Docker: spec}, nil

	default:
		return usesStepPlan{}, fmt.Errorf("action %s has runs.using=%q, which isn't supported yet (only JS/node and Docker actions run today)", step.Uses, metadata.Runs.Using)
	}
}
```

Now update `internal/engine/executor.go`'s `RunJob`. Replace this block:

```go
		var command string
		var args []string
		var usesEnv map[string]string
		if step.Uses != "" {
			args, usesEnv, err = prepareUsesStep(ctx, runnerJob, opts.WorkspaceDir, id, step, actx, &nodeReady)
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

with:

```go
		var command string
		var plan usesStepPlan
		if step.Uses != "" {
			plan, err = prepareUsesStep(ctx, runnerJob, opts.WorkspaceDir, id, step, actx, &nodeReady)
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

Replace this block (the env-merging loop):

```go
		env["GITHUB_WORKSPACE"] = runnerJob.WorkspacePath()
		for k, v := range usesEnv {
			env[k] = v
		}
```

with:

```go
		env["GITHUB_WORKSPACE"] = runnerJob.WorkspacePath()
		for k, v := range plan.Env {
			env[k] = v
		}
```

Finally, replace the `Exec` call itself:

```go
		stepResult, err := runnerJob.Exec(stepCtx, runner.StepSpec{
			Command:          command,
			Args:             args,
			Shell:            effectiveShell(step, job, wf),
			Env:              env,
			WorkingDirectory: workingDirectory,
			FilesDir:         filesDir,
		})
```

with:

```go
		var stepResult runner.StepResult
		if plan.Docker != nil {
			plan.Docker.Env = env
			plan.Docker.FilesDir = filesDir
			stepResult, err = runnerJob.RunDockerAction(stepCtx, *plan.Docker)
		} else {
			stepResult, err = runnerJob.Exec(stepCtx, runner.StepSpec{
				Command:          command,
				Args:             plan.Args,
				Shell:            effectiveShell(step, job, wf),
				Env:              env,
				WorkingDirectory: workingDirectory,
				FilesDir:         filesDir,
			})
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/engine/... -v`
Expected: PASS — full engine package suite (network/Docker-dependent tests skip cleanly if unavailable)

- [ ] **Step 5: Commit**

```bash
git add internal/engine/uses_step.go internal/engine/uses_step_test.go internal/engine/executor.go internal/engine/executor_test.go
git commit -m "feat(engine): execute Docker actions as sibling containers"
```

---

### Task 6: Real end-to-end verification — raw image + Dockerfile-based fixture

**Files:**
- Create: `examples/workflows/actions/docker-hello-action/action.yml`
- Create: `examples/workflows/actions/docker-hello-action/Dockerfile`
- Create: `examples/workflows/actions/docker-hello-action/entrypoint.sh`
- Create: `examples/workflows/uses-docker-image.yml`
- Create: `examples/workflows/uses-docker-action.yml`
- Modify: `examples/README.md`, `docs/usage.md`, `CHANGELOG.md`, `docs/design/specs/2026-09-14-mirror-gha-design.md`

**Interfaces:**
- None new — this task is the real, no-fakes proof that Tasks 1-5 work together end-to-end.

- [ ] **Step 1: Create the Dockerfile-based action fixture**

```yaml
# examples/workflows/actions/docker-hello-action/action.yml
name: 'Docker Hello Action'
description: 'A minimal Docker action, for testing uses: (runs.using: docker) with mirror-gha'
inputs:
  who-to-greet:
    description: 'Who to greet'
    required: true
    default: 'World'
outputs:
  greeting:
    description: 'The greeting message'
runs:
  using: 'docker'
  image: 'Dockerfile'
```

```dockerfile
# examples/workflows/actions/docker-hello-action/Dockerfile
FROM alpine:3.19
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
```

```sh
# examples/workflows/actions/docker-hello-action/entrypoint.sh
#!/bin/sh
set -e

# GitHub Actions' INPUT_* convention allows dashes in input names, which
# aren't valid POSIX shell variable-name characters — $INPUT_WHO-TO-GREET
# would parse as "$INPUT_WHO" followed by a literal "-TO-GREET". printenv
# reads the raw environment entry directly, sidestepping shell variable
# syntax entirely (the same underlying fact that made mirror-gha's JS
# action support bypass the shell for exec — see runner.StepSpec's doc
# comment — just encountered from the action-author's side this time).
who="$(printenv 'INPUT_WHO-TO-GREET' || true)"
who="${who:-World}"

echo "Hello, $who! (from a Docker action)"
echo "greeting=Hello, $who!" >> "$GITHUB_OUTPUT"
```

- [ ] **Step 2: Create the Dockerfile-based example workflow**

```yaml
# examples/workflows/uses-docker-action.yml
# Demonstrates uses: with a repo-based Docker action (runs.using: docker,
# runs.image: Dockerfile) — builds the action's own Dockerfile (cached by
# tag after the first run), runs it as a separate sibling container, and
# reads its output back via steps.<id>.outputs.<name>.
#
# Try it (from the repo root):
#   mirror run examples/workflows/uses-docker-action.yml
name: uses docker action
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: greet
        id: greet
        uses: ./examples/workflows/actions/docker-hello-action
        with:
          who-to-greet: 'mirror-gha'
      - name: use the output
        run: echo "Got greeting: ${{ steps.greet.outputs.greeting }}"
```

- [ ] **Step 3: Run it for real, twice, to verify output and image caching**

Run:
```bash
cd /Users/vishnu.prasaath/workspace/mirror-gha
go build -o bin/mirror ./cmd/mirror
docker rmi mirror-gha-.-examples-workflows-actions-docker-hello-action:latest 2>/dev/null || true
time ./bin/mirror run examples/workflows/uses-docker-action.yml
time ./bin/mirror run examples/workflows/uses-docker-action.yml
```
Expected: both runs report `success` for both steps; output includes `Hello, mirror-gha! (from a Docker action)` and `Got greeting: Hello, mirror-gha!`; the second run's wall-clock time is visibly shorter than the first (no rebuild — confirm with `docker image inspect mirror-gha-.-examples-workflows-actions-docker-hello-action:latest` succeeding without a fresh `docker build` in between).

- [ ] **Step 4: Create the raw docker:// image example workflow**

```yaml
# examples/workflows/uses-docker-image.yml
# Demonstrates uses: with a raw docker://image:tag reference — no
# action.yml at all, entrypoint/args come from this step's own with:
# block. Proves the job workspace bind mount and network namespace join
# both work for a Docker action exactly like they do for run: steps.
#
# Try it (from the repo root):
#   mirror run examples/workflows/uses-docker-image.yml
name: uses raw docker image
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: write a marker file
        run: echo "written by a run: step" > marker.txt
      - name: read it back from a raw docker:// step
        uses: docker://alpine:3.19
        with:
          entrypoint: 'cat'
          args: '/github/workspace/marker.txt'
```

- [ ] **Step 5: Run it for real and verify the output**

Run: `./bin/mirror run examples/workflows/uses-docker-image.yml`
Expected: both steps report `success`; output includes `written by a run: step` — proving the raw docker:// container saw the file the earlier `run:` step wrote to the shared workspace.

- [ ] **Step 6: Re-run the full existing example suite to check for regressions**

Run:
```bash
for f in examples/workflows/*.yml; do
  echo "=== $f ==="
  ./bin/mirror run "$f" || echo "FAILED: $f"
done
```
Expected: every workflow prints `success` for all its steps; no `FAILED` lines.

- [ ] **Step 7: Update documentation**

In `examples/README.md`, add to the table:

```markdown
| [`uses-docker-action.yml`](workflows/uses-docker-action.yml) | `uses:` with a repo-based Docker action ([`actions/docker-hello-action`](workflows/actions/docker-hello-action/)) — builds its Dockerfile (cached by tag after the first run), runs as a separate sibling container |
| [`uses-docker-image.yml`](workflows/uses-docker-image.yml) | `uses:` with a raw `docker://image:tag` reference — no `action.yml`, `entrypoint`/`args` from `with:`, proves the job workspace bind mount and network join both work |
```

Update its "What's not shown here (yet)" paragraph to drop Docker actions from the unsupported list — only composite actions, artifacts, caching, `matrix.include`/`exclude`, and Windows/macOS runners remain unsupported.

In `docs/usage.md`, add a bullet under "What's supported today" (right after the existing `uses:` JS actions bullet):

```markdown
- **`uses:` Docker actions** — a raw `docker://image:tag` reference, or a
  Marketplace/local action whose `runs.image` names a Dockerfile (built
  and cached by tag, rebuilt only if the tag doesn't already exist). Runs
  as its own container — not exec'd into the job's container, since a
  Docker action's image is frequently a different base OS entirely —
  joined to the job container's network namespace so `localhost`
  service-container access still works, with the job workspace, `with:`/
  `INPUT_*`, and `$GITHUB_OUTPUT` all working the same way they do
  everywhere else. `/var/run/docker.sock` is mounted into every Docker
  action container unconditionally, matching real GitHub-hosted runners —
  this gives any Docker action, including an unmodified Marketplace one,
  full host Docker daemon control the moment it runs. Composite actions
  (`runs.using: composite`) are still rejected with a clear error.
```

Update "What's not supported yet" to read:

```markdown
- Composite actions (`runs.using: composite`) — JS and Docker actions
  (Marketplace, local, and raw `docker://` references) all work; composite
  step-graph expansion doesn't yet
```

In `CHANGELOG.md`, add under `### Added`:

```markdown
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
```

In `docs/design/specs/2026-09-14-mirror-gha-design.md`, change the "Docker Actions Runtime" section's opening line — add right after the heading, before `**Scope:**`:

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
git add examples/workflows/actions/docker-hello-action examples/workflows/uses-docker-image.yml examples/workflows/uses-docker-action.yml examples/README.md docs/usage.md CHANGELOG.md docs/design/specs/2026-09-14-mirror-gha-design.md
git commit -m "feat: verify Docker actions end-to-end with raw image + Dockerfile fixture"
```

## Self-Review Notes

- **Spec coverage:** Every piece of the "Docker Actions Runtime" spec section maps to a task — `action.yml` Docker fields (Task 1), raw `docker://` recognition (Task 2), image resolution/build/caching (Task 3), `RunDockerAction`/mounts/networking/socket (Task 4), execution-flow dispatch including the raw-image special case (Task 5), real end-to-end proof of both the raw-image and Dockerfile-build-and-cache paths (Task 6). Composite actions are explicitly out of scope per the spec and stay rejected, not implemented.
- **Placeholder scan:** No TBD/TODO; every step has complete, real code.
- **Type consistency:** `ActionRuns.{Image,Entrypoint,Args,Env}` (Task 1) are read identically in Tasks 3 and 5. `ActionRef.{Docker,DockerImage}` (Task 2) is checked identically in Task 5's `prepareUsesStep`. `runner.DockerActionSpec`'s fields (Task 4) match exactly how Task 5 constructs it (`Image`, `Entrypoint`, `Args`, `Env`, `ActionSourceDir`, `ActionPathInContainer`, `FilesDir`) and how `executor.go` fills in `Env`/`FilesDir` after `prepareUsesStep` returns. `usesStepPlan`'s shape (Task 5) is used consistently by both `prepareUsesStep`'s three return sites (node/docker/raw-docker) and `RunJob`'s branch.
- **Known simplifications carried over from the design spec, restated at point of use so an implementer doesn't need to re-derive them:** `with.args`/`with.entrypoint` split via `strings.Fields`, not real shellwords (Task 5); `runs.args` from `action.yml` itself is never expression-substituted, only `with.args` is (Task 5, via the existing generic `with:` substitution loop) — both called out inline in Task 5's implementation and in Global Constraints.
