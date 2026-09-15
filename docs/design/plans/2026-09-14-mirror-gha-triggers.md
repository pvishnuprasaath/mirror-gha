# Triggers and Event Payloads Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Real `github.event_name`/`github.event`/`GITHUB_EVENT_NAME`/`GITHUB_EVENT_PATH`, replacing the hardcoded `event_name: "workflow_dispatch"` stub, plus synthetic default event payloads for `push`/`pull_request`/`workflow_dispatch`/`workflow_call`/`repository_dispatch`/`workflow_run` and a real `--event-path`/`--event-name` override mechanism matching act's own.

**Architecture:** `Workflow.OnEventNames()` parses the three `on:` YAML shapes. `cmd/mirror/main.go` resolves the effective event name (flag > single `on:` trigger > `"push"` default, matching act's priority chain) and event JSON (real `--event-path` file, or a synthetic default payload builder keyed by event name), writes it to one host temp file, and threads both through `RunWorkflow`/`JobRunOptions` down to `RunJob`, which stages the file into every job's environment via the existing `/mirror-*` `CopyToContainer` convention and populates `actx.GitHub["event_name"/"event_path"/"event"]`. A new `resolveNestedPath` helper gives `github.event.*` arbitrary-depth traversal into the loaded JSON, since the existing `github` context case only ever handled flat two-segment lookups.

**Tech Stack:** Go 1.27 stdlib only — `encoding/json` for payload parsing (already used elsewhere in this codebase for `toJSON()`/`fromJSON()`).

**Spec:** `docs/design/specs/2026-09-14-mirror-gha-design.md`, "Triggers and Event Payloads" section.

## Global Constraints

- No `on: push/pull_request` branches/paths/types filtering — matches act's own choice (confirmed dead/unwired code in act's own source), and is inherently moot once a user has explicitly invoked a local run.
- Synthetic default payloads are plausible, structurally-real shapes (matching GitHub's public webhook documentation), not attempts at exhaustive fidelity — any trigger name outside the six covered falls back to `{}`, matching act's own default for the untyped case.
- `workflow_dispatch`/`workflow_call` inputs are represented structurally (empty `inputs: {}`) but not populated from a new CLI input mechanism — out of scope, a distinct feature.
- `--event-path` (real, user-supplied) always overrides the synthetic default when given.
- Every existing test must keep passing; `RunWorkflow`'s signature change updates every call site in the same task that introduces it.

---

### Task 1: `Workflow.OnEventNames()` — parse all three `on:` shapes

**Files:**
- Modify: `internal/engine/workflow.go`
- Test: `internal/engine/workflow_test.go`

**Interfaces:**
- Produces: `func (wf *Workflow) OnEventNames() ([]string, error)`. Consumed by Task 5's event-name resolution.

- [ ] **Step 1: Write the failing tests**

Add to `internal/engine/workflow_test.go`:

```go
func TestWorkflow_OnEventNames_BareString(t *testing.T) {
	wf := &Workflow{On: "push"}
	names, err := wf.OnEventNames()
	if err != nil {
		t.Fatalf("OnEventNames() error = %v", err)
	}
	if len(names) != 1 || names[0] != "push" {
		t.Fatalf("OnEventNames() = %v, want [push]", names)
	}
}

func TestWorkflow_OnEventNames_List(t *testing.T) {
	wf := &Workflow{On: []interface{}{"push", "pull_request"}}
	names, err := wf.OnEventNames()
	if err != nil {
		t.Fatalf("OnEventNames() error = %v", err)
	}
	if len(names) != 2 || names[0] != "push" || names[1] != "pull_request" {
		t.Fatalf("OnEventNames() = %v, want [push pull_request]", names)
	}
}

func TestWorkflow_OnEventNames_Mapping(t *testing.T) {
	yaml := []byte(`
name: sample
on:
  push:
    branches: [main]
  workflow_dispatch: {}
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	names, err := wf.OnEventNames()
	if err != nil {
		t.Fatalf("OnEventNames() error = %v", err)
	}
	if len(names) != 2 {
		t.Fatalf("OnEventNames() = %v, want 2 entries", names)
	}
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["push"] || !found["workflow_dispatch"] {
		t.Fatalf("OnEventNames() = %v, want to contain push and workflow_dispatch", names)
	}
}

