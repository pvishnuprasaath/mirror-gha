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

	exitCode := runCommand("testdata/simple.yml")
	if exitCode != 0 {
		t.Fatalf("runCommand() = %d, want 0", exitCode)
	}
}
