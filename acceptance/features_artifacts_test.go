package acceptance

import (
	"strings"
	"testing"
	"time"
)

func TestArtifacts_V4RealUploadThenDownloadAcrossJobs(t *testing.T) {
	requireDocker(t)
	requireNetwork(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("uses-artifact-v4.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "hello from build (v4)") {
		t.Errorf("Stdout = %q, want the uploaded file's real content to round-trip through the download job", result.Stdout)
	}
}

func TestArtifacts_V3RealUploadThenDownloadAcrossJobs(t *testing.T) {
	requireDocker(t)
	requireNetwork(t)
	result := run(t, t.TempDir(), 60*time.Second, "run", examplePath("uses-artifact-v3.yml"))
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "hello from build (v3)") {
		t.Errorf("Stdout = %q, want the uploaded file's real content to round-trip through the download job via the legacy v3 protocol", result.Stdout)
	}
}
