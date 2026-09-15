package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestContainerAndServices_RealNetworkReachabilityAndImageSwap(t *testing.T) {
	requireDocker(t)
	requireNetwork(t)
	result := run(t, t.TempDir(), 120*time.Second, "run", examplePath("services-container.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "v20.") {
		t.Errorf("Stdout = %q, want a real Node 20.x version string (proving container: node:20 actually swapped the image)", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "accepting connections") {
		t.Errorf("Stdout = %q, want pg_isready's real success message (proving the postgres service is reachable by hostname)", result.Stdout)
	}
}

// TestMacOSBackend_HostOSDependentBehavior is the one test in this suite
// with a genuinely different expected outcome depending on the machine
// running it — matching macos-job.yml's own real, documented behavior:
// it only succeeds when mirror-gha itself is running on a Mac, and fails
// with a clear, specific error everywhere else. Both branches are real
// assertions, not a skip in one direction.
func TestMacOSBackend_HostOSDependentBehavior(t *testing.T) {
	if isDarwinHost() {
		requireNetwork(t) // the example's second step is a real JS action needing the pinned darwin Node download
		result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("macos-job.yml"))
		if result.ExitCode != 0 {
			t.Fatalf("ExitCode = %d, want 0 on a real Mac host (stderr: %s)", result.ExitCode, result.Stderr)
		}
		if !strings.Contains(result.Stdout, "Darwin") {
			t.Errorf("Stdout = %q, want real uname -s output showing Darwin (proving no container was involved)", result.Stdout)
		}
		if !strings.Contains(result.Stdout, "Hello mirror-gha!") {
			t.Errorf("Stdout = %q, want the real JS action's greeting, run natively", result.Stdout)
		}
		return
	}
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("macos-job.yml"))
	if result.ExitCode == 0 {
		t.Fatal("ExitCode = 0, want a non-zero exit on a non-Mac host — macos-latest requires running mirror-gha on a Mac")
	}
	if !strings.Contains(result.Stderr, "requires running mirror-gha on a Mac host") {
		t.Errorf("Stderr = %q, want the specific cross-host macOS error message", result.Stderr)
	}
}

func isDarwinHost() bool {
	return runtimeGOOS() == "darwin"
}

func TestTimeoutMinutes_RealEnforcementKillsLongRunningStep(t *testing.T) {
	requireDocker(t)
	start := time.Now()
	result := run(t, t.TempDir(), 30*time.Second, "run", testdataPath("timeout-minutes.yml"))
	elapsed := time.Since(start)
	if result.ExitCode == 0 {
		t.Fatal("ExitCode = 0, want a non-zero exit — the step's 30s sleep should never complete within a ~1.2s timeout-minutes")
	}
	if elapsed > 15*time.Second {
		t.Errorf("elapsed = %s, want well under 15s — a real timeout must actually stop the running step, not merely give up waiting on it after the fact", elapsed)
	}
	if strings.Contains(result.Stdout, "should never print") {
		t.Error("Stdout contains \"should never print\" — the timed-out step's command completed anyway, meaning the real subprocess was never actually killed")
	}
}

func TestDefaultsRunShell_JobLevelOverridesWorkflowLevel(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", testdataPath("defaults-precedence.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if strings.Contains(result.Stdout, "bash_version=\n") || !strings.Contains(result.Stdout, "bash_version=") {
		t.Errorf("Stdout = %q, want a non-empty BASH_VERSION (job-level defaults.run.shell: bash must override the workflow-level shell: sh)", result.Stdout)
	}
}

func TestContinueOnError_RealFailingStepDoesNotStopJob(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("continue-on-error.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "We got here even though the previous step failed") {
		t.Errorf("Stdout = %q, want the step after the continue-on-error: true failure to have actually run", result.Stdout)
	}
}
