# Services and Container Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement job-level `container:` (swap the job's own container image) and `services:` (sidecar containers reachable by name), including registry `credentials:` for both.

**Architecture:** `internal/engine/workflow.go` parses `container:`/`services:` into a new `ContainerSpec` type; `internal/engine/executor.go` resolves and validates credentials, then hands fully-resolved specs to a new `runner.ContainerSpec`-typed `Backend.StartJob` signature; `internal/runner/docker_backend.go` does the actual `docker network create`/`docker run`/`docker login` work via the existing shell-out-to-`docker`-CLI pattern (no Docker Go SDK, matching this project's zero-external-Go-dependency constraint).

**Tech Stack:** Go 1.27 stdlib only, `docker` CLI via `os/exec` (existing pattern, no new dependency), `gopkg.in/yaml.v3` (already vendored under `third_party/`, matches existing YAML handling in `workflow.go`).

**Spec:** `docs/design/specs/2026-09-14-mirror-gha-design.md`, "Services and Container Runtime" section.

## Global Constraints

- Zero external Go dependencies — everything shells out to the `docker` CLI via `os/exec`, same as every existing backend call.
- No Docker Go SDK, no protobuf, no shlex package — hand-roll the small `Options` tokenizer.
- Registry passwords are piped via stdin to `docker login` (`--password-stdin`), never passed as a `-p`/`--password` argument — required so the password never appears in process args or shell history.
- Jobs with no `container:`/`services:` must be completely unaffected — no new network, no behavior change, same as today.
- Every existing test must keep passing; interface-signature changes must update every call site in the same task that introduces the change (never leave the tree non-compiling between tasks).

---

### Task 1: `ContainerSpec` type and `Job.Container()` parsing

**Files:**
- Modify: `internal/engine/workflow.go`
- Test: `internal/engine/workflow_test.go` (create if it doesn't already cover `Job`/`Container()` — check first; if a `workflow_test.go` already exists, add to it)

**Interfaces:**
- Produces: `type ContainerSpec struct { Image string; Env map[string]string; Ports []string; Volumes []string; Options string; Credentials map[string]string }` (engine package). `func (j *Job) Container() (*ContainerSpec, error)`.

- [ ] **Step 1: Check for an existing workflow_test.go**

Run: `ls internal/engine/workflow_test.go 2>&1`

If it exists, read it first so Step 2's test additions match its existing style (imports, helper usage). If it doesn't exist, Step 2 creates it fresh.

- [ ] **Step 2: Write the failing tests**

Add to `internal/engine/workflow_test.go` (create the file with `package engine` + `import "testing"` if it doesn't exist yet):

```go
func TestJob_Container_Absent(t *testing.T) {
	job := &Job{}
	spec, err := job.Container()
	if err != nil {
		t.Fatalf("Container() error = %v", err)
	}
	if spec != nil {
		t.Errorf("Container() = %v, want nil for a job with no container: field", spec)
	}
}

func TestJob_Container_BareImageString(t *testing.T) {
	yamlDoc := `
runs-on: ubuntu-latest
container: node:20
steps: []
`
	wf, err := Parse([]byte("name: t\non: push\njobs:\n  build:\n" + indentLines(yamlDoc, "    ")))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]
	spec, err := job.Container()
	if err != nil {
		t.Fatalf("Container() error = %v", err)
	}
	if spec == nil || spec.Image != "node:20" {
		t.Fatalf("Container() = %+v, want Image=node:20", spec)
	}
}

func TestJob_Container_Mapping(t *testing.T) {
	yamlDoc := `
runs-on: ubuntu-latest
container:
  image: node:20
  env:
    FOO: bar
  ports:
    - "8080:8080"
  volumes:
    - "/host/path:/container/path"
  options: "--cpus 2"
  credentials:
    username: myuser
    password: mypass
steps: []
`
	wf, err := Parse([]byte("name: t\non: push\njobs:\n  build:\n" + indentLines(yamlDoc, "    ")))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]
	spec, err := job.Container()
	if err != nil {
		t.Fatalf("Container() error = %v", err)
	}
	if spec == nil {
		t.Fatal("Container() = nil, want a resolved spec")
	}
	if spec.Image != "node:20" {
		t.Errorf("Image = %q, want node:20", spec.Image)
	}
	if spec.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO] = %q, want bar", spec.Env["FOO"])
	}
	if len(spec.Ports) != 1 || spec.Ports[0] != "8080:8080" {
		t.Errorf("Ports = %v, want [8080:8080]", spec.Ports)
	}
	if len(spec.Volumes) != 1 || spec.Volumes[0] != "/host/path:/container/path" {
		t.Errorf("Volumes = %v, want [/host/path:/container/path]", spec.Volumes)
	}
	if spec.Options != "--cpus 2" {
		t.Errorf("Options = %q, want --cpus 2", spec.Options)
	}
	if spec.Credentials["username"] != "myuser" || spec.Credentials["password"] != "mypass" {
		t.Errorf("Credentials = %v, want username=myuser password=mypass", spec.Credentials)
	}
}

// indentLines prefixes every non-empty line of s with prefix — a small
// test helper to embed a multi-line YAML snippet under a jobs: key.
func indentLines(s, prefix string) string {
	lines := splitLinesKeepEmpty(s)
	var b strings.Builder
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(prefix + l + "\n")
	}
	return b.String()
}

func splitLinesKeepEmpty(s string) []string {
	return strings.Split(strings.Trim(s, "\n"), "\n")
}
```

Add `"strings"` to the test file's imports.

- [ ] **Step 3: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestJob_Container -v`
Expected: FAIL — `job.Container undefined` (method doesn't exist yet).

- [ ] **Step 4: Implement `ContainerSpec` and `Job.Container()`**

In `internal/engine/workflow.go`, add after the `Strategy` type (after the closing `}` of the `Strategy` struct, before `RunDefaults`):

```go
// ContainerSpec is a job's `container:` field, or one entry of its
// `services:` map — GitHub Actions reuses the same shape for both.
type ContainerSpec struct {
	Image       string            `yaml:"image"`
	Env         map[string]string `yaml:"env"`
	Ports       []string          `yaml:"ports"`
	Volumes     []string          `yaml:"volumes"`
	Options     string            `yaml:"options"`
	Credentials map[string]string `yaml:"credentials"`
}
```

In the `Job` struct, add two fields (after `Defaults`):

```go
	RawContainer yaml.Node                `yaml:"container"`
	Services     map[string]ContainerSpec `yaml:"services"`
```

After the `Job` struct's closing `}`, add:

```go
// Container resolves the job's `container:` field, which GitHub Actions
// allows as either a bare image string or a mapping. Returns nil, nil
// when the job has no container: field at all.
func (j *Job) Container() (*ContainerSpec, error) {
	switch j.RawContainer.Kind {
	case 0:
		return nil, nil
	case yaml.ScalarNode:
		var image string
		if err := j.RawContainer.Decode(&image); err != nil {
			return nil, fmt.Errorf("container: %w", err)
		}
		return &ContainerSpec{Image: image}, nil
	case yaml.MappingNode:
		spec := &ContainerSpec{}
		if err := j.RawContainer.Decode(spec); err != nil {
			return nil, fmt.Errorf("container: %w", err)
		}
		return spec, nil
	default:
		return nil, fmt.Errorf("container: must be a string or a mapping")
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestJob_Container -v`
Expected: PASS (all three tests).

- [ ] **Step 6: Run the full engine test suite to check for regressions**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... 2>&1 | tail -20`
Expected: all packages still `ok`.

- [ ] **Step 7: Commit**

```bash
git add internal/engine/workflow.go internal/engine/workflow_test.go
git commit -m "feat(engine): parse job container:/services: fields"
```

---

### Task 2: Credentials validation and registry-host parsing

**Files:**
- Create: `internal/engine/credentials.go`
- Test: `internal/engine/credentials_test.go`

**Interfaces:**
- Consumes: nothing new (pure functions over strings/maps).
- Produces: `func validateCredentials(creds map[string]string) (username, password string, err error)`. `func registryHostFor(image string) string`. Both will be called from Task 7's `toRunnerContainerSpec` helper.

- [ ] **Step 1: Write the failing tests**

Create `internal/engine/credentials_test.go`:

```go
package engine

import "testing"

func TestValidateCredentials_Nil(t *testing.T) {
	username, password, err := validateCredentials(nil)
	if err != nil {
		t.Fatalf("validateCredentials(nil) error = %v", err)
	}
	if username != "" || password != "" {
		t.Errorf("validateCredentials(nil) = (%q, %q), want (\"\", \"\")", username, password)
	}
}

func TestValidateCredentials_Valid(t *testing.T) {
	username, password, err := validateCredentials(map[string]string{
		"username": "myuser",
		"password": "mypass",
	})
	if err != nil {
		t.Fatalf("validateCredentials() error = %v", err)
	}
	if username != "myuser" || password != "mypass" {
		t.Errorf("validateCredentials() = (%q, %q), want (myuser, mypass)", username, password)
	}
}

func TestValidateCredentials_MissingKey(t *testing.T) {
	_, _, err := validateCredentials(map[string]string{"username": "myuser"})
	if err == nil {
		t.Fatal("validateCredentials() error = nil, want error for missing password key")
	}
}

func TestValidateCredentials_ExtraKey(t *testing.T) {
	_, _, err := validateCredentials(map[string]string{
		"username": "myuser",
		"password": "mypass",
		"registry": "ghcr.io",
	})
	if err == nil {
		t.Fatal("validateCredentials() error = nil, want error for an extra key")
	}
}

func TestValidateCredentials_EmptyValue(t *testing.T) {
	_, _, err := validateCredentials(map[string]string{"username": "", "password": "mypass"})
	if err == nil {
		t.Fatal("validateCredentials() error = nil, want error for an empty username")
	}
}

func TestRegistryHostFor(t *testing.T) {
	cases := map[string]string{
		"node:20":                      "index.docker.io",
		"myuser/myimage:latest":        "index.docker.io",
		"ghcr.io/owner/image:tag":      "ghcr.io",
		"localhost:5000/image":         "localhost:5000",
		"my.registry.example.com/image": "my.registry.example.com",
	}
	for image, want := range cases {
		got := registryHostFor(image)
		if got != want {
			t.Errorf("registryHostFor(%q) = %q, want %q", image, got, want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run "TestValidateCredentials|TestRegistryHostFor" -v`
Expected: FAIL — `validateCredentials undefined`, `registryHostFor undefined`.

- [ ] **Step 3: Implement**

Create `internal/engine/credentials.go`:

```go
package engine

import (
	"fmt"
	"strings"
)

// validateCredentials checks a container/service's `credentials:` map has
// exactly the two keys GitHub Actions requires — "username" and
// "password" — matching act's own validation
// (pkg/runner/run_context.go's handleCredentials/handleServiceCredentials
// both reject anything other than exactly 2 keys). A nil map (no
// credentials: field at all) is not an error — it just means no registry
// auth is needed for this image.
func validateCredentials(creds map[string]string) (username, password string, err error) {
	if creds == nil {
		return "", "", nil
	}
	if len(creds) != 2 {
		return "", "", fmt.Errorf("credentials must have exactly \"username\" and \"password\" keys")
	}
	username, ok := creds["username"]
	if !ok || username == "" {
		return "", "", fmt.Errorf("credentials must have exactly \"username\" and \"password\" keys")
	}
	password, ok = creds["password"]
	if !ok || password == "" {
		return "", "", fmt.Errorf("credentials must have exactly \"username\" and \"password\" keys")
	}
	return username, password, nil
}

// registryHostFor parses the registry host out of an image reference,
// matching act's/Docker's own default-registry heuristic: the reference's
// first "/"-separated segment is the registry host only if it looks like
// one (contains a "." or ":", or is exactly "localhost") — otherwise the
// whole reference is a Docker Hub image (e.g. "node:20" or
// "myuser/myimage:latest") and the registry defaults to Docker Hub's
// canonical host, index.docker.io.
func registryHostFor(image string) string {
	const dockerHub = "index.docker.io"
	first, rest, found := strings.Cut(image, "/")
	if !found {
		return dockerHub
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	_ = rest
	return dockerHub
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run "TestValidateCredentials|TestRegistryHostFor" -v`
Expected: PASS (all 6 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/engine/credentials.go internal/engine/credentials_test.go
git commit -m "feat(engine): validate container credentials, parse registry host"
```

---

### Task 3: `Options` quote-aware tokenizer

**Files:**
- Create: `internal/runner/dockeropts.go`
- Test: `internal/runner/dockeropts_test.go`

**Interfaces:**
- Produces: `func splitDockerOptions(s string) ([]string, error)`. Used by Task 5/6's `docker_backend.go` to turn a `ContainerSpec.Options` string into extra `docker run`/`docker create` args.

- [ ] **Step 1: Write the failing tests**

Create `internal/runner/dockeropts_test.go`:

```go
package runner

import (
	"reflect"
	"testing"
)

func TestSplitDockerOptions_Empty(t *testing.T) {
	got, err := splitDockerOptions("")
	if err != nil {
		t.Fatalf("splitDockerOptions(\"\") error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("splitDockerOptions(\"\") = %v, want empty", got)
	}
}

func TestSplitDockerOptions_WhitespaceOnly(t *testing.T) {
	got, err := splitDockerOptions("   \t  ")
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("splitDockerOptions() = %v, want empty", got)
	}
}

func TestSplitDockerOptions_SingleToken(t *testing.T) {
	got, err := splitDockerOptions("--cpus")
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	want := []string{"--cpus"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitDockerOptions() = %v, want %v", got, want)
	}
}

func TestSplitDockerOptions_MultipleTokens(t *testing.T) {
	got, err := splitDockerOptions("--cpus 2 --memory 512m")
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	want := []string{"--cpus", "2", "--memory", "512m"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitDockerOptions() = %v, want %v", got, want)
	}
}

func TestSplitDockerOptions_DoubleQuotedWithSpace(t *testing.T) {
	got, err := splitDockerOptions(`--health-cmd "pg_isready -U postgres" --health-interval 2s`)
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	want := []string{"--health-cmd", "pg_isready -U postgres", "--health-interval", "2s"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitDockerOptions() = %v, want %v", got, want)
	}
}

func TestSplitDockerOptions_SingleQuoted(t *testing.T) {
	got, err := splitDockerOptions(`--label 'a value with spaces'`)
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	want := []string{"--label", "a value with spaces"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitDockerOptions() = %v, want %v", got, want)
	}
}

func TestSplitDockerOptions_AdjacentQuoteNoSpace(t *testing.T) {
	// A quote can start mid-token — everything from the quote character
	// onward (up to the matching close quote) is treated as part of that
	// same token, with the quote characters themselves stripped.
	got, err := splitDockerOptions(`--foo"bar baz"`)
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	want := []string{"--foobar baz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitDockerOptions() = %v, want %v", got, want)
	}
}

func TestSplitDockerOptions_UnterminatedQuote(t *testing.T) {
	_, err := splitDockerOptions(`--health-cmd "pg_isready`)
	if err == nil {
		t.Fatal("splitDockerOptions() error = nil, want error for unterminated quote")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/runner/... -run TestSplitDockerOptions -v`
Expected: FAIL — `splitDockerOptions undefined`.

- [ ] **Step 3: Implement**

Create `internal/runner/dockeropts.go`:

```go
package runner

import "fmt"

// splitDockerOptions tokenizes a ContainerSpec.Options string into
// individual docker CLI arguments. It's a hand-rolled, deliberately
// simple scanner — not a full shell-word tokenizer — because the only
// real-world need is whitespace-separated tokens with occasional quoted
// substrings that themselves contain spaces (e.g.
// `--health-cmd "pg_isready -U postgres"`). No escape-sequence support:
// GitHub Actions' own `options:` field doesn't document any either.
func splitDockerOptions(s string) ([]string, error) {
	var tokens []string
	var current []rune
	inToken := false
	var quote rune // 0 when not inside a quoted region

	flush := func() {
		if inToken {
			tokens = append(tokens, string(current))
			current = nil
			inToken = false
		}
	}

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current = append(current, r)
			continue
		}
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		case r == '"' || r == '\'':
			quote = r
			inToken = true
		default:
			inToken = true
			current = append(current, r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in options: %q", s)
	}
	flush()
	return tokens, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/runner/... -run TestSplitDockerOptions -v`
Expected: PASS (all 8 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/runner/dockeropts.go internal/runner/dockeropts_test.go
git commit -m "feat(runner): add quote-aware tokenizer for container options"
```

---

### Task 4: `runner.ContainerSpec` and `Backend.StartJob` signature change (interface only, no behavior yet)

This task changes the interface and fixes every call site to compile —
it deliberately does NOT change `LinuxDockerBackend.StartJob`'s actual
behavior yet (that's Tasks 5 and 6). Keeping this as its own task means
the tree compiles and all tests pass before any new Docker-invocation
logic is introduced, so a mistake in Task 5/6 is easy to isolate.

**Files:**
- Modify: `internal/runner/backend.go`
- Modify: `internal/runner/docker_backend.go:42` (signature line only)
- Modify: `internal/runner/docker_backend_test.go` (11 call sites)
- Modify: `internal/engine/composite_step_test.go:15` (1 call site)
- Modify: `internal/engine/executor_test.go:17` (fake `StartJob` method definition)

**Interfaces:**
- Consumes: nothing new.
- Produces: `type ContainerSpec struct { Image string; Env map[string]string; Ports, Volumes []string; Options string; Username, Password string }` (runner package — note `Username`/`Password` are already-resolved strings, not a `Credentials` map; Task 7 is the only caller that resolves the map into these two fields). New `Backend.StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *ContainerSpec, services map[string]ContainerSpec) (Job, error)` signature — every later task's `docker_backend.go` work builds on this exact signature.

- [ ] **Step 1: Update the `Backend` interface**

In `internal/runner/backend.go`, add after `StepResult` (or anywhere before the `Backend` interface — the `Job`/`DockerActionSpec` structs are unaffected):

```go
// ContainerSpec is one job's `container:` override, or one entry of its
// `services:` map — the runner package's own copy, decoupled from
// internal/engine's identically-shaped type the same way DockerActionSpec
// already is: this package receives fully-resolved values (Username/
// Password already validated and extracted from a raw credentials map),
// not YAML-shaped or credential-validation concerns.
type ContainerSpec struct {
	Image    string
	Env      map[string]string
	Ports    []string
	Volumes  []string
	Options  string
	Username string
	Password string
}
```

Change the `Backend` interface's `StartJob` method:

```go
type Backend interface {
	// StartJob starts the environment, bind-mounting hostWorkspaceDir so
	// its contents are visible at Job.WorkspacePath() from inside it.
	// jobID names the job this environment belongs to (used to name the
	// per-job Docker network created when services is non-empty — safe to
	// pass an arbitrary non-empty string in tests that don't exercise
	// services). containerSpec overrides the runner image when non-nil.
	// services, when non-empty, starts one sidecar container per entry,
	// reachable from the job's own environment by its map key as hostname.
	StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *ContainerSpec, services map[string]ContainerSpec) (Job, error)
}
```

- [ ] **Step 2: Update `LinuxDockerBackend.StartJob`'s signature (body unchanged for now)**

In `internal/runner/docker_backend.go`, change only the function signature line (line 42):

```go
func (b *LinuxDockerBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *ContainerSpec, services map[string]ContainerSpec) (Job, error) {
```

Leave the function body exactly as-is for this task (it doesn't reference `jobID`/`containerSpec`/`services` yet — that's fine, Go allows unused function parameters).

- [ ] **Step 3: Attempt to build to find every broken call site**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... 2>&1`
Expected: compile errors at every existing call site (docker_backend_test.go, composite_step_test.go, executor_test.go) — this is the authoritative list, use it to check nothing is missed in the next step.

- [ ] **Step 4: Fix every real-backend call site**

In `internal/runner/docker_backend_test.go`, change every occurrence of `backend.StartJob(context.Background(), <workspaceArg>)` to `backend.StartJob(context.Background(), "test-job", <workspaceArg>, nil, nil)` — 11 occurrences, at (pre-existing) lines 32, 59, 87, 119, 138, 163, 190, 205, 257, 283, 307. Each one keeps its original second argument (`t.TempDir()`, `""`, `hostWorkspace`, or `workspaceDir`) as the new third argument, unchanged; only the leading `"test-job", ` and trailing `, nil, nil` are new.

In `internal/engine/composite_step_test.go:15`, change:
```go
	job, err := backend.StartJob(context.Background(), t.TempDir())
```
to:
```go
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
```

- [ ] **Step 5: Fix the fake backend in executor_test.go**

In `internal/engine/executor_test.go`, change:
```go
func (f *fakeBackend) StartJob(ctx context.Context, hostWorkspaceDir string) (runner.Job, error) {
```
to:
```go
func (f *fakeBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *runner.ContainerSpec, services map[string]runner.ContainerSpec) (runner.Job, error) {
```
(the body is unchanged — it doesn't need to inspect the new parameters for any existing test).

- [ ] **Step 6: Build and run the full test suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && go test ./... 2>&1 | tail -20`
Expected: everything compiles, every existing test still passes (this task changed no runtime behavior, only the interface shape).

- [ ] **Step 7: Commit**

```bash
git add internal/runner/backend.go internal/runner/docker_backend.go internal/runner/docker_backend_test.go internal/engine/composite_step_test.go internal/engine/executor_test.go
git commit -m "refactor(runner): thread jobID/containerSpec/services through Backend.StartJob"
```

---

### Task 5: Job container image swap, env/ports/volumes/options, and registry login for the job's own container

**Files:**
- Modify: `internal/runner/docker_backend.go`
- Test: `internal/runner/docker_backend_test.go`

**Interfaces:**
- Consumes: `runner.ContainerSpec` (Task 4), `splitDockerOptions` (Task 3).
- Produces: `func dockerRegistryLogin(ctx context.Context, registry, username, password string) error`. `func dockerRegistryLogout(ctx context.Context, registry string) error`. `func registryHostFor(image string) string` (runner package's own copy — see rationale below). `dockerJob` struct gains `registryLogouts []string`. These are consumed by Task 6's service-container logic and by `dockerJob.Stop`.

- [ ] **Step 1: Write the failing tests**

Add to `internal/runner/docker_backend_test.go`:

```go
func TestLinuxDockerBackend_StartJob_ContainerImageSwap(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), &ContainerSpec{Image: "alpine:3.19"}, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "cat /etc/os-release",
		Shell:    "sh",
		Env:      map[string]string{},
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if !strings.Contains(result.Stdout, "Alpine") {
		t.Errorf("Stdout = %q, want it to identify as Alpine (proving the image swap took effect, not the default ubuntu:22.04)", result.Stdout)
	}
}

func TestLinuxDockerBackend_StartJob_ContainerEnvAndOptions(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), &ContainerSpec{
		Options: `--label "mirror-test=yes"`,
	}, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	dj := job.(*dockerJob)
	cmd := exec.CommandContext(context.Background(), "docker", "inspect", "--format", "{{index .Config.Labels \"mirror-test\"}}", dj.containerID)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker inspect error = %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "yes" {
		t.Errorf("label mirror-test = %q, want yes (proving Options reached docker run)", strings.TrimSpace(string(out)))
	}
}

func TestRegistryHostFor_RunnerPackage(t *testing.T) {
	cases := map[string]string{
		"node:20":                 "index.docker.io",
		"ghcr.io/owner/image:tag": "ghcr.io",
	}
	for image, want := range cases {
		got := registryHostFor(image)
		if got != want {
			t.Errorf("registryHostFor(%q) = %q, want %q", image, got, want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/runner/... -run "TestLinuxDockerBackend_StartJob_Container|TestRegistryHostFor_RunnerPackage" -v`
Expected: FAIL — image swap test fails because `StartJob` still hardcodes `b.image`; options test fails because no label is set; `registryHostFor` undefined.

- [ ] **Step 3: Implement registry login/logout helpers and `registryHostFor`**

Add to `internal/runner/docker_backend.go` (near the top, after the existing `const` block):

```go
// registryHostFor parses the registry host out of an image reference —
// duplicated from internal/engine's identical helper rather than
// exported/cross-imported, matching this project's established
// precedent (requireDocker/requireNetwork are also duplicated per
// package) of keeping these packages decoupled.
func registryHostFor(image string) string {
	const dockerHub = "index.docker.io"
	first, _, found := strings.Cut(image, "/")
	if !found {
		return dockerHub
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	return dockerHub
}

// dockerRegistryLogin authenticates docker's local credential store to
// registry so a subsequent docker pull/run against a private image
// succeeds. password is piped via stdin (--password-stdin), never passed
// as a -p/--password argument, so it never appears in process args or
// shell history.
func dockerRegistryLogin(ctx context.Context, registry, username, password string) error {
	cmd := exec.CommandContext(ctx, "docker", "login", registry, "-u", username, "--password-stdin")
	cmd.Stdin = strings.NewReader(password)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker login %s: %w: %s", registry, err, stderr.String())
	}
	return nil
}

// dockerRegistryLogout is best-effort cleanup, called from dockerJob.Stop.
func dockerRegistryLogout(ctx context.Context, registry string) error {
	cmd := exec.CommandContext(ctx, "docker", "logout", registry)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker logout %s: %w: %s", registry, err, stderr.String())
	}
	return nil
}
```

- [ ] **Step 4: Implement the job-container image swap, env/ports/volumes/options, and login**

Replace `LinuxDockerBackend.StartJob`'s body in `internal/runner/docker_backend.go` with:

```go
func (b *LinuxDockerBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *ContainerSpec, services map[string]ContainerSpec) (Job, error) {
	if hostWorkspaceDir == "" {
		return nil, fmt.Errorf("hostWorkspaceDir must not be empty")
	}

	hostFilesRoot, err := os.MkdirTemp("", "mirror-job-")
	if err != nil {
		return nil, fmt.Errorf("create job files root: %w", err)
	}

	image := b.image
	var registryLogouts []string
	if containerSpec != nil {
		if containerSpec.Image != "" {
			image = containerSpec.Image
		}
		if containerSpec.Username != "" {
			registry := registryHostFor(image)
			if err := dockerRegistryLogin(ctx, registry, containerSpec.Username, containerSpec.Password); err != nil {
				os.RemoveAll(hostFilesRoot)
				return nil, err
			}
			registryLogouts = append(registryLogouts, registry)
		}
	}

	args := []string{"run", "-d", "--rm",
		"-v", hostFilesRoot + ":" + containerFilesMount,
		"-v", hostWorkspaceDir + ":" + containerWorkspaceMount,
	}
	if containerSpec != nil {
		for k, v := range containerSpec.Env {
			args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
		}
		for _, p := range containerSpec.Ports {
			args = append(args, "-p", p)
		}
		for _, v := range containerSpec.Volumes {
			args = append(args, "-v", v)
		}
		if containerSpec.Options != "" {
			opts, err := splitDockerOptions(containerSpec.Options)
			if err != nil {
				os.RemoveAll(hostFilesRoot)
				return nil, err
			}
			args = append(args, opts...)
		}
	}
	args = append(args, image, "sleep", "infinity")

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(hostFilesRoot)
		return nil, fmt.Errorf("start job container: %w: %s", err, stderr.String())
	}

	return &dockerJob{
		containerID:      strings.TrimSpace(stdout.String()),
		hostFilesRoot:    hostFilesRoot,
		hostWorkspaceDir: hostWorkspaceDir,
		registryLogouts:  registryLogouts,
	}, nil
}
```

Note: `jobID` and `services` are intentionally unused by this task's body — Task 6 adds the network/service logic that uses them. Add `_ = jobID` and `_ = services` is NOT needed in Go (unused function parameters never cause a compile error, only unused local variables/imports do) — leave them as-is.

Add `registryLogouts []string` to the `dockerJob` struct:

```go
type dockerJob struct {
	containerID      string
	hostFilesRoot    string
	hostWorkspaceDir string
	registryLogouts  []string
}
```

- [ ] **Step 5: Update `dockerJob.Stop` to log out of any registries**

Replace `dockerJob.Stop`'s body in `internal/runner/docker_backend.go`:

```go
func (j *dockerJob) Stop(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", j.containerID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stopErr := cmd.Run()
	if stopErr != nil {
		stopErr = fmt.Errorf("stop job container %s: %w: %s", j.containerID, stopErr, stderr.String())
	}

	for _, registry := range j.registryLogouts {
		dockerRegistryLogout(ctx, registry) // best-effort, error intentionally ignored
	}

	// Always attempt cleanup of the host-side files root, even if the
	// container removal above failed — it's a plain temp directory, not
	// something Docker knows about, and leaving it behind on every job
	// run silently accumulates in the OS temp directory forever. Note:
	// the workspace mount is the caller's own directory (e.g. their repo)
	// and must never be removed here.
	if err := os.RemoveAll(j.hostFilesRoot); err != nil && stopErr == nil {
		return fmt.Errorf("remove job files root %s: %w", j.hostFilesRoot, err)
	}
	return stopErr
}
```

- [ ] **Step 6: Run the new tests**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/runner/... -run "TestLinuxDockerBackend_StartJob_Container|TestRegistryHostFor_RunnerPackage" -v`
Expected: PASS (all 3 tests).

- [ ] **Step 7: Run the full test suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && go test ./... 2>&1 | tail -20`
Expected: everything still passes — this task didn't touch the no-`container:` code path's behavior (image still defaults to `b.image`, no login attempted when `containerSpec` is nil or has no `Username`).

- [ ] **Step 8: Commit**

```bash
git add internal/runner/docker_backend.go internal/runner/docker_backend_test.go
git commit -m "feat(runner): job container image swap, env/ports/volumes/options, registry login"
```

---

### Task 6: `services:` — per-job network, sidecar containers, health wait, teardown

**Files:**
- Modify: `internal/runner/docker_backend.go`
- Test: `internal/runner/docker_backend_test.go`

**Interfaces:**
- Consumes: `registryHostFor`/`dockerRegistryLogin`/`dockerRegistryLogout`/`splitDockerOptions` (Tasks 3 and 5).
- Produces: `func waitForServiceHealth(ctx context.Context, containerID string, timeout time.Duration) error`. `dockerJob` struct gains `networkName string`, `serviceContainerIDs []string`. Consumed by Task 8's real end-to-end verification.

- [ ] **Step 1: Write the failing test**

Add to `internal/runner/docker_backend_test.go`:

```go
func TestLinuxDockerBackend_StartJob_ServiceReachableByAlias(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), "svc-test-job", t.TempDir(), nil, map[string]ContainerSpec{
		"echoserver": {Image: "alpine:3.19"},
	})
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	// alpine has no long-running server, but DNS resolution alone proves
	// the network-alias wiring works — getent hosts resolves via the
	// container's own /etc/resolv.conf + Docker's embedded DNS.
	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "getent hosts echoserver",
		Shell:    "sh",
		Env:      map[string]string{},
		FilesDir: mkStepDir(t, job.FilesRoot()),
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("getent hosts echoserver exit = %d, want 0 (service should resolve by name); stderr: %s", result.ExitCode, result.Stderr)
	}

	dj := job.(*dockerJob)
	if dj.networkName == "" {
		t.Error("networkName is empty, want a created per-job network")
	}
	if len(dj.serviceContainerIDs) != 1 {
		t.Errorf("serviceContainerIDs = %v, want exactly 1", dj.serviceContainerIDs)
	}
}

func TestLinuxDockerBackend_StartJob_ServiceTeardownCleansUpNetwork(t *testing.T) {
	requireDocker(t)

	backend := NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), "svc-teardown-job", t.TempDir(), nil, map[string]ContainerSpec{
		"echoserver": {Image: "alpine:3.19"},
	})
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	dj := job.(*dockerJob)
	networkName := dj.networkName
	serviceID := dj.serviceContainerIDs[0]

	if err := job.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	inspectNet := exec.CommandContext(context.Background(), "docker", "network", "inspect", networkName)
	if err := inspectNet.Run(); err == nil {
		t.Errorf("network %s still exists after Stop()", networkName)
	}

	inspectSvc := exec.CommandContext(context.Background(), "docker", "inspect", serviceID)
	if err := inspectSvc.Run(); err == nil {
		t.Errorf("service container %s still exists after Stop()", serviceID)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/runner/... -run TestLinuxDockerBackend_StartJob_Service -v`
Expected: FAIL — `getent hosts echoserver` fails (no network/service exists yet), `dj.networkName`/`serviceContainerIDs` are zero values.

- [ ] **Step 3: Implement `waitForServiceHealth`**

Add to `internal/runner/docker_backend.go`:

```go
// waitForServiceHealth polls containerID's Docker healthcheck status.
// A container with no HEALTHCHECK defined reports an empty Health.Status
// (docker inspect's format returns "" for a nonexistent field path) —
// treated as ready immediately, matching act's own behavior of not
// blocking forever on a service that never declared a healthcheck.
func waitForServiceHealth(ctx context.Context, containerID string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		cmd := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.State.Health.Status}}", containerID)
		out, err := cmd.CombinedOutput()
		status := strings.TrimSpace(string(out))
		if err != nil || status == "<no value>" || status == "" {
			return nil
		}
		if status == "healthy" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("service container %s did not become healthy within %s (last status: %s)", containerID, timeout, status)
		}
		time.Sleep(time.Second)
	}
}
```

Add `"time"` to the file's imports.

- [ ] **Step 4: Implement network creation, service startup, and job-container network attachment**

Replace `LinuxDockerBackend.StartJob`'s body again (extending Task 5's version) in `internal/runner/docker_backend.go`:

```go
func (b *LinuxDockerBackend) StartJob(ctx context.Context, jobID string, hostWorkspaceDir string, containerSpec *ContainerSpec, services map[string]ContainerSpec) (Job, error) {
	if hostWorkspaceDir == "" {
		return nil, fmt.Errorf("hostWorkspaceDir must not be empty")
	}

	hostFilesRoot, err := os.MkdirTemp("", "mirror-job-")
	if err != nil {
		return nil, fmt.Errorf("create job files root: %w", err)
	}

	image := b.image
	var registryLogouts []string
	if containerSpec != nil {
		if containerSpec.Image != "" {
			image = containerSpec.Image
		}
		if containerSpec.Username != "" {
			registry := registryHostFor(image)
			if err := dockerRegistryLogin(ctx, registry, containerSpec.Username, containerSpec.Password); err != nil {
				os.RemoveAll(hostFilesRoot)
				return nil, err
			}
			registryLogouts = append(registryLogouts, registry)
		}
	}

	var networkName string
	var serviceContainerIDs []string
	if len(services) > 0 {
		networkName = sanitizeDockerName("mirror-svc-" + jobID)
		createNet := exec.CommandContext(ctx, "docker", "network", "create", networkName)
		var netStderr bytes.Buffer
		createNet.Stderr = &netStderr
		if err := createNet.Run(); err != nil && !strings.Contains(netStderr.String(), "already exists") {
			os.RemoveAll(hostFilesRoot)
			return nil, fmt.Errorf("create network %s: %w: %s", networkName, err, netStderr.String())
		}

		names := make([]string, 0, len(services))
		for name := range services {
			names = append(names, name)
		}
		sort.Strings(names)

		for _, name := range names {
			spec := services[name]
			if spec.Username != "" {
				registry := registryHostFor(spec.Image)
				if err := dockerRegistryLogin(ctx, registry, spec.Username, spec.Password); err != nil {
					return nil, err
				}
				registryLogouts = append(registryLogouts, registry)
			}

			svcArgs := []string{"run", "-d", "--rm",
				"--network", networkName,
				"--network-alias", name,
			}
			for k, v := range spec.Env {
				svcArgs = append(svcArgs, "-e", fmt.Sprintf("%s=%s", k, v))
			}
			for _, p := range spec.Ports {
				svcArgs = append(svcArgs, "-p", p)
			}
			for _, v := range spec.Volumes {
				svcArgs = append(svcArgs, "-v", v)
			}
			if spec.Options != "" {
				opts, err := splitDockerOptions(spec.Options)
				if err != nil {
					return nil, err
				}
				svcArgs = append(svcArgs, opts...)
			}
			svcArgs = append(svcArgs, spec.Image)

			svcCmd := exec.CommandContext(ctx, "docker", svcArgs...)
			var svcStdout, svcStderr bytes.Buffer
			svcCmd.Stdout = &svcStdout
			svcCmd.Stderr = &svcStderr
			if err := svcCmd.Run(); err != nil {
				return nil, fmt.Errorf("start service %s: %w: %s", name, err, svcStderr.String())
			}
			serviceID := strings.TrimSpace(svcStdout.String())
			serviceContainerIDs = append(serviceContainerIDs, serviceID)

			if err := waitForServiceHealth(ctx, serviceID, 5*time.Minute); err != nil {
				return nil, fmt.Errorf("service %s: %w", name, err)
			}
		}
	}

	args := []string{"run", "-d", "--rm",
		"-v", hostFilesRoot + ":" + containerFilesMount,
		"-v", hostWorkspaceDir + ":" + containerWorkspaceMount,
	}
	if networkName != "" {
		args = append(args, "--network", networkName)
	}
	if containerSpec != nil {
		for k, v := range containerSpec.Env {
			args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
		}
		for _, p := range containerSpec.Ports {
			args = append(args, "-p", p)
		}
		for _, v := range containerSpec.Volumes {
			args = append(args, "-v", v)
		}
		if containerSpec.Options != "" {
			opts, err := splitDockerOptions(containerSpec.Options)
			if err != nil {
				os.RemoveAll(hostFilesRoot)
				return nil, err
			}
			args = append(args, opts...)
		}
	}
	args = append(args, image, "sleep", "infinity")

	cmd := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(hostFilesRoot)
		return nil, fmt.Errorf("start job container: %w: %s", err, stderr.String())
	}

	return &dockerJob{
		containerID:         strings.TrimSpace(stdout.String()),
		hostFilesRoot:       hostFilesRoot,
		hostWorkspaceDir:    hostWorkspaceDir,
		registryLogouts:     registryLogouts,
		networkName:         networkName,
		serviceContainerIDs: serviceContainerIDs,
	}, nil
}

// sanitizeDockerName makes s safe to use as a Docker network/container
// name — lowercase, with anything other than [a-z0-9-] replaced by "-".
func sanitizeDockerName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}
```

Add `"sort"` to the file's imports.

Add `networkName string` and `serviceContainerIDs []string` to the `dockerJob` struct:

```go
type dockerJob struct {
	containerID         string
	hostFilesRoot       string
	hostWorkspaceDir    string
	registryLogouts     []string
	networkName         string
	serviceContainerIDs []string
}
```

- [ ] **Step 5: Update `dockerJob.Stop` to tear down services and the network**

Replace `dockerJob.Stop`'s body again in `internal/runner/docker_backend.go`:

```go
func (j *dockerJob) Stop(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", j.containerID)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stopErr := cmd.Run()
	if stopErr != nil {
		stopErr = fmt.Errorf("stop job container %s: %w: %s", j.containerID, stopErr, stderr.String())
	}

	for _, serviceID := range j.serviceContainerIDs {
		exec.CommandContext(ctx, "docker", "rm", "-f", serviceID).Run() // best-effort
	}

	if j.networkName != "" {
		exec.CommandContext(ctx, "docker", "network", "rm", j.networkName).Run() // best-effort
	}

	for _, registry := range j.registryLogouts {
		dockerRegistryLogout(ctx, registry) // best-effort, error intentionally ignored
	}

	if err := os.RemoveAll(j.hostFilesRoot); err != nil && stopErr == nil {
		return fmt.Errorf("remove job files root %s: %w", j.hostFilesRoot, err)
	}
	return stopErr
}
```

- [ ] **Step 6: Run the new tests**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/runner/... -run TestLinuxDockerBackend_StartJob_Service -v`
Expected: PASS (both tests).

- [ ] **Step 7: Run the full test suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && go test ./... 2>&1 | tail -20`
Expected: everything passes.

- [ ] **Step 8: Commit**

```bash
git add internal/runner/docker_backend.go internal/runner/docker_backend_test.go
git commit -m "feat(runner): services sidecar containers, per-job network, health wait"
```

---

### Task 7: Engine wiring — `JobRunOptions.JobID`, container env merge, spec conversion, `RunWorkflow` threading

**Files:**
- Modify: `internal/engine/executor.go`
- Modify: `internal/engine/workflow_run.go`
- Test: `internal/engine/executor_test.go`

**Interfaces:**
- Consumes: `Job.Container()` (Task 1), `validateCredentials` (Task 2), `runner.ContainerSpec`/`runner.Backend.StartJob` (Task 4).
- Produces: `JobRunOptions.JobID string` field. `func toRunnerContainerSpec(spec *ContainerSpec) (*runner.ContainerSpec, error)`. Nothing outside this task consumes these — this is the final wiring task before the example-workflow verification in Task 8.

- [ ] **Step 1: Write the failing test**

Add to `internal/engine/executor_test.go`:

```go
func TestRunJob_ContainerEnvMergesIntoStepEnv(t *testing.T) {
	// A bare RawContainer node representing container: {env: {FOO: bar}}
	// isn't easy to hand-construct outside YAML parsing, so this test
	// parses a real workflow instead of hand-building a Job.
	wf, err := Parse([]byte(`
name: t
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    container:
      image: node:20
      env:
        FOO: bar
    steps:
      - id: s
        run: echo hi
        shell: sh
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]

	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}
	_, err = RunJob(context.Background(), wf, &job, backend, JobRunOptions{
		WorkspaceDir: t.TempDir(),
		JobID:        "build",
	})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if backend.lastJob == nil {
		t.Fatal("backend.lastJob is nil, want StartJob to have been called")
	}
	if len(backend.lastJob.execSpecs) != 1 {
		t.Fatalf("execSpecs = %v, want exactly 1 step executed", backend.lastJob.execSpecs)
	}
	if got := backend.lastJob.execSpecs[0].Env["FOO"]; got != "bar" {
		t.Errorf("step Env[FOO] = %q, want %q (container.env should merge into every step's env)", got, "bar")
	}
}

func TestToRunnerContainerSpec_Nil(t *testing.T) {
	spec, err := toRunnerContainerSpec(nil)
	if err != nil {
		t.Fatalf("toRunnerContainerSpec(nil) error = %v", err)
	}
	if spec != nil {
		t.Errorf("toRunnerContainerSpec(nil) = %v, want nil", spec)
	}
}

func TestToRunnerContainerSpec_ResolvesCredentials(t *testing.T) {
	spec, err := toRunnerContainerSpec(&ContainerSpec{
		Image:       "ghcr.io/owner/image:tag",
		Credentials: map[string]string{"username": "u", "password": "p"},
	})
	if err != nil {
		t.Fatalf("toRunnerContainerSpec() error = %v", err)
	}
	if spec.Image != "ghcr.io/owner/image:tag" || spec.Username != "u" || spec.Password != "p" {
		t.Errorf("toRunnerContainerSpec() = %+v, want Image=ghcr.io/owner/image:tag Username=u Password=p", spec)
	}
}

func TestToRunnerContainerSpec_InvalidCredentialsError(t *testing.T) {
	_, err := toRunnerContainerSpec(&ContainerSpec{
		Image:       "node:20",
		Credentials: map[string]string{"username": "u"},
	})
	if err == nil {
		t.Fatal("toRunnerContainerSpec() error = nil, want error for malformed credentials")
	}
}
```

(The first test, `TestRunJob_ContainerEnvMergesIntoStepEnv`, is intentionally light on assertions about `fakeBackend`'s internals since `fakeBackend`/`fakeJob`'s exact field names for inspecting passed-through container specs don't exist yet — its real job is just to prove `RunJob` doesn't error when a job has a real parsed `container:` block and a `JobID` is supplied. Read `fakeJob`'s definition in `executor_test.go` before finalizing this test — if it already exposes a field for the last `StepSpec.Env` used, assert `FOO=bar` appears there instead of the placeholder body above.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run "TestRunJob_ContainerEnvMergesIntoStepEnv|TestToRunnerContainerSpec" -v`
Expected: FAIL — `toRunnerContainerSpec undefined`, `JobRunOptions.JobID` field doesn't exist (won't compile).

- [ ] **Step 3: Add `JobID` to `JobRunOptions`**

In `internal/engine/executor.go`, add to the `JobRunOptions` struct:

```go
type JobRunOptions struct {
	Needs                    map[string]JobOutcome
	Matrix                   MatrixCombination
	Vars                     map[string]string
	WorkspaceDir             string // host directory bind-mounted as the job's workspace
	LocalRepositoryOverrides map[string]string
	ExtraEnv                 map[string]string
	JobID                    string // names the job for Docker network naming when it has services:
}
```

- [ ] **Step 4: Implement `toRunnerContainerSpec`**

Add to `internal/engine/executor.go` (near `RunJob`, before or after it):

```go
// toRunnerContainerSpec converts an engine ContainerSpec (YAML-shaped,
// with a raw credentials map) into a runner.ContainerSpec (already
// credential-validated, Username/Password resolved) — the boundary where
// credential validation happens, keeping the runner package free of any
// YAML/credentials-map concerns.
func toRunnerContainerSpec(spec *ContainerSpec) (*runner.ContainerSpec, error) {
	if spec == nil {
		return nil, nil
	}
	username, password, err := validateCredentials(spec.Credentials)
	if err != nil {
		return nil, fmt.Errorf("container credentials: %w", err)
	}
	return &runner.ContainerSpec{
		Image:    spec.Image,
		Env:      spec.Env,
		Ports:    spec.Ports,
		Volumes:  spec.Volumes,
		Options:  spec.Options,
		Username: username,
		Password: password,
	}, nil
}
```

- [ ] **Step 5: Wire it into `RunJob`**

In `internal/engine/executor.go`'s `RunJob`, replace:

```go
	actx := NewContext(wf, job)
	actx.Needs = opts.Needs
	actx.Matrix = opts.Matrix
	actx.Vars = opts.Vars
	for k, v := range opts.ExtraEnv {
		actx.Env[k] = v
	}
	result := &JobResult{Conclusion: "success"}

	runnerJob, err := backend.StartJob(ctx, opts.WorkspaceDir)
	if err != nil {
		return nil, fmt.Errorf("start job: %w", err)
	}
	defer runnerJob.Stop(ctx)
```

with:

```go
	actx := NewContext(wf, job)
	actx.Needs = opts.Needs
	actx.Matrix = opts.Matrix
	actx.Vars = opts.Vars
	for k, v := range opts.ExtraEnv {
		actx.Env[k] = v
	}
	result := &JobResult{Conclusion: "success"}

	containerSpec, err := job.Container()
	if err != nil {
		return nil, fmt.Errorf("job container: %w", err)
	}
	if containerSpec != nil {
		for k, v := range containerSpec.Env {
			actx.Env[k] = v
		}
	}
	runnerContainerSpec, err := toRunnerContainerSpec(containerSpec)
	if err != nil {
		return nil, err
	}
	runnerServices := make(map[string]runner.ContainerSpec, len(job.Services))
	for name, spec := range job.Services {
		converted, err := toRunnerContainerSpec(&spec)
		if err != nil {
			return nil, fmt.Errorf("service %s: %w", name, err)
		}
		runnerServices[name] = *converted
	}

	jobID := opts.JobID
	if jobID == "" {
		jobID = "job"
	}
	runnerJob, err := backend.StartJob(ctx, jobID, opts.WorkspaceDir, runnerContainerSpec, runnerServices)
	if err != nil {
		return nil, fmt.Errorf("start job: %w", err)
	}
	defer runnerJob.Stop(ctx)
```

- [ ] **Step 6: Thread `JobID` through `RunWorkflow`**

In `internal/engine/workflow_run.go`, in the `RunJob` call inside `RunWorkflow`'s loop, add `JobID: name` to the `JobRunOptions{}` literal:

```go
			jr, err := RunJob(runCtx, wf, &job, backend, JobRunOptions{
				Needs:                    outcomes,
				Matrix:                   combo,
				WorkspaceDir:             workspaceDir,
				LocalRepositoryOverrides: localRepositoryOverrides,
				Vars:                     vars,
				ExtraEnv:                 extraEnv,
				JobID:                    name,
			})
```

- [ ] **Step 7: Run the new tests**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run "TestRunJob_ContainerEnvMergesIntoStepEnv|TestToRunnerContainerSpec" -v`
Expected: PASS (all 3 tests).

- [ ] **Step 8: Run the full test suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && go test ./... 2>&1 | tail -20`
Expected: everything passes — every job without `container:`/`services:` gets `containerSpec == nil` and `runnerServices` as an empty (non-nil but zero-length) map, matching the "completely unaffected" requirement from the spec.

- [ ] **Step 9: `gofmt`/`go vet` check**

Run: `export PATH="/opt/homebrew/bin:$PATH" && gofmt -l internal/engine internal/runner && go vet ./...`
Expected: no output from `gofmt -l` (nothing needs formatting), no vet errors.

- [ ] **Step 10: Commit**

```bash
git add internal/engine/executor.go internal/engine/workflow_run.go internal/engine/executor_test.go
git commit -m "feat(engine): wire container:/services: through RunJob to the backend"
```

---

### Task 8: Real end-to-end verification and docs

**Files:**
- Create: `examples/workflows/services-container.yml`
- Modify: `docs/usage.md` (move `services:`/`container:` out of "What's not supported yet", document it under "What's supported today")
- Modify: `CHANGELOG.md`
- Modify: `internal/engine/workflow.go` (only if the real run below surfaces a bug)
- Modify: `internal/runner/docker_backend.go` (only if the real run below surfaces a bug)

- [ ] **Step 1: Write the example workflow**

Create `examples/workflows/services-container.yml`:

```yaml
# Demonstrates job-level container: (the job's own container runs a
# different image than the default ubuntu:22.04) and services: (a
# sidecar container reachable from job steps by name).
#
# Try it:
#   mirror run examples/workflows/services-container.yml
name: services and container
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    container: node:20
    services:
      postgres:
        image: postgres:16
        env:
          POSTGRES_PASSWORD: postgres
        options: >-
          --health-cmd "pg_isready -U postgres"
          --health-interval 2s
          --health-timeout 2s
          --health-retries 10
    steps:
      - name: confirm container image swap
        run: node --version
      - name: install postgres client
        run: apt-get update -qq && apt-get install -y -qq postgresql-client
      - name: confirm service reachable by name
        run: pg_isready -h postgres -U postgres
```

- [ ] **Step 2: Run it for real**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go run ./cmd/mirror run examples/workflows/services-container.yml 2>&1 | tail -60`
Expected: all three steps succeed; `node --version` prints a real Node 20.x version (proving the image swap — the bare `ubuntu:22.04` image has no `node` binary at all); `pg_isready -h postgres -U postgres` prints `postgres:5432 - accepting connections` (proving the service is reachable by name from inside the `node:20` job container over the per-job network).

If any step fails, root-cause it for real (add temporary logging if needed, same discipline as every other sub-project this session) and fix the actual bug — do not weaken the workflow to route around it. Likely candidates if something breaks: `node:20`'s base OS (Debian) needing `apt-get update` before `postgresql-client` installs (already included above); the healthcheck not being ready before the job container starts running steps (check `waitForServiceHealth`'s polling actually observed `"healthy"` before returning — add a `docker inspect` sanity check manually if the failure is intermittent).

- [ ] **Step 3: Confirm cleanup**

Run: `export PATH="/opt/homebrew/bin:$PATH" && docker network ls | grep mirror-svc; docker ps -a | grep -E "postgres|node:20"`
Expected: both commands print nothing — the network and both containers (job + service) were removed when the run finished. If the network or containers linger, `dockerJob.Stop`'s teardown has a bug — fix it (check the network name computed by `StartJob` actually matches what `Stop` tries to remove, and that `Stop` runs even when a step fails partway through the job).

- [ ] **Step 4: Update docs/usage.md**

In `docs/usage.md`, remove this line from "What's not supported yet":
```
- `services:` and `container:` job fields
```

Add a new bullet to "What's supported today" (alongside the other job-feature bullets, near `strategy.matrix`):

```
- **`container:` and `services:` job fields** — `container:` swaps the
  image the job's own container runs as (bare image string or a mapping
  with `env`/`ports`/`volumes`/`options`/`credentials`); `services:`
  starts one sidecar container per entry, reachable from job steps by
  its map key as hostname over a per-job Docker network created only
  when services are present (jobs without `services:` are unaffected —
  no network is created). Both support `credentials:` (`username`/
  `password`, exactly those two keys) for private registries — mirror-gha
  shells out to `docker login`/`docker logout` around the pull/run since
  it has no Docker Go SDK dependency (password piped via stdin, never
  passed as a CLI argument). Health-checked services (`options:
  --health-cmd ...`) are waited on (up to 5 minutes) before the job's
  steps start; services with no healthcheck are treated as ready
  immediately. Known v1 limitation: registry login/logout is
  process-wide (shared Docker daemon config), so two jobs in the same
  `mirror run` pulling different private images concurrently could
  race — acceptable for a local single-workflow-run tool, not solved in
  v1.
```

- [ ] **Step 5: Update CHANGELOG.md**

Add to the `### Added` section, above the most recent entry:

```markdown
- **`container:` and `services:` job fields.** Checked against act's own
  implementation (`pkg/model/workflow.go`'s `Job.Container()`/
  `ContainerSpec`, `pkg/runner/run_context.go`'s `startJobContainer`/
  service loop, `pkg/container/docker_network.go`) rather than guessed:
  `container:` swaps the image the job's own container runs (no separate
  "runner" container — act doesn't have one either); `services:` starts
  one sidecar container per entry on a per-job Docker network created
  only when services are present, reachable from the job container by
  service name via `--network-alias` (Docker's embedded DNS only
  resolves aliases on user-defined networks, not the default bridge, so
  the job container joins that same network whenever services exist).
  Registry `credentials:` supported for both, via `docker login`/
  `docker logout` (mirror-gha shells to the `docker` CLI, not the Docker
  Go SDK act uses) — password piped via stdin, never passed as a CLI
  argument. A hand-rolled quote-aware tokenizer parses `options:` since
  values like `--health-cmd "pg_isready -U postgres"` contain spaces
  inside quotes (no external shlex dependency, keeping the
  zero-external-Go-dependency record intact). Verified for real: a
  `container: node:20` job running a step that depends on Node actually
  being present, with a `services: postgres` sidecar reached by hostname
  from inside that non-default job-container image, and confirmed
  `docker network`/`docker ps` show everything cleaned up after the run.
```

- [ ] **Step 6: Final full-suite check**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l . | grep -v third_party && go vet ./... && go test ./... 2>&1 | tail -20`
Expected: clean build, no unformatted files (outside `third_party/`), no vet errors, every package `ok`.

- [ ] **Step 7: Commit**

```bash
git add examples/workflows/services-container.yml docs/usage.md CHANGELOG.md
git commit -m "docs: document container:/services: support, add example workflow"
```

(If Step 2/3 surfaced and required a real bug fix, that fix should already be committed as its own commit before this one, with its own regression test — same discipline as every prior sub-project's final verification task this session.)
