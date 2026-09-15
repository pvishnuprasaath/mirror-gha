package acceptance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// binary builds the real mirror CLI once per test run (shared by every
// test in this package) and returns its path. Every test in this suite
// shells out to this exact compiled artifact via os/exec — exactly as a
// real user invoking `mirror` from a shell would — rather than calling
// internal Go functions directly. This is a deliberate acceptance-test
// design choice: it exercises real CLI flag parsing and the real process
// boundary, and each subprocess gets its own real stdout/stderr, so these
// tests never need the fragile global os.Stdout swapping an in-process
// approach would require.
func binary(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mirror-acceptance-bin-")
		if err != nil {
			buildErr = err
			return
		}
		binPath = filepath.Join(dir, "mirror")
		cmd := exec.Command("go", "build", "-o", binPath, "./cmd/mirror")
		cmd.Dir = repoRoot()
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			buildErr = fmt.Errorf("build mirror binary: %w: %s", err, stderr.String())
		}
	})
	if buildErr != nil {
		t.Fatalf("binary() error = %v", buildErr)
	}
	return binPath
}

// repoRoot returns this repo's root, computed relative to this source
// file (acceptance/harness_test.go) so `go build ./cmd/mirror` and
// examplePath() resolve correctly regardless of the test runner's own
// working directory.
func repoRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Dir(filepath.Dir(thisFile)) // acceptance/ -> repo root
}

// examplePath resolves a filename under examples/workflows/ to its real
// absolute path.
func examplePath(name string) string {
	return filepath.Join(repoRoot(), "examples", "workflows", name)
}

// testdataPath resolves a filename under acceptance/testdata/ to its
// real absolute path.
func testdataPath(name string) string {
	return filepath.Join(repoRoot(), "acceptance", "testdata", name)
}

// repoRootRelative resolves rel against repoRoot() — used where a flag
// value itself must be an absolute path (e.g. --local-repository's
// local/path side), unlike examplePath/testdataPath which resolve a
// workflow file argument directly.
func repoRootRelative(rel string) string {
	return filepath.Join(repoRoot(), rel)
}

func runtimeGOOS() string { return runtime.GOOS }

// runResult is what run() captures from one real mirror invocation.
type runResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// run shells out to the real, compiled mirror binary with args, from
// workdir (the job workspace a real user would already be in) —
// capturing real stdout/stderr and the real process exit code. Fails the
// test outright (not just returning a non-zero ExitCode) only if the
// process couldn't be started/run at all (a real infrastructure problem,
// distinct from the workflow itself failing, which is a normal,
// assertable ExitCode).
func run(t *testing.T, workdir string, timeout time.Duration, args ...string) runResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary(t), args...)
	cmd.Dir = workdir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run mirror %v: %v (stderr: %s)", args, err, stderr.String())
		}
	}
	return runResult{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: exitCode}
}

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping acceptance test")
	}
}

func requireNetwork(t *testing.T) {
	t.Helper()
	if err := exec.Command("curl", "-sS", "-o", os.DevNull, "--max-time", "5", "https://nodejs.org").Run(); err != nil {
		t.Skipf("no network connectivity, skipping: %v", err)
	}
}

func requireDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("not running on darwin, skipping")
	}
}
