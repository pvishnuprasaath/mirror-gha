package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// clearPersistentCacheStore removes mirror-gha's real, cross-invocation
// cache store before this test runs, so it's repeatable regardless of
// what ran before it (including a previous run of this very test) — the
// store deliberately persists across separate `mirror run` invocations
// (unlike the artifact store), and uses-cache.yml's cache key is a fixed
// literal, so without this the "cold cache" assertion below would only
// ever be true the very first time this test ever runs on a given
// machine. This computes the same real path
// internal/actions.CacheRoot()/internal/cacheserver.StoreRoot() do,
// duplicated here rather than imported, to keep this suite's black-box
// contract intact — this is test environment setup, not a call into any
// code path under test.
func clearPersistentCacheStore(t *testing.T) {
	t.Helper()
	base, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir() error = %v", err)
	}
	if err := os.RemoveAll(filepath.Join(base, "mirror-gha", "action-cache")); err != nil {
		t.Fatalf("clear persistent cache store: %v", err)
	}
}

// TestCache_RealSaveThenRestoreAcrossTwoInvocations is the one scenario
// in this suite that runs the real binary TWICE against the SAME
// workdir, on purpose: actions/cache@v4's real save only happens via its
// post: entry point after the job's own steps finish, and its real
// restore only succeeds if a PRIOR mirror run invocation actually saved
// something to the persistent, cross-invocation cache store mirror-gha
// deliberately maintains (unlike the artifact store, which is
// deliberately fresh per invocation) — this can only be proven by two
// genuinely separate process invocations, not by asserting anything
// within a single run.
func TestCache_RealSaveThenRestoreAcrossTwoInvocations(t *testing.T) {
	requireDocker(t)
	requireNetwork(t)
	clearPersistentCacheStore(t)

	workdir := t.TempDir()

	first := run(t, workdir, 60*time.Second, "run", examplePath("uses-cache.yml"))
	if first.ExitCode != 0 {
		t.Fatalf("first run ExitCode = %d, want 0 (stderr: %s)", first.ExitCode, first.Stderr)
	}
	if !strings.Contains(first.Stdout, "cache-hit output: []") {
		t.Errorf("first run Stdout = %q, want cache-hit output: [] (a cold cache — this exact key has never been saved before)", first.Stdout)
	}

	second := run(t, workdir, 60*time.Second, "run", examplePath("uses-cache.yml"))
	if second.ExitCode != 0 {
		t.Fatalf("second run ExitCode = %d, want 0 (stderr: %s)", second.ExitCode, second.Stderr)
	}
	if !strings.Contains(second.Stdout, "cache-hit output: [true]") {
		t.Errorf("second run Stdout = %q, want cache-hit output: [true] — the first run's real post: save should make this a real restore hit, proving actual cross-invocation persistence, not simulated", second.Stdout)
	}
}
