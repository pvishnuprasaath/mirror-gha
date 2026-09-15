package engine

import "testing"

func TestParse_SingleJobSingleStep(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: say hello
        run: echo hello
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if wf.Name != "sample" {
		t.Errorf("Name = %q, want %q", wf.Name, "sample")
	}
	job, ok := wf.Jobs["build"]
	if !ok {
		t.Fatalf("job %q not found", "build")
	}
	if job.RunsOn != "ubuntu-latest" {
		t.Errorf("RunsOn = %q, want %q", job.RunsOn, "ubuntu-latest")
	}
	if len(job.Steps) != 1 {
		t.Fatalf("len(Steps) = %d, want 1", len(job.Steps))
	}
	if job.Steps[0].Run != "echo hello" {
		t.Errorf("Steps[0].Run = %q, want %q", job.Steps[0].Run, "echo hello")
	}
}

func TestParse_NeedsAsScalarOrList(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  a:
    runs-on: ubuntu-latest
    steps: [{run: echo a}]
  b:
    runs-on: ubuntu-latest
    needs: a
    steps: [{run: echo b}]
  c:
    runs-on: ubuntu-latest
    needs: [a, b]
    steps: [{run: echo c}]
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got := []string(wf.Jobs["b"].Needs); len(got) != 1 || got[0] != "a" {
		t.Errorf("Jobs[b].Needs = %v, want [a] (scalar form)", got)
	}
	if got := []string(wf.Jobs["c"].Needs); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("Jobs[c].Needs = %v, want [a b] (list form)", got)
	}
}

func TestParse_MatrixAndDefaultsAndTimeout(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
defaults:
  run:
    shell: bash
jobs:
  build:
    runs-on: ubuntu-latest
    timeout-minutes: 5
    defaults:
      run:
        working-directory: ./sub
    strategy:
      fail-fast: false
      matrix:
        version: [16, 18]
    outputs:
      greeting: hello
    steps:
      - run: echo hi
        timeout-minutes: 1
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if wf.Defaults == nil || wf.Defaults.Run.Shell != "bash" {
		t.Errorf("workflow-level Defaults.Run.Shell = %v, want %q", wf.Defaults, "bash")
	}
	job := wf.Jobs["build"]
	if job.TimeoutMinutes != 5 {
		t.Errorf("job.TimeoutMinutes = %v, want 5", job.TimeoutMinutes)
	}
	if job.Defaults == nil || job.Defaults.Run.WorkingDirectory != "./sub" {
		t.Errorf("job.Defaults.Run.WorkingDirectory = %v, want %q", job.Defaults, "./sub")
	}
	if job.Strategy == nil || job.Strategy.FailFast == nil || *job.Strategy.FailFast != false {
		t.Errorf("job.Strategy.FailFast = %v, want false", job.Strategy)
	}
	if job.Outputs["greeting"] != "hello" {
		t.Errorf(`job.Outputs["greeting"] = %q, want "hello"`, job.Outputs["greeting"])
	}
	if job.Steps[0].TimeoutMinutes != 1 {
		t.Errorf("job.Steps[0].TimeoutMinutes = %v, want 1", job.Steps[0].TimeoutMinutes)
	}
}

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
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    container: node:20
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
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
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
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
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
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

func TestJob_Environment_Absent(t *testing.T) {
	job := &Job{}
	spec, err := job.Environment()
	if err != nil {
		t.Fatalf("Environment() error = %v", err)
	}
	if spec != nil {
		t.Errorf("Environment() = %v, want nil for a job with no environment: field", spec)
	}
}

func TestJob_Environment_BareName(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    environment: production
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]
	spec, err := job.Environment()
	if err != nil {
		t.Fatalf("Environment() error = %v", err)
	}
	if spec == nil || spec.Name != "production" {
		t.Fatalf("Environment() = %+v, want Name=production", spec)
	}
}

