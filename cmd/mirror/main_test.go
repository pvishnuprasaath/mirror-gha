package main

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestParseVarFlags_NameEqualsValue(t *testing.T) {
	vars := parseVarFlags([]string{"MY_VAR=hello"})
	if vars["MY_VAR"] != "hello" {
		t.Errorf(`vars["MY_VAR"] = %q, want %q`, vars["MY_VAR"], "hello")
	}
}

func TestParseVarFlags_BareNameIsEmptyValue(t *testing.T) {
	vars := parseVarFlags([]string{"MY_VAR"})
	val, ok := vars["MY_VAR"]
	if !ok || val != "" {
		t.Errorf(`vars["MY_VAR"] = (%q, %v), want ("", true)`, val, ok)
	}
}

func TestParseVarFile_SkipsBlankLinesAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".vars")
	content := "# a comment\n\nFOO=bar\nBARE_VAR\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write var file: %v", err)
	}

	vars, err := parseVarFile(path)
	if err != nil {
		t.Fatalf("parseVarFile() error = %v", err)
	}
	if vars["FOO"] != "bar" {
		t.Errorf(`vars["FOO"] = %q, want %q`, vars["FOO"], "bar")
	}
	if val, ok := vars["BARE_VAR"]; !ok || val != "" {
		t.Errorf(`vars["BARE_VAR"] = (%q, %v), want ("", true)`, val, ok)
	}
}

func TestParseVarFile_MissingDefaultFileIsNotAnError(t *testing.T) {
	vars, err := parseVarFile(filepath.Join(t.TempDir(), ".vars"))
	if err != nil {
		t.Fatalf("parseVarFile() error = %v, want nil for a missing default .vars file", err)
	}
	if len(vars) != 0 {
		t.Errorf("vars = %v, want empty", vars)
	}
}
