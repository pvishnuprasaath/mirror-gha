package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestWorkflowCommands_RenderedMaskedAndDebugHiddenByDefault(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", testdataPath("workflow-commands.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (an ::error:: annotation must not fail the step by itself; stderr: %s)", result.ExitCode, result.Stderr)
	}
	for _, want := range []string{
		"▶ setup",
		"the token is ***",
		"❌ deliberate error annotation",
		"⚠️  deliberate warning annotation",
		"ℹ️  deliberate notice annotation",
		"second step also mentions ***",
	} {
		if !strings.Contains(result.Stdout, want) {
			t.Errorf("Stdout missing %q\nfull stdout:\n%s", want, result.Stdout)
		}
	}
	if strings.Contains(result.Stdout, "topsecret123") {
		t.Error("Stdout contains the unmasked secret value — ::add-mask:: redaction failed")
	}
	if strings.Contains(result.Stdout, "hidden by default") {
		t.Error("Stdout contains the ::debug:: line without --debug — it should be hidden by default")
	}
}

func TestWorkflowCommands_DebugShownWithFlag(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", "--debug", testdataPath("workflow-commands.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "🐛 hidden by default") {
		t.Errorf("Stdout = %q, want the ::debug:: line rendered when --debug is set", result.Stdout)
	}
}

func TestGitHubToken_SecretsContextAndEnvironmentName(t *testing.T) {
	requireDocker(t)
	result := run(t, t.TempDir(), 30*time.Second, "run", testdataPath("token-permissions-environment.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 — permissions: must be a no-op shim, not something that breaks the run (stderr: %s)", result.ExitCode, result.Stderr)
	}
	want := "env=ghs_mirror_gha_local_placeholder_token expr=ghs_mirror_gha_local_placeholder_token environment=production"
	if !strings.Contains(result.Stdout, want) {
		t.Errorf("Stdout = %q, want %q", result.Stdout, want)
	}
}