func TestJob_Environment_Mapping(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    environment:
      name: production
      url: https://example.com
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]
	spec, err := job.Environment()
	if err != nil {
		t.Fatalf("Environment() error = %v", err)
	}
	if spec == nil || spec.Name != "production" || spec.URL != "https://example.com" {
		t.Fatalf("Environment() = %+v, want Name=production URL=https://example.com", spec)
	}
}

func TestJob_Permissions_Absent(t *testing.T) {
	job := &Job{}
	spec, err := job.Permissions()
	if err != nil {
		t.Fatalf("Permissions() error = %v", err)
	}
	if spec != nil {
		t.Errorf("Permissions() = %v, want nil for a job with no permissions: field", spec)
	}
}

func TestJob_Permissions_BareString(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    permissions: read-all
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]
	spec, err := job.Permissions()
	if err != nil {
		t.Fatalf("Permissions() error = %v", err)
	}
	if spec == nil || spec.All != "read-all" {
		t.Fatalf("Permissions() = %+v, want All=read-all", spec)
	}
}

func TestJob_Permissions_Mapping(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      issues: write
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]
	spec, err := job.Permissions()
	if err != nil {
		t.Fatalf("Permissions() error = %v", err)
	}
	if spec == nil || spec.Scopes["contents"] != "read" || spec.Scopes["issues"] != "write" {
		t.Fatalf("Permissions() = %+v, want Scopes={contents:read, issues:write}", spec)
	}
}

func TestWorkflow_Permissions_BareString(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
permissions: write-all
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
	spec, err := wf.Permissions()
	if err != nil {
		t.Fatalf("Permissions() error = %v", err)
	}
	if spec == nil || spec.All != "write-all" {
		t.Fatalf("Permissions() = %+v, want All=write-all", spec)
	}
}

func TestJob_Concurrency_Absent(t *testing.T) {
	job := &Job{}
	spec, err := job.Concurrency()
	if err != nil {
		t.Fatalf("Concurrency() error = %v", err)
	}
	if spec != nil {
		t.Errorf("Concurrency() = %v, want nil for a job with no concurrency: field", spec)
	}
}

func TestJob_Concurrency_BareString(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    concurrency: deploy-shared
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]
	spec, err := job.Concurrency()
	if err != nil {
		t.Fatalf("Concurrency() error = %v", err)
	}
	if spec == nil || spec.Group != "deploy-shared" || spec.CancelInProgress {
		t.Fatalf("Concurrency() = %+v, want Group=deploy-shared CancelInProgress=false", spec)
	}
}

func TestJob_Concurrency_Mapping(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
jobs:
  build:
    runs-on: ubuntu-latest
    concurrency:
      group: deploy-${{ matrix.env }}
      cancel-in-progress: true
    steps:
      - run: echo hi
`)
	wf, err := Parse(yaml)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	job := wf.Jobs["build"]
	spec, err := job.Concurrency()
	if err != nil {
		t.Fatalf("Concurrency() error = %v", err)
	}
	if spec == nil || spec.Group != "deploy-${{ matrix.env }}" || !spec.CancelInProgress {
		t.Fatalf("Concurrency() = %+v, want Group=deploy-${{ matrix.env }} CancelInProgress=true", spec)
	}
}

func TestWorkflow_Concurrency_BareString(t *testing.T) {
	yaml := []byte(`
name: sample
on: workflow_dispatch
concurrency: whole-run
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
	spec, err := wf.Concurrency()
	if err != nil {
		t.Fatalf("Concurrency() error = %v", err)
	}
	if spec == nil || spec.Group != "whole-run" {
		t.Fatalf("Concurrency() = %+v, want Group=whole-run", spec)
	}
}

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

func TestParse_NoJobs(t *testing.T) {
	yaml := []byte(`
name: empty
on: workflow_dispatch
jobs: {}
`)
	_, err := Parse(yaml)
	if err == nil {
		t.Fatal("Parse() error = nil, want error for workflow with no jobs")
	}
}
