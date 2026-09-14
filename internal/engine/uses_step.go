package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"mirror-gha/internal/actions"
	"mirror-gha/internal/runner"
)

// usesStepPlan is what prepareUsesStep resolves a uses: step down to.
// Exactly one execution shape applies per step: a node action returns
// Args (exec'd directly into the job container, see runner.StepSpec's own
// doc comment for why no shell), a Docker action returns Docker (run as
// its own sibling container instead — see DockerActionSpec's doc comment
// for why it can't just be an Exec). Env carries INPUT_*/GITHUB_ACTION_PATH
// (plus runs.env for Docker actions) either way, merged into the step's
// env by the caller exactly like today.
type usesStepPlan struct {
	Args      []string
	Env       map[string]string
	Docker    *runner.DockerActionSpec
	Composite *compositeInvocation
	Post      *postAction
}

// prepareUsesStep resolves and stages a uses: step's action. actx is used
// only for expression substitution inside with: values; the caller still
// owns the workflow-command file protocol and output parsing, identical
// to run: steps.
func prepareUsesStep(ctx context.Context, p runStepParams, stepID string, step Step, actx *Context) (usesStepPlan, error) {
	with := map[string]string{}
	for k, v := range step.With {
		val, err := SubstituteExpressions(v, actx)
		if err != nil {
			return usesStepPlan{}, fmt.Errorf("substitute with.%s: %w", k, err)
		}
		with[k] = val
	}

	ref, err := actions.ResolveUsesRef(step.Uses)
	if err != nil {
		return usesStepPlan{}, err
	}

	// A raw docker://image:tag reference has no action.yml, no repo, no
	// source directory at all — the image itself is the action.
	// entrypoint/args come only from this step's own with: block.
	if ref.Docker {
		spec := &runner.DockerActionSpec{Image: ref.DockerImage}
		if v, ok := with["entrypoint"]; ok && v != "" {
			spec.Entrypoint = strings.Fields(v)
		}
		if v, ok := with["args"]; ok {
			spec.Args = strings.Fields(v)
		}
		return usesStepPlan{Docker: spec}, nil
	}

	cacheRoot, err := actions.CacheRoot()
	if err != nil {
		return usesStepPlan{}, fmt.Errorf("resolve cache root: %w", err)
	}

	var hostSourceDir string
	if ref.Local {
		hostSourceDir = filepath.Join(p.WorkspaceDir, ref.LocalPath)
	} else {
		overrideKey := ref.Owner + "/" + ref.Repo + "@" + ref.Ref
		if localPath, ok := p.LocalRepositoryOverrides[overrideKey]; ok {
			hostSourceDir = localPath
		} else {
			actionDir, err := actions.FetchRemote(ref.Owner, ref.Repo, ref.Ref, cacheRoot)
			if err != nil {
				return usesStepPlan{}, fmt.Errorf("fetch action %s: %w", step.Uses, err)
			}
			hostSourceDir = actionDir
			if ref.Subpath != "" {
				hostSourceDir = filepath.Join(actionDir, ref.Subpath)
			}
		}
	}

	metadata, err := actions.ParseMetadata(hostSourceDir)
	if err != nil {
		return usesStepPlan{}, fmt.Errorf("parse action metadata for %s: %w", step.Uses, err)
	}

	containerActionPath := actions.ContainerActionPath(stepID)

	switch {
	case strings.HasPrefix(metadata.Runs.Using, "node"):
		if !*p.NodeReady {
			nodeDir, err := actions.EnsureNode(cacheRoot)
			if err != nil {
				return usesStepPlan{}, fmt.Errorf("ensure node runtime: %w", err)
			}
			if err := p.RunnerJob.CopyToContainer(ctx, nodeDir, actions.ContainerNodePath); err != nil {
				return usesStepPlan{}, fmt.Errorf("copy node runtime into job: %w", err)
			}
			*p.NodeReady = true
		}
		if err := p.RunnerJob.CopyToContainer(ctx, hostSourceDir, containerActionPath); err != nil {
			return usesStepPlan{}, fmt.Errorf("copy action %s into job: %w", step.Uses, err)
		}
		env := actions.InputEnv(metadata, with)
		env["GITHUB_ACTION_PATH"] = containerActionPath
		args := []string{actions.ContainerNodePath + "/bin/node", containerActionPath + "/" + metadata.Runs.Main}

		plan := usesStepPlan{Args: args, Env: env}
		if metadata.Runs.Post != "" {
			postEnv := map[string]string{}
			for k, v := range env {
				postEnv[k] = v
			}
			plan.Post = &postAction{
				Args:   []string{actions.ContainerNodePath + "/bin/node", containerActionPath + "/" + metadata.Runs.Post},
				Env:    postEnv,
				PostIf: metadata.Runs.PostIf,
			}
		}
		return plan, nil

	case metadata.Runs.Using == "docker":
		image, err := actions.ResolveDockerImage(ctx, hostSourceDir, step.Uses, metadata.Runs)
		if err != nil {
			return usesStepPlan{}, fmt.Errorf("resolve docker image for %s: %w", step.Uses, err)
		}

		// with.args/with.entrypoint override runs.args/runs.entrypoint
		// wholesale, not merged. runs.args itself is never
		// expression-substituted here — no current caller needs it, and
		// doing it correctly would need this Docker action's own env (not
		// yet built at this point in the function, unlike composite
		// actions' BaseEnv) threaded into a temporary Context just for
		// this. with.args, already substituted above against the
		// workflow's own contexts like every other with: value, is the
		// supported way to parameterize a Docker action's arguments.
		entrypoint := metadata.Runs.Entrypoint
		if v, ok := with["entrypoint"]; ok {
			entrypoint = v
		}
		args := metadata.Runs.Args
		if v, ok := with["args"]; ok {
			args = strings.Fields(v)
		}

		env := actions.InputEnv(metadata, with)
		for k, v := range metadata.Runs.Env {
			env[k] = v
		}

		spec := &runner.DockerActionSpec{
			Image:                 image,
			Args:                  args,
			ActionSourceDir:       hostSourceDir,
			ActionPathInContainer: containerActionPath,
		}
		if entrypoint != "" {
			spec.Entrypoint = strings.Fields(entrypoint)
		}
		return usesStepPlan{Env: env, Docker: spec}, nil

	case metadata.Runs.Using == "composite":
		if err := p.RunnerJob.CopyToContainer(ctx, hostSourceDir, containerActionPath); err != nil {
			return usesStepPlan{}, fmt.Errorf("copy composite action %s into job: %w", step.Uses, err)
		}
		nestedSteps := make([]Step, len(metadata.Runs.Steps))
		for i, as := range metadata.Runs.Steps {
			nestedSteps[i] = Step{
				ID:               as.ID,
				Name:             as.Name,
				Run:              as.Run,
				Uses:             as.Uses,
				With:             as.With,
				Shell:            as.Shell,
				Env:              as.Env,
				If:               as.If,
				ContinueOnError:  as.ContinueOnError,
				WorkingDirectory: as.WorkingDirectory,
			}
		}
		baseEnv := actions.InputEnv(metadata, with)
		baseEnv["GITHUB_ACTION_PATH"] = containerActionPath
		return usesStepPlan{Composite: &compositeInvocation{
			Steps:   nestedSteps,
			Outputs: metadata.Outputs,
			BaseEnv: baseEnv,
		}}, nil

	default:
		return usesStepPlan{}, fmt.Errorf("action %s has runs.using=%q, which isn't supported (expected node*, docker, or composite)", step.Uses, metadata.Runs.Using)
	}
}
