package main

import (
	"os/exec"
	"testing"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping end-to-end test")
	}
}

func TestRunCommand_EndToEnd(t *testing.T) {
	requireDocker(t)

	exitCode := runCommand("testdata/simple.yml", runMode{})
	if exitCode != 0 {
		t.Fatalf("runCommand() = %d, want 0", exitCode)
	}
}

func TestRunCommand_List(t *testing.T) {
	// --list never touches Docker, so no requireDocker() guard needed.
	exitCode := runCommand("testdata/simple.yml", runMode{list: true})
	if exitCode != 0 {
		t.Fatalf("runCommand(list) = %d, want 0", exitCode)
	}
}

func TestRunCommand_Graph(t *testing.T) {
	exitCode := runCommand("testdata/simple.yml", runMode{graph: true})
	if exitCode != 0 {
		t.Fatalf("runCommand(graph) = %d, want 0", exitCode)
	}
}

func TestRunCommand_DryRun(t *testing.T) {
	// --dryrun still validates runs-on support but never calls Docker.
	exitCode := runCommand("testdata/simple.yml", runMode{dryRun: true})
	if exitCode != 0 {
		t.Fatalf("runCommand(dryrun) = %d, want 0", exitCode)
	}
}

func TestRunCommand_DryRunRejectsUnsupportedRunner(t *testing.T) {
	exitCode := runCommand("testdata/unsupported-runner.yml", runMode{dryRun: true})
	if exitCode != 1 {
		t.Fatalf("runCommand(dryrun) on an unsupported runner = %d, want 1", exitCode)
	}
}

func TestRunMain_ListFlag(t *testing.T) {
	exitCode := runMain([]string{"--list", "testdata/simple.yml"})
	if exitCode != 0 {
		t.Fatalf("runMain([--list, ...]) = %d, want 0", exitCode)
	}
}

func TestRunMain_NoWorkflowArgument(t *testing.T) {
	exitCode := runMain([]string{"--list"})
	if exitCode != 1 {
		t.Fatalf("runMain([--list]) with no workflow path = %d, want 1", exitCode)
	}
}

func TestRunMain_WorkdirFlag(t *testing.T) {
	requireDocker(t)

	exitCode := runMain([]string{"--workdir", t.TempDir(), "testdata/simple.yml"})
	if exitCode != 0 {
		t.Fatalf("runMain([--workdir, ...]) = %d, want 0", exitCode)
	}
}

func TestParseLocalRepositoryOverrides_ValidEntries(t *testing.T) {
	overrides, err := parseLocalRepositoryOverrides([]string{"actions/checkout@v4=/tmp/my-checkout"})
	if err != nil {
		t.Fatalf("parseLocalRepositoryOverrides() error = %v", err)
	}
	if overrides["actions/checkout@v4"] != "/tmp/my-checkout" {
		t.Errorf(`overrides["actions/checkout@v4"] = %q, want %q`, overrides["actions/checkout@v4"], "/tmp/my-checkout")
	}
}

func TestParseLocalRepositoryOverrides_MissingEqualsIsError(t *testing.T) {
	_, err := parseLocalRepositoryOverrides([]string{"actions/checkout@v4"})
	if err == nil {
		t.Fatal("parseLocalRepositoryOverrides() error = nil, want error for a value missing '='")
	}
}
