package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestNeeds_LinearDependencyPassesJobOutput(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("job-dependencies.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Deploying version 1.2.3") {
		t.Errorf("Stdout = %q, want the deploy job to read build's declared output via needs.build.outputs.version", result.Stdout)
	}
}

func TestNeeds_StepOutputPassingWithinOneJob(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("output-passing.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Building version 1.2.3") {
		t.Errorf("Stdout = %q, want a later step to read an earlier step's $GITHUB_OUTPUT value", result.Stdout)
	}
}

func TestNeeds_DiamondDependencyFanOutFanIn(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", testdataPath("needs-diamond.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	for _, want := range []string{"b saw from-a", "c saw from-a", "d running after b and c"} {
		if !strings.Contains(result.Stdout, want) {
			t.Errorf("Stdout missing %q\nfull stdout:\n%s", want, result.Stdout)
		}
	}
}
