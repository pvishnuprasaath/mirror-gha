package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestTriggers_DefaultResolvesToPushWhenOnHasTwoEntries(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", examplePath("triggers.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "event_name=push") {
		t.Errorf("Stdout = %q, want event_name=push (triggers.yml's on: [push, pull_request] has 2 entries, so the default falls back to \"push\")", result.Stdout)
	}
	if strings.Contains(result.Stdout, "PR #") {
		t.Errorf("Stdout = %q, want the pull_request-only step to be skipped when event_name is push", result.Stdout)
	}
}

func TestTriggers_EventNameFlagSelectsSyntheticPullRequestPayload(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", "--event-name", "pull_request", examplePath("triggers.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "event_name=pull_request") {
		t.Errorf("Stdout = %q, want event_name=pull_request", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "PR #1: Local test pull request (feature-branch -> main)") {
		t.Errorf("Stdout = %q, want the synthetic default pull_request payload's exact values", result.Stdout)
	}
}

func TestTriggers_EventPathOverridesSyntheticDefault(t *testing.T) {
	requireDocker(t)
	payloadPath := repoRoot() + "/examples/event-payloads/custom-pull-request.json"
	result := run(t, t.TempDir(), 30*time.Second, "run", "--event-name", "pull_request", "--event-path", payloadPath, examplePath("triggers.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "PR #99: A real custom payload overriding the synthetic default (my-feature -> main)") {
		t.Errorf("Stdout = %q, want the real user-supplied payload's values, not the synthetic default's", result.Stdout)
	}
	if strings.Contains(result.Stdout, "PR #1: Local test pull request") {
		t.Errorf("Stdout = %q, the synthetic default's values leaked through — --event-path did not override it", result.Stdout)
	}
}