func TestWorkflow_OnEventNames_Nil(t *testing.T) {
	wf := &Workflow{}
	names, err := wf.OnEventNames()
	if err != nil {
		t.Fatalf("OnEventNames() error = %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("OnEventNames() = %v, want empty", names)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestWorkflow_OnEventNames -v`
Expected: FAIL — `wf.OnEventNames` undefined.

- [ ] **Step 3: Implement**

Add to `internal/engine/workflow.go`, after the `Workflow` struct's existing `Concurrency()` method:

```go
// OnEventNames extracts the trigger names from the workflow's on: field,
// which GitHub Actions allows as a bare string, a list of strings, or a
// mapping (keys are the trigger names, values are each trigger's own
// sub-config — e.g. branches/paths filters, which mirror-gha doesn't
// evaluate, matching act's own choice not to either). Returns an empty
// slice, not an error, for a workflow with no on: field at all.
func (wf *Workflow) OnEventNames() ([]string, error) {
	switch v := wf.On.(type) {
	case nil:
		return nil, nil
	case string:
		return []string{v}, nil
	case []interface{}:
		names := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("on: list entries must be strings, got %T", item)
			}
			names = append(names, s)
		}
		return names, nil
	case map[string]interface{}:
		names := make([]string, 0, len(v))
		for k := range v {
			names = append(names, k)
		}
		sort.Strings(names)
		return names, nil
	default:
		return nil, fmt.Errorf("on: must be a string, a list, or a mapping, got %T", wf.On)
	}
}
```

Add `"sort"` to `internal/engine/workflow.go`'s imports if not already present (check first — `internal/engine/matrix.go` already imports `sort` in this package, but `workflow.go` itself may not yet).

- [ ] **Step 4: Run tests, then the full engine suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestWorkflow_OnEventNames -v`
Expected: PASS (all 4).

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l internal/engine/workflow.go internal/engine/workflow_test.go && go test ./internal/engine/... 2>&1 | tail -10`
Expected: clean, all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/engine/workflow.go internal/engine/workflow_test.go
git commit -m "feat(engine): parse workflow on: trigger names"
```

---

### Task 2: `github.event.*` arbitrary-depth resolution

**Files:**
- Modify: `internal/engine/context.go`
- Test: `internal/engine/context_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func resolveNestedPath(val interface{}, remaining []string) (interface{}, error)`. Wired into `resolvePath`'s existing `"github"` case, consumed by Task 4's `RunJob` wiring (which populates `c.GitHub["event"]`).

- [ ] **Step 1: Write the failing tests**

Add to `internal/engine/context_test.go`:

```go
func TestResolvePath_GithubEventNestedMap(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.GitHub["event"] = map[string]interface{}{
		"pull_request": map[string]interface{}{
			"number": float64(42),
			"head":   map[string]interface{}{"ref": "feature-branch"},
		},
	}

	got, err := ctx.resolvePath([]string{"github", "event", "pull_request", "number"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v", err)
	}
	if got != float64(42) {
		t.Errorf("resolvePath() = %v, want 42", got)
	}

	got, err = ctx.resolvePath([]string{"github", "event", "pull_request", "head", "ref"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v", err)
	}
	if got != "feature-branch" {
		t.Errorf("resolvePath() = %v, want feature-branch", got)
	}
}

func TestResolvePath_GithubEventCaseInsensitiveKeys(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.GitHub["event"] = map[string]interface{}{"Ref": "refs/heads/main"}

	got, err := ctx.resolvePath([]string{"github", "event", "ref"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v", err)
	}
	if got != "refs/heads/main" {
		t.Errorf("resolvePath() = %v, want refs/heads/main", got)
	}
}

func TestResolvePath_GithubEventArrayIndex(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.GitHub["event"] = map[string]interface{}{
		"commits": []interface{}{
			map[string]interface{}{"message": "first"},
			map[string]interface{}{"message": "second"},
		},
	}

	got, err := ctx.resolvePath([]string{"github", "event", "commits", "1", "message"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v", err)
	}
	if got != "second" {
		t.Errorf("resolvePath() = %v, want second", got)
	}
}

func TestResolvePath_GithubEventMissingPathReturnsNil(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	ctx.GitHub["event"] = map[string]interface{}{"ref": "refs/heads/main"}

	got, err := ctx.resolvePath([]string{"github", "event", "pull_request", "number"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v, want nil error for a missing path", err)
	}
	if got != nil {
		t.Errorf("resolvePath() = %v, want nil for a missing path", got)
	}
}

func TestResolvePath_GithubEventWhenEventFieldAbsent(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})
	got, err := ctx.resolvePath([]string{"github", "event", "ref"})
	if err != nil {
		t.Fatalf("resolvePath() error = %v", err)
	}
	if got != nil {
		t.Errorf("resolvePath() = %v, want nil when github.event was never set", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestResolvePath_GithubEvent -v`
Expected: FAIL — `github.event.pull_request.number` currently hits the existing `case "github":`'s `len(path) != 2` check and errors "invalid github reference."

- [ ] **Step 3: Implement**

In `internal/engine/context.go`, replace the existing `case "github":` block inside `resolvePath`:

```go
	case "github":
		if len(path) < 2 {
			return nil, fmt.Errorf("invalid github reference: %s", strings.Join(path, "."))
		}
		if strings.EqualFold(path[1], "event") {
			return resolveNestedPath(c.GitHub["event"], path[2:])
		}
		if len(path) != 2 {
			return nil, fmt.Errorf("invalid github reference: %s", strings.Join(path, "."))
		}
		return lookupAnyCI(c.GitHub, path[1]), nil
```

Add after `lookupAnyCI`'s existing definition:

```go
// resolveNestedPath traverses an arbitrary JSON-shaped value (as produced
// by encoding/json's generic Unmarshal into interface{} — map[string]
// interface{} for objects, []interface{} for arrays, float64/string/bool/
// nil for scalars) by the given path segments. Used for github.event.*,
// which can be arbitrarily deep and shaped since it holds a real (or
// synthetic) GitHub webhook payload, not a fixed set of known keys the
// way every other github.* field is. Returns nil, nil for a path that
// doesn't resolve (a missing property, an out-of-range index) rather than
// an error, matching this codebase's existing leniency for
// steps.<id>.outputs.<name>/needs.<job>.outputs.<name> — not every
// payload (real or synthetic) includes every field a workflow might
// reference.
func resolveNestedPath(val interface{}, remaining []string) (interface{}, error) {
	if len(remaining) == 0 {
		return val, nil
	}
	key := remaining[0]
	switch v := val.(type) {
	case map[string]interface{}:
		for k, mv := range v {
			if strings.EqualFold(k, key) {
				return resolveNestedPath(mv, remaining[1:])
			}
		}
		return nil, nil
	case []interface{}:
		idx, err := strconv.Atoi(key)
		if err != nil || idx < 0 || idx >= len(v) {
			return nil, nil
		}
		return resolveNestedPath(v[idx], remaining[1:])
	default:
		return nil, nil
	}
}
```

Add `"strconv"` to `internal/engine/context.go`'s imports.

- [ ] **Step 4: Run tests, then the full engine suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestResolvePath_GithubEvent -v`
Expected: PASS (all 5).

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l internal/engine/context.go internal/engine/context_test.go && go test ./internal/engine/... 2>&1 | tail -10`
Expected: clean, all pass — the existing `github.workspace`/`github.sha`-style two-segment lookups are unaffected (the new `len(path) < 2` check is looser than the old `!= 2`, and the `event` branch is checked before the still-present `!= 2` guard for everything else).

- [ ] **Step 5: Commit**

```bash
git add internal/engine/context.go internal/engine/context_test.go
git commit -m "feat(engine): arbitrary-depth github.event.* resolution"
```

---

### Task 3: Synthetic default event payload builders

**Files:**
- Create: `internal/engine/event_payloads.go`
- Test: `internal/engine/event_payloads_test.go`

**Interfaces:**
- Produces: `func DefaultEventPayload(eventName string) string` (returns a raw JSON string). Consumed by Task 5's `cmd/mirror/main.go` wiring.

- [ ] **Step 1: Write the failing tests**

Create `internal/engine/event_payloads_test.go`:

```go
package engine

import (
	"encoding/json"
	"testing"
)

func TestDefaultEventPayload_UnknownEventIsEmptyObject(t *testing.T) {
	got := DefaultEventPayload("release")
	if got != "{}" {
		t.Errorf("DefaultEventPayload(release) = %q, want {}", got)
	}
}

func TestDefaultEventPayload_Push(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("push")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["ref"] != "refs/heads/main" {
		t.Errorf("ref = %v, want refs/heads/main", payload["ref"])
	}
	headCommit, ok := payload["head_commit"].(map[string]interface{})
	if !ok || headCommit["message"] == "" {
		t.Errorf("head_commit.message missing or empty: %v", payload["head_commit"])
	}
	pusher, ok := payload["pusher"].(map[string]interface{})
	if !ok || pusher["name"] != "local" {
		t.Errorf("pusher.name = %v, want local", payload["pusher"])
	}
}

func TestDefaultEventPayload_PullRequest(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("pull_request")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["action"] != "opened" {
		t.Errorf("action = %v, want opened", payload["action"])
	}
	pr, ok := payload["pull_request"].(map[string]interface{})
	if !ok {
		t.Fatalf("pull_request missing: %v", payload)
	}
	head, ok := pr["head"].(map[string]interface{})
	if !ok || head["ref"] == "" {
		t.Errorf("pull_request.head.ref missing: %v", pr)
	}
	base, ok := pr["base"].(map[string]interface{})
	if !ok || base["ref"] != "main" {
		t.Errorf("pull_request.base.ref = %v, want main", base)
	}
}

func TestDefaultEventPayload_WorkflowDispatch(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("workflow_dispatch")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := payload["inputs"].(map[string]interface{}); !ok {
		t.Errorf("inputs missing or wrong type: %v", payload["inputs"])
	}
}

func TestDefaultEventPayload_WorkflowCall(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("workflow_call")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := payload["inputs"].(map[string]interface{}); !ok {
		t.Errorf("inputs missing or wrong type: %v", payload["inputs"])
	}
}

func TestDefaultEventPayload_RepositoryDispatch(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("repository_dispatch")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["action"] == "" {
		t.Errorf("action missing: %v", payload)
	}
	if _, ok := payload["client_payload"].(map[string]interface{}); !ok {
		t.Errorf("client_payload missing or wrong type: %v", payload["client_payload"])
	}
}

func TestDefaultEventPayload_WorkflowRun(t *testing.T) {
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(DefaultEventPayload("workflow_run")), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	wr, ok := payload["workflow_run"].(map[string]interface{})
	if !ok {
		t.Fatalf("workflow_run missing: %v", payload)
	}
	if wr["conclusion"] != "success" {
		t.Errorf("workflow_run.conclusion = %v, want success", wr["conclusion"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestDefaultEventPayload -v`
Expected: FAIL — `DefaultEventPayload` undefined.

- [ ] **Step 3: Implement**

Create `internal/engine/event_payloads.go`:

```go
package engine

// DefaultEventPayload returns a synthetic, structurally-real GitHub
// webhook payload for the six trigger types covered here (matching
// GitHub's own public webhook-payload documentation — act has no
// reference implementation for this at all, confirmed via source: it
// never fabricates a per-event payload, only ever loading a real
// user-supplied one or falling back to "{}"). Populated with the same
// placeholder conventions this project already uses elsewhere (a
// 40-zero sha, "local/mirror-gha" as the repository, "refs/heads/main").
// Any trigger name outside these six falls back to "{}", matching act's
// own default for the untyped case rather than attempting exhaustive
// coverage of GitHub's several dozen trigger types.
func DefaultEventPayload(eventName string) string {
	const zeroSHA = "0000000000000000000000000000000000000000"
	switch eventName {
	case "push":
		return `{
  "ref": "refs/heads/main",
  "before": "` + zeroSHA + `",
  "after": "` + zeroSHA + `",
  "repository": {"full_name": "local/mirror-gha", "default_branch": "main"},
  "pusher": {"name": "local", "email": "local@example.com"},
  "head_commit": {"id": "` + zeroSHA + `", "message": "local test run", "author": {"name": "local"}}
}`
	case "pull_request":
		return `{
  "action": "opened",
  "number": 1,
  "pull_request": {
    "number": 1,
    "title": "Local test pull request",
    "state": "open",
    "draft": false,
    "merged": false,
    "head": {"ref": "feature-branch", "sha": "` + zeroSHA + `"},
    "base": {"ref": "main", "sha": "` + zeroSHA + `"},
    "user": {"login": "local"}
  },
  "repository": {"full_name": "local/mirror-gha"}
}`
	case "workflow_dispatch":
		return `{"inputs": {}, "ref": "refs/heads/main", "repository": {"full_name": "local/mirror-gha"}}`
	case "workflow_call":
		return `{"inputs": {}}`
	case "repository_dispatch":
		return `{"action": "mirror-local-event", "client_payload": {}, "repository": {"full_name": "local/mirror-gha"}}`
	case "workflow_run":
		return `{
  "action": "completed",
  "workflow_run": {
    "id": 1,
    "name": "local test run",
    "head_branch": "main",
    "head_sha": "` + zeroSHA + `",
    "status": "completed",
    "conclusion": "success",
    "event": "push"
  }
}`
	default:
		return "{}"
	}
}
```

- [ ] **Step 4: Run tests, then the full engine suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestDefaultEventPayload -v`
Expected: PASS (all 7).

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l internal/engine/event_payloads.go internal/engine/event_payloads_test.go && go test ./internal/engine/... 2>&1 | tail -10`
Expected: clean, all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/engine/event_payloads.go internal/engine/event_payloads_test.go
git commit -m "feat(engine): synthetic default event payloads for six trigger types"
```

---

### Task 4: `RunJob` wiring — stage event.json, populate `actx.GitHub`

**Files:**
- Modify: `internal/engine/executor.go`
- Test: `internal/engine/executor_test.go`

**Interfaces:**
- Consumes: `resolveNestedPath` (Task 2, indirectly via `resolvePath`).
- Produces: `JobRunOptions.EventName string`, `JobRunOptions.EventJSONDir string` (a host directory containing exactly one file, `event.json` — reusing the existing `/mirror-*` `CopyToContainer` convention, which copies a directory's contents, not a single file). Consumed by Task 5's `RunWorkflow`/`cmd/mirror/main.go` wiring.

- [ ] **Step 1: Write the failing test**

Add to `internal/engine/executor_test.go`:

```go
func TestRunJob_StagesEventJSONAndExposesGithubEvent(t *testing.T) {
	eventDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(eventDir, "event.json"), []byte(`{"ref":"refs/heads/main","pull_request":{"number":7}}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	wf := &Workflow{Name: "t"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "s", Run: "echo hi"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{
		WorkspaceDir: t.TempDir(),
		EventName:    "pull_request",
		EventJSONDir: eventDir,
	})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if backend.lastJob == nil {
		t.Fatal("backend.lastJob is nil")
	}
	if len(backend.lastJob.execSpecs) != 1 {
		t.Fatalf("execSpecs = %v, want exactly 1 step executed", backend.lastJob.execSpecs)
	}
	if got := backend.lastJob.execSpecs[0].Env["GITHUB_EVENT_NAME"]; got != "pull_request" {
		t.Errorf(`Env["GITHUB_EVENT_NAME"] = %q, want "pull_request"`, got)
	}
	if got := backend.lastJob.execSpecs[0].Env["GITHUB_EVENT_PATH"]; got != "/mirror-event/event.json" {
		t.Errorf(`Env["GITHUB_EVENT_PATH"] = %q, want "/mirror-event/event.json"`, got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestRunJob_StagesEventJSONAndExposesGithubEvent -v`
Expected: FAIL — `JobRunOptions.EventName`/`EventJSONDir` don't exist yet (won't compile).

- [ ] **Step 3: Add the fields and wiring**

In `internal/engine/executor.go`, add to `JobRunOptions`:

```go
	JobID                    string // names the job for Docker network naming when it has services:
	EventName                string // github.event_name / GITHUB_EVENT_NAME — empty means the caller didn't set one (RunWorkflow always sets it)
	EventJSONDir             string // host directory containing exactly one file, event.json — staged to /mirror-event via CopyToContainer, matching the existing /mirror-node convention. Empty means no event payload is staged.
}
```

In `RunJob`, find the existing `runnerJob, err := backend.StartJob(...)` call and its `defer runnerJob.Stop(ctx)` line (this must come before the new code below, since staging the event file needs a running `runnerJob` to call `CopyToContainer` on). Immediately after that `defer` line, and before the existing `actx.GitHub["workspace"] = runnerJob.WorkspacePath()` line, add:

```go
	if opts.EventName != "" {
		actx.GitHub["event_name"] = opts.EventName
	}
	if opts.EventJSONDir != "" {
		data, err := os.ReadFile(filepath.Join(opts.EventJSONDir, "event.json"))
		if err != nil {
			return nil, fmt.Errorf("read event.json: %w", err)
		}
		var event map[string]interface{}
		if err := json.Unmarshal(data, &event); err != nil {
			return nil, fmt.Errorf("parse event.json: %w", err)
		}
		actx.GitHub["event"] = event
		if err := runnerJob.CopyToContainer(ctx, opts.EventJSONDir, "/mirror-event"); err != nil {
			return nil, fmt.Errorf("stage event.json: %w", err)
		}
		actx.GitHub["event_path"] = "/mirror-event/event.json"
	}
```

Add `"encoding/json"` and `"path/filepath"` to `internal/engine/executor.go`'s imports if not already present (check first — `os` is already imported).

- [ ] **Step 4: Run the test, then the full engine suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestRunJob_StagesEventJSONAndExposesGithubEvent -v`
Expected: PASS.

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l internal/engine/executor.go internal/engine/executor_test.go && go test ./internal/engine/... 2>&1 | tail -10`
Expected: clean, all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/engine/executor.go internal/engine/executor_test.go
git commit -m "feat(engine): stage event.json per job, expose github.event/event_name/event_path"
```

---

### Task 5: `RunWorkflow` wiring, event-name resolution, CLI flags

**Files:**
- Modify: `internal/engine/workflow_run.go`
- Modify: `internal/engine/workflow_run_test.go`
- Modify: `cmd/mirror/main.go`

**Interfaces:**
- Consumes: `Workflow.OnEventNames()` (Task 1), `DefaultEventPayload` (Task 3), `JobRunOptions.EventName`/`EventJSONDir` (Task 4).
- Produces: `RunWorkflow`'s new trailing parameters `eventName, eventJSONDir string`. `--event-path`/`--event-name` CLI flags.

- [ ] **Step 1: Write the failing test**

Add to `internal/engine/workflow_run_test.go`:

```go
func TestRunWorkflow_ThreadsEventNameAndJSONToJobs(t *testing.T) {
	eventDir := t.TempDir()
	if err := os.WriteFile(eventDir+"/event.json", []byte(`{"ref":"refs/heads/main"}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	wf := &Workflow{
		Name: "test",
		Jobs: map[string]Job{
			"a": {RunsOn: "ubuntu-latest", Steps: []Step{{ID: "s", Run: "echo a"}}},
		},
	}

	result, err := RunWorkflow(context.Background(), wf, succeedSelector, t.TempDir(), nil, nil, nil, "push", eventDir)
	if err != nil {
		t.Fatalf("RunWorkflow() error = %v", err)
	}
	if result.Jobs["a"].Conclusion != "success" {
		t.Fatalf("Conclusion = %q, want success", result.Jobs["a"].Conclusion)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestRunWorkflow_ThreadsEventNameAndJSONToJobs -v`
Expected: FAIL — `RunWorkflow` currently takes 7 arguments, this call passes 9 (won't compile).

- [ ] **Step 3: Update `RunWorkflow`'s signature and every existing call site**

In `internal/engine/workflow_run.go`, change:

```go
func RunWorkflow(ctx context.Context, wf *Workflow, selectBackend BackendSelector, workspaceDir string, localRepositoryOverrides map[string]string, vars map[string]string, extraEnv map[string]string) (*WorkflowResult, error) {
```

to:

```go
func RunWorkflow(ctx context.Context, wf *Workflow, selectBackend BackendSelector, workspaceDir string, localRepositoryOverrides map[string]string, vars map[string]string, extraEnv map[string]string, eventName string, eventJSONDir string) (*WorkflowResult, error) {
```

In the `RunJob` call inside the matrix-combination goroutine, add the two new fields to the `JobRunOptions{}` literal:

```go
				jr, err := RunJob(runCtx, wf, &job, backend, JobRunOptions{
					Needs:                    outcomes,
					Matrix:                   combo,
					WorkspaceDir:             workspaceDir,
					LocalRepositoryOverrides: localRepositoryOverrides,
					Vars:                     vars,
					ExtraEnv:                 extraEnv,
					JobID:                    name,
					EventName:                eventName,
					EventJSONDir:             eventJSONDir,
				})
```

Find every existing call site in `internal/engine/workflow_run_test.go` (every `RunWorkflow(context.Background(), wf, ...)` call — run `grep -n "RunWorkflow(" internal/engine/workflow_run_test.go` to get the authoritative list) and append `, "push", ""` to each (empty `eventJSONDir` is valid — Task 4's `RunJob` treats it as "no event payload staged," matching every existing test's behavior unchanged since none of them care about `github.event`).

- [ ] **Step 4: Run the new test, then every RunWorkflow test**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go test ./internal/engine/... -run TestRunWorkflow -v -race`
Expected: PASS, every test including the new one and all the ones just updated with the two trailing args.

- [ ] **Step 5: Wire the CLI flags in `cmd/mirror/main.go`**

Add to `runMode`:

```go
	eventPath string
	eventName string
```

In `runMain`, add after the existing `debug` flag definition:

```go
	eventPath := fs.String("event-path", "", "path to a real event JSON file (default: a synthetic payload for the resolved event name)")
	eventName := fs.String("event-name", "", "github.event_name to use (default: the workflow's only on: trigger, or \"push\")")
```

Update the `runMode{...}` literal passed to `runCommand`:

```go
	return runCommand(rest[0], runMode{list: *list, graph: *graph, dryRun: *dryRun, workdir: *workdir, localRepositoryOverrides: overrides, vars: vars, debug: *debug, eventPath: *eventPath, eventName: *eventName})
```

- [ ] **Step 6: Resolve the effective event name and JSON in `runCommand`**

In `cmd/mirror/main.go`'s `runCommand`, after `wf, err := engine.Parse(data)` succeeds and before the `switch { case mode.list: ... }` block, add:

```go
	eventName := mode.eventName
	if eventName == "" {
		names, err := wf.OnEventNames()
		if err != nil {
			fmt.Fprintf(os.Stderr, "parse on: field: %v\n", err)
			return 1
		}
		if len(names) == 1 {
			eventName = names[0]
		} else {
			eventName = "push"
		}
	}
```

(This must run even for `--list`/`--graph` modes' early returns — but since it produces no side effects and those modes don't need `eventName` at all, it's harmless to compute unconditionally before the `switch`; simplest to just leave it here rather than special-casing around the early returns.)

Later, inside the existing `if !mode.dryRun { ... }` block (same block that starts the cache/artifact servers), after the `extraEnv` map is fully populated (after the `ACTIONS_STEP_DEBUG` block, before the cache server startup — or anywhere in that block; placement relative to the cache/artifact server setup doesn't matter since this is independent), add:

```go
	var eventJSON string
	if mode.eventPath != "" {
		data, err := os.ReadFile(mode.eventPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read event path: %v\n", err)
			return 1
		}
		eventJSON = string(data)
	} else {
		eventJSON = engine.DefaultEventPayload(eventName)
	}
	eventDir, err := os.MkdirTemp("", "mirror-event-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create event dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(eventDir)
	if err := os.WriteFile(filepath.Join(eventDir, "event.json"), []byte(eventJSON), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write event.json: %v\n", err)
		return 1
	}
```

`engine.DefaultEventPayload` is already exported (Task 3 defined it that way) since `cmd/mirror` is a different package and needs direct access to it.

Update the `engine.RunWorkflow(...)` call site:

```go
	result, err := engine.RunWorkflow(context.Background(), wf, selectBackend, workspaceDir, mode.localRepositoryOverrides, mode.vars, extraEnv, eventName, eventDir)
```

- [ ] **Step 7: Run the full test suite**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l . | grep -v third_party; go vet ./... && go test ./... -race 2>&1 | tail -25`
Expected: clean build, no unformatted files, no vet errors, every package `ok` under `-race`.

- [ ] **Step 8: Commit**

```bash
git add internal/engine/workflow_run.go internal/engine/workflow_run_test.go internal/engine/event_payloads.go internal/engine/event_payloads_test.go cmd/mirror/main.go
git commit -m "feat: wire --event-path/--event-name, resolve effective event name, thread through RunWorkflow"
```

---

### Task 6: Real end-to-end verification and docs

**Files:**
- Create: `examples/workflows/triggers.yml`
- Create: `examples/event-payloads/custom-pull-request.json`
- Modify: `docs/usage.md`
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Write the example workflow**

Create `examples/workflows/triggers.yml`:

```yaml
# Demonstrates github.event_name/github.event/GITHUB_EVENT_NAME/
# GITHUB_EVENT_PATH — real for a user-supplied --event-path, synthetic
# (but structurally real) by default otherwise.
#
# Try it (synthetic pull_request payload):
#   mirror run --event-name pull_request examples/workflows/triggers.yml
#
# Try it (real user-supplied payload):
#   mirror run --event-name pull_request --event-path examples/event-payloads/custom-pull-request.json examples/workflows/triggers.yml
name: triggers
on: [push, pull_request]
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: report event context
        run: |
          echo "event_name=$GITHUB_EVENT_NAME"
          echo "event_path=$GITHUB_EVENT_PATH"
          cat "$GITHUB_EVENT_PATH"
      - name: report pull_request fields via expressions
        if: github.event_name == 'pull_request'
        run: 'echo "PR #${{ github.event.pull_request.number }}: ${{ github.event.pull_request.title }} (${{ github.event.pull_request.head.ref }} -> ${{ github.event.pull_request.base.ref }})"'
```

- [ ] **Step 2: Write the real custom event payload**

Create `examples/event-payloads/custom-pull-request.json`:

```json
{
  "action": "opened",
  "number": 99,
  "pull_request": {
    "number": 99,
    "title": "A real custom payload overriding the synthetic default",
    "head": {"ref": "my-feature", "sha": "abc123"},
    "base": {"ref": "main", "sha": "def456"}
  }
}
```

- [ ] **Step 3: Run it for real — synthetic push default**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go run ./cmd/mirror run examples/workflows/triggers.yml 2>&1 | tail -20`
Expected: `event_name=push` (the workflow's `on:` has two entries, so it defaults to `"push"` per the priority chain — confirm this is actually what happens, not an error), `event_path=/mirror-event/event.json`, the file's contents show the synthetic push payload's real JSON.

- [ ] **Step 4: Run it for real — synthetic pull_request payload via `--event-name`**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go run ./cmd/mirror run --event-name pull_request examples/workflows/triggers.yml 2>&1 | tail -20`
Expected: the second step runs (its `if:` matches), printing `PR #1: Local test pull request (feature-branch -> main)` — the synthetic default payload's exact values.

- [ ] **Step 5: Run it for real — a real user-supplied `--event-path` overriding the synthetic default**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go run ./cmd/mirror run --event-name pull_request --event-path examples/event-payloads/custom-pull-request.json examples/workflows/triggers.yml 2>&1 | tail -20`
Expected: the second step now prints `PR #99: A real custom payload overriding the synthetic default (my-feature -> main)` — the real file's values, not the synthetic default's, proving the override actually works end-to-end.

If any of Steps 3-5 fail or show wrong values, root-cause it for real (temporary logging if needed) and fix the actual bug — likely candidates: the event-name resolution priority chain picking the wrong default, the `/mirror-event` staging not reaching the container correctly, or `resolveNestedPath`'s case-insensitive matching missing something real JSON produces.

- [ ] **Step 6: Update docs/usage.md**

Add a new bullet to "What's supported today" (near the `strategy.matrix`/concurrency bullets):

```
- **`github.event_name`/`github.event`/`GITHUB_EVENT_NAME`/
  `GITHUB_EVENT_PATH`** — real for a user-supplied `--event-path <file>`
  (loaded verbatim, matching act's own mechanism exactly); otherwise a
  synthetic but structurally real default payload (matching GitHub's own
  public webhook-payload shapes) for `push`, `pull_request`,
  `workflow_dispatch`, `workflow_call`, `repository_dispatch`, and
  `workflow_run` — any other trigger name defaults to `{}`, matching
  act's own default for the untyped case (act itself never fabricates a
  payload for any event, confirmed via source). `--event-name <name>`
  selects `github.event_name`; without it, the workflow's own `on:`
  block picks it when it names exactly one trigger, else it defaults to
  `"push"` — the same priority chain act uses. `github.event.*` supports
  arbitrary-depth expressions (`github.event.pull_request.head.ref`),
  not just the top-level fields. No `on: push: branches/paths` or
  `pull_request: types` filtering — matches act's own choice (dead,
  unwired code for this exists even in act's own source), and is
  inherently moot once a user has explicitly invoked a local run; the
  job's own `if:` conditions are the mechanism that still matters
  locally.
```

- [ ] **Step 7: Update CHANGELOG.md**

Add to the `### Added` section, above the most recent entry:

```markdown
- **Real triggers and event payloads.** Checked against act's actual
  event-loading mechanism (`pkg/runner/runner.go`'s `configure()`,
  `pkg/model/github_context.go`, `cmd/root.go`'s event-name resolution)
  rather than guessed: `--event-path <file>` loads a real user-supplied
  JSON payload exactly like act's own `-e`/`--eventpath` flag;
  `--event-name <name>` selects `github.event_name` via the same
  priority chain act uses (explicit flag, else the workflow's own single
  `on:` trigger, else `"push"`). Beyond what act itself does (confirmed
  via source: act never fabricates a payload for any event, always
  falling back to a bare `{}`), mirror-gha also ships synthetic but
  structurally real default payloads (matching GitHub's own public
  webhook documentation, since there's no single "correct" fake payload
  and act has no reference implementation for this at all) for `push`,
  `pull_request`, `workflow_dispatch`, `workflow_call`,
  `repository_dispatch`, and `workflow_run`. `github.event.*` gained
  genuine arbitrary-depth expression resolution
  (`github.event.pull_request.head.ref`) — the previous `github.*`
  context lookup only ever handled flat two-segment paths. No `on:
  push/pull_request` branches/paths/types filtering — matches act's own
  choice (confirmed dead, unwired pattern-matching code even in act's
  own source) and is inherently moot once a user has already explicitly
  invoked a local run; the job's own `if:` conditions remain the
  mechanism that matters locally. Verified for real: the exact same
  workflow run three ways — no flags (defaults to the synthetic `push`
  payload), `--event-name pull_request` (synthetic pull_request payload,
  real `github.event.pull_request.*` field access via `if:`/`run:`), and
  `--event-name pull_request --event-path <real file>` (confirming the
  real file's values override the synthetic default end-to-end, not just
  in isolated unit tests).
```

- [ ] **Step 8: Final full-suite check**

Run: `export PATH="/opt/homebrew/bin:$PATH" && go build ./... && gofmt -l . | grep -v third_party && go vet ./... && go test ./... -race 2>&1 | tail -25`
Expected: clean build, no unformatted files, no vet errors, every package `ok` under `-race`.

- [ ] **Step 9: Commit**

```bash
git add examples/workflows/triggers.yml examples/event-payloads/custom-pull-request.json docs/usage.md CHANGELOG.md
git commit -m "docs: document real triggers and event payloads, add example workflow"
```

(If Steps 3-5 surfaced and required a real bug fix, that fix should already be committed as its own commit before this one, with its own regression test — same discipline as every prior sub-project's final verification task this session.)
