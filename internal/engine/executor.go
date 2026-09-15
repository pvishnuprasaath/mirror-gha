package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mirror-gha/internal/commands"
	"mirror-gha/internal/runner"
)

// JobResult is the outcome of running every step in a job.
type JobResult struct {
	Conclusion string // "success", "failure", or "skipped"
	Steps      []StepReport
	Outputs    map[string]string
}

// StepReport is the per-step record inside a JobResult.
type StepReport struct {
	ID         string
	Name       string
	Conclusion string // "success", "failure", or "skipped"
	ExitCode   int
	Stdout     string
	Stderr     string
}

// JobRunOptions carries the parts of a job's execution context that come
// from the surrounding workflow (a multi-job DAG's `needs:` outcomes, one
// matrix combination, any locally-supplied `vars`, and the host workspace
// directory to mount) rather than the job definition itself.
type JobRunOptions struct {
	Needs                    map[string]JobOutcome
	Matrix                   MatrixCombination
	Vars                     map[string]string
	WorkspaceDir             string // host directory bind-mounted as the job's workspace
	LocalRepositoryOverrides map[string]string
	ExtraEnv                 map[string]string
	JobID                    string // names the job for Docker network naming when it has services:
	EventName                string // github.event_name / GITHUB_EVENT_NAME — empty means the caller didn't set one (RunWorkflow always sets it)
	EventJSONDir             string // host directory containing exactly one file, event.json — staged to /mirror-event via CopyToContainer, matching the existing /mirror-node convention. Empty means no event payload is staged.
}

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

// runStepParams bundles what varies between a job's own top-level steps
// and a composite action's nested ones — RunnerJob/WorkspaceDir/NodeReady/
// LocalRepositoryOverrides stay the same across an entire job (including
// into any composite action nested inside it), while Workflow/Job/Depth
// change for a composite's synthetic child execution (see
// composite_step.go's runCompositeSteps).
type runStepParams struct {
	Workflow                 *Workflow
	Job                      *Job
	RunnerJob                runner.Job
	WorkspaceDir             string
	NodeReady                *bool
	LocalRepositoryOverrides map[string]string
	Depth                    int
	// Masks accumulates every value registered via ::add-mask:: across
	// this job's steps (composite actions share the same pointer with
	// their parent — see composite_step.go), redacted from this and every
	// subsequent step's stdout/stderr, matching act's own per-job mask
	// registry.
	Masks *[]string
}

