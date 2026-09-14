package runner

import (
	"context"
	"fmt"
)

// StepResult is what a Job reports after executing one step.
type StepResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// StepSpec is everything a Job needs to execute one step, fully resolved
// (env merged, command already expression-substituted) by the caller.
type StepSpec struct {
	Command          string
	Shell            string
	Env              map[string]string
	WorkingDirectory string
	FilesDir         string // per-step host dir, must be created under Job.FilesRoot()
}

// Job is one job's execution environment: a single long-lived container (or
// equivalent), matching how real GitHub Actions and act both work — every
// step of a job execs into the same environment, so filesystem state
// (checked-out files, installed packages, PATH changes) persists step to
// step within a job. A fresh environment per *step* is a fidelity bug, not
// a simplification: it breaks nearly every real-world multi-step workflow.
type Job interface {
	// FilesRoot is the host directory backing the workflow-command files
	// (GITHUB_ENV/PATH/OUTPUT/STEP_SUMMARY). Callers must create each
	// step's FilesDir as a subdirectory of this root so the backend can
	// make it visible inside the running environment.
	FilesRoot() string
	// WorkspacePath is where the job's workspace (the bind-mounted host
	// directory `mirror run` was invoked from, or overridden via
	// --workdir) is visible from inside the running environment. Callers
	// use this for the GITHUB_WORKSPACE env var, the github.workspace
	// context value, and as the default step working directory.
	WorkspacePath() string
	Exec(ctx context.Context, spec StepSpec) (StepResult, error)
	Stop(ctx context.Context) error
}

// Backend starts a job's execution environment for a given `runs-on` label.
type Backend interface {
	// StartJob starts the environment, bind-mounting hostWorkspaceDir so
	// its contents are visible at Job.WorkspacePath() from inside it.
	StartJob(ctx context.Context, hostWorkspaceDir string) (Job, error)
}

// ErrUnsupportedRunner is returned by SelectBackend for runner labels that
// don't have a working backend yet (Windows/macOS are Phase 2/3 of the
// design spec's roadmap).
type ErrUnsupportedRunner struct {
	RunsOn string
}

func (e *ErrUnsupportedRunner) Error() string {
	return fmt.Sprintf("runner %q is not supported yet (only ubuntu-latest/ubuntu-22.04/ubuntu-24.04 run today — see docs/design/specs for the Phase 2/3 roadmap)", e.RunsOn)
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
