package engine

import (
	"context"
	"fmt"
	"strings"

	"mirror-gha/internal/actions"
)

// compositeInvocation is what prepareUsesStep resolves a composite
// (runs.using: composite) uses: step down to — its own nested step list
// (already converted from actions.ActionStep to engine.Step), its own
// declared outputs (each a value: expression, evaluated against the
// nested scope once the steps finish), and the env this composite
// invocation's own nested steps see (INPUT_*/GITHUB_ACTION_PATH) layered
// on top of the calling step's own env.
type compositeInvocation struct {
	Steps   []Step
	Outputs map[string]actions.ActionOutput
	BaseEnv map[string]string
}

// maxCompositeDepth caps composite-in-composite recursion. act itself has
// no such cap — its action references are content-addressed network
// fetches, making a self-referencing cycle rare. mirror-gha's local-path
// (./) resolution makes a composite action accidentally referencing
// itself a real, easy-to-hit crash (unbounded Go call-stack recursion)
// rather than a theoretical one; no real action nests anywhere near this
// deep, so the cap is cheap insurance, not a functional limitation.
const maxCompositeDepth = 10

// runCompositeSteps executes a composite action's own nested step list
// against a child Context — a fresh Steps map (so nested step IDs never
// collide with or leak into the caller's), Env seeded from the parent's
// env plus this composite's own INPUT_*/GITHUB_ACTION_PATH — and
// evaluates the composite's own declared outputs against that child
// scope once the nested steps finish, mirroring act's
// newCompositeRunContext and its post-execution rc.setOutput bridge.
// Nested steps never inherit the calling workflow/job's
// defaults.run.shell or working-directory (p.Job/p.Workflow are swapped
// for empty synthetic values here) — matches act's finding that a
// composite's synthetic RunContext has an empty Job(), so its Defaults
// always resolve empty.
func runCompositeSteps(ctx context.Context, p runStepParams, parentActx *Context, comp *compositeInvocation) (conclusion string, outputs map[string]string, stdout string, stderr string, err error) {
	if p.Depth+1 > maxCompositeDepth {
		return "", nil, "", "", fmt.Errorf("exceeded max composite action nesting depth (%d) — likely a self-referencing action", maxCompositeDepth)
	}

	childActx := &Context{
		GitHub: parentActx.GitHub,
		Env:    map[string]string{},
		Runner: parentActx.Runner,
		Steps:  map[string]StepOutcome{},
		Needs:  parentActx.Needs,
		Matrix: parentActx.Matrix,
		Vars:   parentActx.Vars,
	}
	for k, v := range parentActx.Env {
		childActx.Env[k] = v
	}
	for k, v := range comp.BaseEnv {
		childActx.Env[k] = v
	}

	childParams := runStepParams{
		Workflow:                 &Workflow{},
		Job:                      &Job{},
		RunnerJob:                p.RunnerJob,
		WorkspaceDir:             p.WorkspaceDir,
		NodeReady:                p.NodeReady,
		LocalRepositoryOverrides: p.LocalRepositoryOverrides,
		Depth:                    p.Depth + 1,
	}

	conclusion = "success"
	var stdoutBuilder, stderrBuilder strings.Builder
	for i, nestedStep := range comp.Steps {
		nestedID := nestedStep.ID
		if nestedID == "" {
			nestedID = fmt.Sprintf("step-%d", i)
		}
		report, err := runStep(ctx, childParams, childActx, nestedStep, nestedID)
		if err != nil {
			return "", nil, "", "", fmt.Errorf("nested step %s: %w", nestedID, err)
		}
		stdoutBuilder.WriteString(report.Stdout)
		stderrBuilder.WriteString(report.Stderr)
		if report.Conclusion == "failure" && !nestedStep.ContinueOnError {
			conclusion = "failure"
			break
		}
	}

	outputs = map[string]string{}
	for name, out := range comp.Outputs {
		val, err := SubstituteExpressions(out.Value, childActx)
		if err != nil {
			return "", nil, "", "", fmt.Errorf("evaluate output %q: %w", name, err)
		}
		outputs[name] = val
	}

	return conclusion, outputs, stdoutBuilder.String(), stderrBuilder.String(), nil
}
