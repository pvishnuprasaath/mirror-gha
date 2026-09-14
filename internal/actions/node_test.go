package actions

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureNode_DownloadsAndCaches(t *testing.T) {
	requireNetwork(t)

	cacheRoot := t.TempDir()
	dir, err := EnsureNode(cacheRoot, "linux")
	if err != nil {
		t.Fatalf("EnsureNode() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "node")); err != nil {
		t.Errorf("expected bin/node in %s: %v", dir, err)
	}

	dir2, err := EnsureNode(cacheRoot, "linux")
	if err != nil {
		t.Fatalf("EnsureNode() second call error = %v", err)
	}
	if dir2 != dir {
		t.Errorf("second EnsureNode() = %q, want same path %q", dir2, dir)
	}
}

func TestEnsureNode_DarwinPlatform(t *testing.T) {
	requireNetwork(t)

	cacheRoot := t.TempDir()
	dir, err := EnsureNode(cacheRoot, "darwin")
	if err != nil {
		t.Fatalf("EnsureNode() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "bin", "node")); err != nil {
		t.Errorf("expected bin/node in %s: %v", dir, err)
	}
}

func TestEnsureNode_InvalidPlatform(t *testing.T) {
	_, err := EnsureNode(t.TempDir(), "windows")
	if err == nil {
		t.Fatal("EnsureNode() error = nil, want error for an unsupported platform")
	}
}

func TestContainerActionPath(t *testing.T) {
	got := ContainerActionPath("emit")
	want := "/mirror-actions/emit"
	if got != want {
		t.Errorf("ContainerActionPath(emit) = %q, want %q", got, want)
	}
}
