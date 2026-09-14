package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"mirror-gha/internal/actions"
	"mirror-gha/internal/runner"
)

// prepareUsesStep resolves and stages a uses: step's action inside the
// running job — copying the pinned Node runtime in once per job, and this
// step's action source in fresh every time — and returns the exec command
// plus the extra env vars (INPUT_*, GITHUB_ACTION_PATH) to merge into the
// step's environment. actx is used only for expression substitution inside
// with: values; the caller still owns the workflow-command file protocol
// and output parsing, identical to run: steps.
func prepareUsesStep(ctx context.Context, job runner.Job, workspaceDir, stepID string, step Step, actx *Context, nodeReady *bool) (string, map[string]string, error) {
	with := map[string]string{}
	for k, v := range step.With {
		val, err := SubstituteExpressions(v, actx)
		if err != nil {
			return "", nil, fmt.Errorf("substitute with.%s: %w", k, err)
		}
		with[k] = val
	}

	ref, err := actions.ResolveUsesRef(step.Uses)
	if err != nil {
		return "", nil, err
	}

	cacheRoot, err := actions.CacheRoot()
	if err != nil {
		return "", nil, fmt.Errorf("resolve cache root: %w", err)
	}

	var hostSourceDir string
	if ref.Local {
		hostSourceDir = filepath.Join(workspaceDir, ref.LocalPath)
	} else {
		actionDir, err := actions.FetchRemote(ref.Owner, ref.Repo, ref.Ref, cacheRoot)
		if err != nil {
			return "", nil, fmt.Errorf("fetch action %s: %w", step.Uses, err)
		}
		hostSourceDir = actionDir
		if ref.Subpath != "" {
			hostSourceDir = filepath.Join(actionDir, ref.Subpath)
		}
	}

	metadata, err := actions.ParseMetadata(hostSourceDir)
	if err != nil {
		return "", nil, fmt.Errorf("parse action metadata for %s: %w", step.Uses, err)
	}

	if !strings.HasPrefix(metadata.Runs.Using, "node") {
		return "", nil, fmt.Errorf("action %s has runs.using=%q, which isn't supported yet (only JS/node actions run today)", step.Uses, metadata.Runs.Using)
	}

	if !*nodeReady {
		nodeDir, err := actions.EnsureNode(cacheRoot)
		if err != nil {
			return "", nil, fmt.Errorf("ensure node runtime: %w", err)
		}
		if err := job.CopyToContainer(ctx, nodeDir, actions.ContainerNodePath); err != nil {
			return "", nil, fmt.Errorf("copy node runtime into job: %w", err)
		}
		*nodeReady = true
	}

	containerActionPath := actions.ContainerActionPath(stepID)
	if err := job.CopyToContainer(ctx, hostSourceDir, containerActionPath); err != nil {
		return "", nil, fmt.Errorf("copy action %s into job: %w", step.Uses, err)
	}

	env := actions.InputEnv(metadata, with)
	env["GITHUB_ACTION_PATH"] = containerActionPath

	command := fmt.Sprintf("%s/bin/node %s/%s", actions.ContainerNodePath, containerActionPath, metadata.Runs.Main)
	return command, env, nil
}
