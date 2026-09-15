package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestKnownGap_WindowsRunnerReturnsClearError(t *testing.T) {
	result := run(t, t.TempDir(), 10*time.Second, "run", testdataPath("windows-unsupported.yml"))
	if result.ExitCode == 0 {
		t.Fatal("ExitCode = 0, want a non-zero exit — windows-latest has no backend")
	}
	if !strings.Contains(result.Stderr, "windows-latest") || !strings.Contains(result.Stderr, "not supported yet") {
		t.Errorf("Stderr = %q, want a clear error naming windows-latest as unsupported, not a silent wrong result", result.Stderr)
	}
}

func TestKnownGap_BranchesFilterIsAcceptedButNotEnforced(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", testdataPath("branches-filter.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 — on: push: branches: must parse without error even though it isn't enforced (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "ran despite an on: push: branches: filter") { // note: fixture text uses "does not"/"acts" (no apostrophes) to stay YAML-single-quote-safe
		t.Errorf("Stdout = %q, want the job to have run — documenting today's real, accepted behavior (branches:/paths:/types: sub-filters are parsed but not evaluated, matching act's own choice) as an explicit, enforced contract rather than an undocumented accident", result.Stdout)
	}
}
