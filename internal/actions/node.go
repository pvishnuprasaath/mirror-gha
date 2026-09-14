package actions

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// PinnedNodeVersion is the single Node build used for every JS action,
// regardless of that action's declared runs.using (node16/node20/etc.) —
// matches act's own behavior (verified against its source: it doesn't
// resolve per-version Node binaries either, it just needs *a* Node
// present and uses it uniformly).
const PinnedNodeVersion = "20.11.1"

// ContainerNodePath is where the pinned Node runtime is copied inside a
// job container, once per job (see internal/engine's uses: step handling).
const ContainerNodePath = "/mirror-node"

// ContainerActionPath is where a given step's action source is copied
// inside the job container — one per uses: step, keyed by that step's ID
// so distinct actions in the same job never collide.
func ContainerActionPath(stepID string) string {
	return "/mirror-actions/" + stepID
}

func nodeArch() (string, error) {
	switch runtime.GOARCH {
	case "amd64":
		return "x64", nil
	case "arm64":
		return "arm64", nil
	default:
		return "", fmt.Errorf("unsupported architecture for the Node runtime: %s", runtime.GOARCH)
	}
}

// EnsureNode downloads and caches the pinned Node build at
// cacheRoot/node/<version>/<platform>/<arch>/, returning that directory.
// platform must be "linux" (Docker backend — the container's own OS,
// independent of whatever OS mirror-gha's own process runs on) or
// "darwin" (macOS host backend — mirror-gha's own process OS, since
// there's no container to target a different one). Assumes the target
// architecture matches the host's (true for default Docker Desktop
// behavior on Linux jobs, and trivially true for host-mode darwin jobs
// since there's no cross-arch concept there at all) — a documented,
// not-yet-configurable assumption.
func EnsureNode(cacheRoot, platform string) (string, error) {
	if platform != "linux" && platform != "darwin" {
		return "", fmt.Errorf("unsupported Node runtime platform: %q (want \"linux\" or \"darwin\")", platform)
	}

	arch, err := nodeArch()
	if err != nil {
		return "", err
	}

	dest := filepath.Join(cacheRoot, "node", PinnedNodeVersion, platform, arch)
	if _, err := os.Stat(filepath.Join(dest, "bin", "node")); err == nil {
		return dest, nil
	}

	tmpFile, err := os.CreateTemp("", "mirror-node-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	url := fmt.Sprintf("https://nodejs.org/dist/v%s/node-v%s-%s-%s.tar.gz", PinnedNodeVersion, PinnedNodeVersion, platform, arch)
	curl := exec.Command("curl", "-fsSL", "-o", tmpPath, url)
	if out, err := curl.CombinedOutput(); err != nil {
		return "", fmt.Errorf("download %s: %w: %s", url, err, out)
	}

	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("clear stale node cache dir %s: %w", dest, err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("create node cache dir %s: %w", dest, err)
	}

	tarCmd := exec.Command("tar", "-xzf", tmpPath, "-C", dest, "--strip-components=1")
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("extract %s: %w: %s", tmpPath, err, out)
	}

	return dest, nil
}