// RunJob executes every step of job in order against backend, evaluating
// `if:` conditions, substituting ${{ }} expressions in `run:` commands, and
// honoring `continue-on-error`. It stops starting new steps at the first
// unhandled failure, but always computes job.Outputs from whatever steps
// did run before returning.
//
// All steps run inside the same job-scoped environment (one container for
// the whole job, not one per step) so filesystem state — checked-out
// files, installed packages, PATH changes — persists step to step, the
// same way real GitHub Actions and act both work. opts.WorkspaceDir is
// bind-mounted into that environment and exposed as GITHUB_WORKSPACE /
// github.workspace / the default step working directory.
func RunJob(ctx context.Context, wf *Workflow, job *Job, backend runner.Backend, opts JobRunOptions) (*JobResult, error) {
	if opts.WorkspaceDir == "" {
		return nil, fmt.Errorf("JobRunOptions.WorkspaceDir must not be empty")
	}

	actx := NewContext(wf, job)
	actx.Needs = opts.Needs
	actx.Matrix = opts.Matrix
	actx.Vars = opts.Vars
	for k, v := range opts.ExtraEnv {
		actx.Env[k] = v
	}
	if token, ok := opts.ExtraEnv["GITHUB_TOKEN"]; ok {
		actx.Secrets["GITHUB_TOKEN"] = token
	}
	result := &JobResult{Conclusion: "success"}

	environmentSpec, err := job.Environment()
	if err != nil {
		return nil, fmt.Errorf("job environment: %w", err)
	}
	if environmentSpec != nil {
		actx.GitHub["environment"] = environmentSpec.Name
	}

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

	actx.GitHub["workspace"] = runnerJob.WorkspacePath()
	nodeReady := false
	var masks []string

	p := runStepParams{
		Workflow:                 wf,
		Job:                      job,
		RunnerJob:                runnerJob,
		WorkspaceDir:             opts.WorkspaceDir,
		NodeReady:                &nodeReady,
		LocalRepositoryOverrides: opts.LocalRepositoryOverrides,
		Masks:                    &masks,
	}

	var pendingPosts []pendingPostAction
	for i, step := range job.Steps {
		id := step.ID
		if id == "" {
			id = fmt.Sprintf("step-%d", i)
		}

		report, post, err := runStep(ctx, p, actx, step, id)
		if err != nil {
			return nil, err
		}
		result.Steps = append(result.Steps, report)
		if post != nil {
			pendingPosts = append(pendingPosts, *post)
		}

		if report.Conclusion == "failure" && !step.ContinueOnError {
			result.Conclusion = "failure"
			break
		}
	}

	// Post actions run once per job, in reverse step order, after every
	// one of the job's own top-level steps has finished — matching real
	// GitHub Actions' post-step lifecycle (e.g. actions/cache@v4 only
	// saves in its post entry point; its main entry point only
	// restores). Composite-nested uses: steps' own post actions aren't
	// collected here — a documented, accepted scope limit for now.
	for i := len(pendingPosts) - 1; i >= 0; i-- {
		pa := pendingPosts[i]

		runPost := true
		if pa.PostIf != "" {
			runPost, err = EvalBool(pa.PostIf, actx)
			if err != nil {
				return nil, fmt.Errorf("evaluate post-if for step %s: %w", pa.StepID, err)
			}
		}
		if !runPost {
			continue
		}

		stateVals, err := commands.ParseKeyValueFile(pa.StateFile)
		if err != nil {
			return nil, fmt.Errorf("parse state for step %s post: %w", pa.StepID, err)
		}

		env := map[string]string{}
		for k, v := range actx.Env {
			env[k] = v
		}
		for k, v := range pa.Env {
			env[k] = v
		}
		for k, v := range stateVals {
			env["STATE_"+k] = v
		}
		env["GITHUB_WORKSPACE"] = runnerJob.WorkspacePath()
		exportGitHubContextEnv(env, actx)

		postFilesDir, err := os.MkdirTemp(runnerJob.FilesRoot(), "post-")
		if err != nil {
			return nil, fmt.Errorf("create temp dir for step %s post: %w", pa.StepID, err)
		}
		postFileSet, err := commands.CreateFileSet(postFilesDir)
		if err != nil {
			return nil, fmt.Errorf("create workflow command files for step %s post: %w", pa.StepID, err)
		}

		stepResult, err := runnerJob.Exec(ctx, runner.StepSpec{
			Args:             pa.Args,
			Env:              env,
			WorkingDirectory: runnerJob.WorkspacePath(),
			FilesDir:         postFilesDir,
		})
		if err != nil {
			return nil, fmt.Errorf("run post for step %s: %w", pa.StepID, err)
		}

		envUpdates, err := commands.ParseKeyValueFile(postFileSet.EnvFile)
		if err != nil {
			return nil, fmt.Errorf("parse env updates for step %s post: %w", pa.StepID, err)
		}
		for k, v := range envUpdates {
			actx.Env[k] = v
		}

		postConclusion := "success"
		if stepResult.ExitCode != 0 {
			postConclusion = "failure"
		}
		result.Steps = append(result.Steps, StepReport{
			ID:         pa.StepID + "-post",
			Name:       "Post " + pa.StepID,
			Conclusion: postConclusion,
			ExitCode:   stepResult.ExitCode,
			Stdout:     stepResult.Stdout,
			Stderr:     stepResult.Stderr,
		})
	}

	outputs := map[string]string{}
	for name, expr := range job.Outputs {
		val, err := SubstituteExpressions(expr, actx)
		if err != nil {
			return nil, fmt.Errorf("evaluate job output %q: %w", name, err)
		}
		outputs[name] = val
	}
	result.Outputs = outputs

	return result, nil
}

// postAction is what prepareUsesStep returns for a node action with a
// non-empty runs.post — the JS action's own save/cleanup entry point.
// Executed once per job, after all of the job's own top-level steps
// finish, in reverse step order — matching real GitHub Actions' post-
// step lifecycle (e.g. actions/cache@v4 only saves in its post entry
// point; its main entry point only restores).
type postAction struct {
	Args   []string
	Env    map[string]string
	PostIf string
}

