package acceptance

import (
	"path/filepath"
	"testing"
	"time"
)

// corpusExclusions lists examples/workflows/*.yml files this generic
// smoke loop must not run with default flags — each has its own reason,
// documented inline, and its own dedicated test elsewhere in this suite
// that exercises it correctly.
var corpusExclusions = map[string]string{
	"macos-job.yml":             "correct exit code is host-OS-dependent (0 on a Mac, a specific error elsewhere) — see TestMacOSBackend_HostOSDependentBehavior in features_platform_test.go, not a generic 0-exit-code check",
	"uses-local-repository.yml": "requires --local-repository to succeed by design (it deliberately references a nonexistent Marketplace repo to prove the override replaces a real fetch, not runs alongside one) — see TestActions_LocalRepositoryOverrideSkipsRealFetch in features_actions_test.go",
}

// TestCorpus_EveryExampleWorkflowRunsSuccessfully globs every file under
// examples/workflows/ and runs each for real with default flags,
// asserting a clean exit. This is intentionally self-maintaining: a
// future new example file is automatically covered here with zero
// changes to this test, catching "did a change silently break any
// documented example" regressions for free as the corpus grows.
func TestCorpus_EveryExampleWorkflowRunsSuccessfully(t *testing.T) {
	requireDocker(t)
	requireNetwork(t) // several examples fetch real actions/images over the network

	matches, err := filepath.Glob(filepath.Join(repoRoot(), "examples", "workflows", "*.yml"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("Glob() found zero example workflows — expected at least one")
	}

	for _, path := range matches {
		name := filepath.Base(path)
		if reason, excluded := corpusExclusions[name]; excluded {
			t.Logf("skipping %s: %s", name, reason)
			continue
		}
		t.Run(name, func(t *testing.T) {
			// Every example's own header comment documents "Try it (from
			// the repo root)" as its intended usage — several reference
			// local actions by workspace-relative path
			// (./examples/workflows/actions/...) or the repo's own real
			// files, which only resolve correctly when the real repo
			// root is the job's workspace, not an arbitrary empty
			// tempdir. Demo artifacts these examples write into the repo
			// root (marker.txt, cache-data/, out/, downloaded/, etc.)
			// are already gitignored — this matches how every one of
			// these examples has actually been run and verified for
			// real throughout this project's own development.
			result := run(t, repoRoot(), 90*time.Second, "run", path)
			if result.ExitCode != 0 {
				t.Errorf("ExitCode = %d, want 0\nstdout:\n%s\nstderr:\n%s", result.ExitCode, result.Stdout, result.Stderr)
			}
		})
	}
}
