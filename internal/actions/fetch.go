package actions

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// FetchRemote downloads and caches a Marketplace action's source at
// cacheRoot/actions/<owner>/<repo>/<ref>/, keyed by the literal ref string
// (a mutable branch ref won't auto-refresh — accepted, matches act's own
// cache-by-ref behavior). Uses curl + tar rather than Go's net/http/
// archive packages: this machine's network already broke Go's own TLS
// trust this session, while curl's Security-framework-backed stack
// handles it fine — see the design spec for the full reasoning.
func FetchRemote(owner, repo, ref, cacheRoot string) (string, error) {
	dest := filepath.Join(cacheRoot, "actions", owner, repo, ref)
	if _, err := os.Stat(filepath.Join(dest, "action.yml")); err == nil {
		return dest, nil
	}
	if _, err := os.Stat(filepath.Join(dest, "action.yaml")); err == nil {
		return dest, nil
	}

	tmpFile, err := os.CreateTemp("", "mirror-action-*.tar.gz")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	url := fmt.Sprintf("https://codeload.github.com/%s/%s/tar.gz/%s", owner, repo, ref)
	curl := exec.Command("curl", "-fsSL", "-o", tmpPath, url)
	if out, err := curl.CombinedOutput(); err != nil {
		return "", fmt.Errorf("download %s: %w: %s", url, err, out)
	}

	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("clear stale cache dir %s: %w", dest, err)
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir %s: %w", dest, err)
	}

	// codeload tarballs contain one top-level dir (e.g. repo-ref/) —
	// strip-components=1 flattens the action's own files directly into dest.
	tarCmd := exec.Command("tar", "-xzf", tmpPath, "-C", dest, "--strip-components=1")
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("extract %s: %w: %s", tmpPath, err, out)
	}

	return dest, nil
}