// pendingPostAction is a postAction plus the parts only known once the
// step has actually run: its real per-step GITHUB_STATE file (state
// passed from main to post, exactly like real actions/cache's main/post
// split relies on @actions/core's saveState/getState) and a display id.
type pendingPostAction struct {
	postAction
	StepID    string
	StateFile string
}

// runStep executes exactly one step — a run: command, a JS/Docker uses:
// step, or a composite uses: step (which recurses via runCompositeSteps,
// composite_step.go) — against actx, updating actx.Steps[id]/actx.Env as
// a side effect and returning this step's report. Shared by RunJob's
// top-level loop and, recursively, by a composite action's own nested
// step list, so "how a step runs" has exactly one implementation
// regardless of nesting depth. The second return value is non-nil only
// for a node action with a post entry point — RunJob's top-level loop
// collects these and runs them after the main loop; runCompositeSteps
// discards them (composite-nested post actions are a known, accepted
// scope limit for now).
func runStep(ctx context.Context, p runStepParams, actx *Context, step Step, id string) (StepReport, *pendingPostAction, error) {
	if step.Run != "" && step.Uses != "" {
		return StepReport{}, nil, fmt.Errorf("step %s: cannot set both run: and uses:", id)
	}

	if step.If != "" {
		ok, err := EvalBool(step.If, actx)
		if err != nil {
			return StepReport{}, nil, fmt.Errorf("evaluate if: for step %s: %w", id, err)
		}
		if !ok {
			actx.Steps[id] = StepOutcome{Outcome: "skipped"}
			return StepReport{ID: id, Name: step.Name, Conclusion: "skipped"}, nil, nil
		}
	}

	var command string
	var plan usesStepPlan
	var err error
	if step.Uses != "" {
		plan, err = prepareUsesStep(ctx, p, id, step, actx)
		if err != nil {
			return StepReport{}, nil, fmt.Errorf("prepare uses: step %s: %w", id, err)
		}
	} else {
		command, err = SubstituteExpressions(step.Run, actx)
		if err != nil {
			return StepReport{}, nil, fmt.Errorf("substitute expressions for step %s: %w", id, err)
		}
	}

	if plan.Composite != nil {
		conclusion, outputs, stdout, stderr, err := runCompositeSteps(ctx, p, actx, plan.Composite)
		if err != nil {
			return StepReport{}, nil, fmt.Errorf("composite step %s: %w", id, err)
		}
		actx.Steps[id] = StepOutcome{Outcome: conclusion, Outputs: outputs}
		return StepReport{ID: id, Name: step.Name, Conclusion: conclusion, Stdout: stdout, Stderr: stderr}, nil, nil
	}

	filesDir, err := os.MkdirTemp(p.RunnerJob.FilesRoot(), "step-")
	if err != nil {
		return StepReport{}, nil, fmt.Errorf("create temp dir for step %s: %w", id, err)
	}
	fileSet, err := commands.CreateFileSet(filesDir)
	if err != nil {
		return StepReport{}, nil, fmt.Errorf("create workflow command files for step %s: %w", id, err)
	}

	env := map[string]string{}
	for k, v := range actx.Env {
		env[k] = v
	}
	for k, v := range step.Env {
		env[k] = v
	}
	env["GITHUB_WORKSPACE"] = p.RunnerJob.WorkspacePath()
	exportGitHubContextEnv(env, actx)
	for k, v := range plan.Env {
		env[k] = v
	}

	workingDirectory := p.RunnerJob.WorkspacePath()
	if step.Uses == "" {
		if wd := effectiveWorkingDirectory(step, p.Job, p.Workflow); wd != "" {
			workingDirectory = wd
		}
	}

	stepCtx := ctx
	var cancel context.CancelFunc
	if step.TimeoutMinutes > 0 {
		stepCtx, cancel = context.WithTimeout(ctx, time.Duration(step.TimeoutMinutes*float64(time.Minute)))
	}

	var stepResult runner.StepResult
	if plan.Docker != nil {
		plan.Docker.Env = env
		plan.Docker.FilesDir = filesDir
		stepResult, err = p.RunnerJob.RunDockerAction(stepCtx, *plan.Docker)
	} else {
		stepResult, err = p.RunnerJob.Exec(stepCtx, runner.StepSpec{
			Command:          command,
			Args:             plan.Args,
			Shell:            effectiveShell(step, p.Job, p.Workflow),
			Env:              env,
			WorkingDirectory: workingDirectory,
			FilesDir:         filesDir,
		})
	}
	if cancel != nil {
		cancel()
	}
	if err != nil {
		return StepReport{}, nil, fmt.Errorf("run step %s: %w", id, err)
	}

	outputs, err := commands.ParseKeyValueFile(fileSet.OutputFile)
	if err != nil {
		return StepReport{}, nil, fmt.Errorf("parse outputs for step %s: %w", id, err)
	}
	// Many real-world actions (including GitHub's own
	// actions/hello-world-javascript-action) still emit outputs via
	// the deprecated stdout-based workflow commands rather than the
	// $GITHUB_OUTPUT file — real GitHub Actions parses both, so this
	// does too. The file-based outputs above win on key conflicts,
	// since that's the current, non-deprecated mechanism.
	for k, v := range commands.ParseLegacyOutputs(stepResult.Stdout) {
		if _, exists := outputs[k]; !exists {
			outputs[k] = v
		}
	}
	envUpdates, err := commands.ParseKeyValueFile(fileSet.EnvFile)
	if err != nil {
		return StepReport{}, nil, fmt.Errorf("parse env updates for step %s: %w", id, err)
	}
	for k, v := range envUpdates {
		actx.Env[k] = v
	}

	conclusion := "success"
	if stepResult.ExitCode != 0 {
		conclusion = "failure"
	}
	actx.Steps[id] = StepOutcome{Outcome: conclusion, Outputs: outputs}

	var pending *pendingPostAction
	if plan.Post != nil {
		pending = &pendingPostAction{postAction: *plan.Post, StepID: id, StateFile: fileSet.StateFile}
	}

	showDebug := actx.Env["ACTIONS_STEP_DEBUG"] == "true"
	stdout, stdoutMasks := commands.ProcessWorkflowCommands(stepResult.Stdout, showDebug)
	stderr, stderrMasks := commands.ProcessWorkflowCommands(stepResult.Stderr, showDebug)
	if p.Masks != nil {
		*p.Masks = append(*p.Masks, stdoutMasks...)
		*p.Masks = append(*p.Masks, stderrMasks...)
		stdout = commands.RedactMasks(stdout, *p.Masks)
		stderr = commands.RedactMasks(stderr, *p.Masks)
	}

	return StepReport{
		ID:         id,
		Name:       step.Name,
		Conclusion: conclusion,
		ExitCode:   stepResult.ExitCode,
		Stdout:     stdout,
		Stderr:     stderr,
	}, pending, nil
}

