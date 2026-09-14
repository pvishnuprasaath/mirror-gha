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
