package engine

import (
	"context"
	"testing"

	"mirror-gha/internal/actions"
	"mirror-gha/internal/runner"
)

func TestRunCompositeSteps_NestedOutputBridgesToParent(t *testing.T) {
	requireDocker(t)

	backend := runner.NewLinuxDockerBackend()
	job, err := backend.StartJob(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	p := runStepParams{RunnerJob: job, WorkspaceDir: t.TempDir()}
	parentActx := NewContext(&Workflow{}, &Job{})

	comp := &compositeInvocation{
		Steps: []Step{
			{ID: "greet", Shell: "sh", Run: `echo "greeting=hi from composite" >> "$GITHUB_OUTPUT"`},
		},
		Outputs: map[string]actions.ActionOutput{
			"greeting": {Value: "${{ steps.greet.outputs.greeting }}"},
		},
		BaseEnv: map[string]string{},
	}

	conclusion, outputs, err := runCompositeSteps(context.Background(), p, parentActx, comp)
	if err != nil {
		t.Fatalf("runCompositeSteps() error = %v", err)
	}
	if conclusion != "success" {
		t.Errorf("conclusion = %q, want success", conclusion)
	}
	if outputs["greeting"] != "hi from composite" {
		t.Errorf(`outputs["greeting"] = %q, want %q`, outputs["greeting"], "hi from composite")
	}
	if _, leaked := parentActx.Steps["greet"]; leaked {
		t.Error(`parentActx.Steps["greet"] exists — nested step IDs must not leak into the parent's own steps context`)
	}
}

func TestRunCompositeSteps_ExceedsMaxDepth(t *testing.T) {
	p := runStepParams{Depth: maxCompositeDepth}
	parentActx := NewContext(&Workflow{}, &Job{})
	comp := &compositeInvocation{}

	_, _, err := runCompositeSteps(context.Background(), p, parentActx, comp)
	if err == nil {
		t.Fatal("runCompositeSteps() error = nil, want error for exceeding max nesting depth")
	}
}