// exportGitHubContextEnv sets every string-valued github.* context entry
// as a real GITHUB_* env var too, not just inside ${{ }} expressions —
// matching real GitHub Actions' own behavior. Found missing via genuine
// end-to-end testing: actions/cache@v4's real save logic gates on
// process.env.GITHUB_REF existing at all (isValidEvent() in its bundled
// dist/save/index.js), which silently no-ops the entire save with only a
// warning if it's missing.
func exportGitHubContextEnv(env map[string]string, actx *Context) {
	for k, v := range actx.GitHub {
		if s, ok := v.(string); ok {
			env["GITHUB_"+strings.ToUpper(k)] = s
		}
	}
}

func effectiveShell(step Step, job *Job, wf *Workflow) string {
	if step.Shell != "" {
		return step.Shell
	}
	if job.Defaults != nil && job.Defaults.Run.Shell != "" {
		return job.Defaults.Run.Shell
	}
	if wf.Defaults != nil && wf.Defaults.Run.Shell != "" {
		return wf.Defaults.Run.Shell
	}
	return ""
}

func effectiveWorkingDirectory(step Step, job *Job, wf *Workflow) string {
	if step.WorkingDirectory != "" {
		return step.WorkingDirectory
	}
	if job.Defaults != nil && job.Defaults.Run.WorkingDirectory != "" {
		return job.Defaults.Run.WorkingDirectory
	}
	if wf.Defaults != nil && wf.Defaults.Run.WorkingDirectory != "" {
		return wf.Defaults.Run.WorkingDirectory
	}
	return ""
}
