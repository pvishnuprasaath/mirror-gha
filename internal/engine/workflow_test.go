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
