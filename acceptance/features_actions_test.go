package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestActions_RawDockerImageStepReadsWorkspaceFile(t *testing.T) {
	requireDocker(t)
	result := run(t, repoRoot(), 30*time.Second, "run", examplePath("uses-docker-image.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "written by a run: step") {
		t.Errorf("Stdout = %q, want the docker://alpine step's cat output of the marker file a prior run: step wrote", result.Stdout)
	}
}

func TestActions_CompositeActionBridgesNestedJSActionOutput(t *testing.T) {
	requireDocker(t)
	requireNetwork(t) // the composite action's nested step is itself a local JS action needing the pinned Node runtime download
	result := run(t, repoRoot(), 60*time.Second, "run", examplePath("uses-composite-action.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Composite action starting for mirror-gha") {
		t.Errorf("Stdout = %q, want the composite's own nested run: step output", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "Got greeting: Hello, mirror-gha!") {
		t.Errorf("Stdout = %q, want the nested JS action's output bridged up through the composite's outputs: block", result.Stdout)
	}
}

func TestActions_RealMarketplaceActionFetchedAndRun(t *testing.T) {
	requireDocker(t)
	requireNetwork(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("uses-marketplace-action.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Hello mirror-gha!") {
		t.Errorf("Stdout = %q, want the real actions/hello-world-javascript-action's real greeting", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "Action ran at:") {
		t.Errorf("Stdout = %q, want the step reading the action's own time output back", result.Stdout)
	}
}

func TestActions_LocalRepositoryOverrideSkipsRealFetch(t *testing.T) {
	requireDocker(t)
	// Deliberately NOT calling requireNetwork(t) — the whole point of this
	// scenario is that mirror-gha/does-not-exist@v1 is not a real,
	// fetchable repository; --local-repository must resolve it locally
	// instead of ever attempting a network fetch.
	overrideFlag := "mirror-gha/does-not-exist@v1=" + repoRootRelative("examples/workflows/actions/hello-action")
	result := run(t, t.TempDir(), 30*time.Second, "run", "--local-repository", overrideFlag, examplePath("uses-local-repository.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "Got greeting: Hello, override-works!") {
		t.Errorf("Stdout = %q, want the local override's action source used instead of a real (nonexistent) fetch", result.Stdout)
	}
}
