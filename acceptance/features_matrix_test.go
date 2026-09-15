package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestMatrix_IncludeExcludeRealMergeSemantics(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("matrix-include-exclude.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	want := []string{
		"os=ubuntu-latest version=16 experimental=true",
		"os=ubuntu-latest version=18 experimental=true",
		"os=windows-latest version=18 experimental=",
		"os=macos-latest version=20 experimental=",
	}
	for _, w := range want {
		if !strings.Contains(result.Stdout, w) {
			t.Errorf("Stdout missing expected combination line %q\nfull stdout:\n%s", w, result.Stdout)
		}
	}
	if strings.Contains(result.Stdout, "os=windows-latest version=16") {
		t.Error("windows-latest/16 should have been excluded, but its output line appeared")
	}
}

func TestMatrix_RealConcurrentExecutionIsFasterThanSequential(t *testing.T) {
	requireDocker(t)
	start := time.Now()
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("matrix-concurrency.yml"))
	elapsed := time.Since(start)
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	// 3 combinations x 2s sleep each, max-parallel: 3 -> all run at once.
	// A generous threshold (well under the ~6s+ strictly-sequential total,
	// allowing real headroom for Docker container startup overhead) proves
	// genuine concurrency without being timing-brittle in CI.
	if elapsed > 5*time.Second {
		t.Errorf("elapsed = %s, want well under 5s (3x2s sequential would be 6s+) — combinations should run concurrently", elapsed)
	}
}

func TestMatrix_RealFailFastStopsUnstartedCombinations(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", testdataPath("matrix-fail-fast.yml"))
	if result.ExitCode != 1 {
		t.Fatalf("ExitCode = %d, want 1 (matrix combination n=1 fails)", result.ExitCode)
	}
	if strings.Contains(result.Stdout, "combo 2 ran") || strings.Contains(result.Stdout, "combo 3 ran") {
		t.Errorf("Stdout = %q, want combinations 2 and 3 skipped after combination 1's real failure (max-parallel: 1, fail-fast defaults true)", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "skipped") {
		t.Errorf("Stdout = %q, want at least one combination reported as skipped", result.Stdout)
	}
}
