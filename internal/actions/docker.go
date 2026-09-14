package actions

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var dockerImageTagSanitizer = regexp.MustCompile(`[^a-zA-Z0-9]+`)

// imageTagFor computes the tag a built action image is cached under —
// mirror-gha-<sanitized-actionRef>:latest, closely matching act's own
// tagging pattern (act-<sanitized>-dockeraction:latest, action.go) so
// `docker images` stays equally easy to cross-reference back to the
// action that owns a given local image.
func imageTagFor(actionRef string) (string, error) {
	if actionRef == "" {
		return "", fmt.Errorf("actionRef must not be empty")
	}
	sanitized := dockerImageTagSanitizer.ReplaceAllString(actionRef, "-")
	// Docker repository names must be lowercase — actionRef is usually
	// already lowercase (owner/repo@ref), but a mixed-case local path or
	// test fixture name would otherwise produce an invalid tag.
	return "mirror-gha-" + strings.ToLower(sanitized) + ":latest", nil
}

// ResolveDockerImage returns the image to run for a repo-based Docker
// action (runs.using: docker). runs.Image is either a docker://image:tag
// reference — used exactly as given, since `docker run` auto-pulls a
// missing image on demand (no explicit Pull() step, unlike act's
// ForcePull-gated one — mirror-gha has no force-pull flag yet, YAGNI) —
// or a Dockerfile path relative to hostSourceDir (the action's own source
// directory, never the job workspace), which gets built.
func ResolveDockerImage(ctx context.Context, hostSourceDir, actionRef string, runs ActionRuns) (string, error) {
	if strings.HasPrefix(runs.Image, "docker://") {
		return strings.TrimPrefix(runs.Image, "docker://"), nil
	}
	if runs.Image == "" {
		return "", fmt.Errorf("docker action has no runs.image set")
	}
	return BuildActionImage(ctx, hostSourceDir, runs.Image, actionRef)
}

// BuildActionImage builds dockerfileRelPath (relative to hostSourceDir)
// into an image tagged via imageTagFor(actionRef). A cache hit — an image
// already exists under that tag — skips the build entirely; no arch check
// (mirror-gha has a single Linux Docker backend) and no force-rebuild
// flag (YAGNI until a real need shows up).
func BuildActionImage(ctx context.Context, hostSourceDir, dockerfileRelPath, actionRef string) (string, error) {
	tag, err := imageTagFor(actionRef)
	if err != nil {
		return "", err
	}

	inspect := exec.CommandContext(ctx, "docker", "image", "inspect", tag)
	if err := inspect.Run(); err == nil {
		return tag, nil
	}

	dockerfile := filepath.Join(hostSourceDir, dockerfileRelPath)
	build := exec.CommandContext(ctx, "docker", "build", "-f", dockerfile, "-t", tag, hostSourceDir)
	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker build %s: %w: %s", dockerfile, err, out)
	}
	return tag, nil
}
