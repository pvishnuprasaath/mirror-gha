package runner

import (
	"context"
	"fmt"
	"os"
)

// DryRunBackend fabricates a successful result for every step without
// touching Docker at all. It exists so `mirror run --dryrun` can exercise
// the real needs/matrix/if orchestration (RunWorkflow, RunJob) and report
// what would run, without executing anything. Callers still validate
// `runs-on` support via SelectBackend before falling back to this — a
// dry run on an unsupported runner still errors, since "would this even
// run here" is exactly what dry-run mode is for.
type DryRunBackend struct{}

func (DryRunBackend) StartJob(ctx context.Context) (Job, error) {
	dir, err := os.MkdirTemp("", "mirror-dryrun-")
	if err != nil {
		return nil, fmt.Errorf("create dry-run files root: %w", err)
	}
	return &dryRunJob{dir: dir}, nil
}

type dryRunJob struct{ dir string }

func (j *dryRunJob) FilesRoot() string { return j.dir }

func (j *dryRunJob) Exec(ctx context.Context, spec StepSpec) (StepResult, error) {
	return StepResult{ExitCode: 0, Stdout: "(dry run: not executed)\n"}, nil
}

func (j *dryRunJob) Stop(ctx context.Context) error {
	return os.RemoveAll(j.dir)
}
