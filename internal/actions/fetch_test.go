package actions

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireNetwork skips the test if there's no working internet connection —
// mirrors internal/runner's requireDocker(t) pattern for an external
// dependency the test needs but can't assume is present. Deliberately
// checks via curl, not Go's net/http: this machine's network already
// broke Go's own TLS trust this session (a corporate TLS-inspecting proxy
// Go doesn't reliably trust, that curl handles fine) — checking
// connectivity with the same client FetchRemote itself uses avoids a
// false "no network" skip when a curl-based fetch would actually succeed.
func requireNetwork(t *testing.T) {
	t.Helper()
	if err := exec.Command("curl", "-sS", "-o", os.DevNull, "--max-time", "5", "https://codeload.github.com").Run(); err != nil {
		t.Skipf("no network connectivity, skipping: %v", err)
	}
}

func TestFetchRemote_DownloadsAndCaches(t *testing.T) {
	requireNetwork(t)

	cacheRoot := t.TempDir()
	// actions/hello-world-javascript-action is GitHub's own small, stable
	// official demo action — a real, public, unauthenticated fetch target.
	dir, err := FetchRemote("actions", "hello-world-javascript-action", "v1", cacheRoot)
	if err != nil {
		t.Fatalf("FetchRemote() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "action.yml")); err != nil {
		t.Errorf("expected action.yml in %s: %v", dir, err)
	}

	// Second call must hit the cache, not fetch again — prove this by
	// checking the directory is returned identically and still valid.
	dir2, err := FetchRemote("actions", "hello-world-javascript-action", "v1", cacheRoot)
	if err != nil {
		t.Fatalf("FetchRemote() second call error = %v", err)
	}
	if dir2 != dir {
		t.Errorf("second FetchRemote() = %q, want same path %q", dir2, dir)
	}
}

func TestFetchRemote_UnknownRepoIsError(t *testing.T) {
	requireNetwork(t)

	_, err := FetchRemote("mirror-gha-nonexistent-owner-xyz", "nonexistent-repo-xyz", "v1", t.TempDir())
	if err == nil {
		t.Fatal("FetchRemote() error = nil, want error for a nonexistent repo")
	}
}
