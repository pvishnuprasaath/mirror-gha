package runner

import (
	"context"
	"fmt"
)

// StepResult is what a Backend reports after executing one step.
type StepResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// StepSpec is everything a Backend needs to execute one step, fully
// resolved (env merged, command already expression-substituted) by the
// caller.
type StepSpec struct {
	Command          string
	Shell            string
	Env              map[string]string
	WorkingDirectory string
	FilesDir         string // host dir bind-mounted for GITHUB_ENV/PATH/OUTPUT/STEP_SUMMARY
}

// Backend executes a single step on a given `runs-on` label.
type Backend interface {
	RunStep(ctx context.Context, spec StepSpec) (StepResult, error)
}

// ErrUnsupportedRunner is returned by SelectBackend for runner labels that
// don't have a working backend yet (Windows/macOS are Phase 2/3 of the
// design spec's roadmap).
type ErrUnsupportedRunner struct {
	RunsOn string
}

func (e *ErrUnsupportedRunner) Error() string {
	return fmt.Sprintf("runner %q is not supported yet (only ubuntu-latest/ubuntu-22.04/ubuntu-24.04 run today — see docs/superpowers/specs for the Phase 2/3 roadmap)", e.RunsOn)
}

// SelectBackend maps a job's `runs-on` value to a concrete Backend.
func SelectBackend(runsOn string) (Backend, error) {
	switch runsOn {
	case "ubuntu-latest", "ubuntu-24.04", "ubuntu-22.04":
		return NewLinuxDockerBackend(), nil
	default:
		return nil, &ErrUnsupportedRunner{RunsOn: runsOn}
	}
}
