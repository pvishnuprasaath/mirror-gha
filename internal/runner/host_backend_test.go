package runner

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func requireDarwin(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("not running on darwin, skipping host backend test")
	}
}

func TestTranslateMirrorPath_RewritesMirrorPrefixedPaths(t *testing.T) {
	got := translateMirrorPath("/tmp/job-root", "/mirror-node")
	want := filepath.Join("/tmp/job-root", "/mirror-node")
	if got != want {
		t.Errorf("translateMirrorPath() = %q, want %q", got, want)
	}
}

func TestTranslateMirrorPath_LeavesOtherPathsAlone(t *testing.T) {
	got := translateMirrorPath("/tmp/job-root", "/usr/bin/node")
	if got != "/usr/bin/node" {
		t.Errorf("translateMirrorPath() = %q, want unchanged /usr/bin/node", got)
	}
}

func TestHostBackend_StartJob_RejectsContainerSpec(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	_, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), &ContainerSpec{Image: "node:20"}, nil)
	if err == nil {
		t.Fatal("StartJob() error = nil, want error rejecting container: on a macOS job")
	}
}

func TestHostBackend_StartJob_RejectsServices(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	_, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, map[string]ContainerSpec{
		"redis": {Image: "redis:7"},
	})
	if err == nil {
		t.Fatal("StartJob() error = nil, want error rejecting services: on a macOS job")
	}
}

func TestHostBackend_StartJob_PlatformIsDarwin(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	if job.Platform() != "darwin" {
		t.Errorf("Platform() = %q, want darwin", job.Platform())
	}
}

func TestHostBackend_Exec_RunsRealShellCommand(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	workspaceDir := t.TempDir()
	job, err := backend.StartJob(context.Background(), "test-job", workspaceDir, nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	filesDir, err := os.MkdirTemp(job.FilesRoot(), "step-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}

	result, err := job.Exec(context.Background(), StepSpec{
		Command:  "echo hello from host",
		Env:      map[string]string{},
		FilesDir: filesDir,
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "hello from host") {
		t.Errorf("Stdout = %q, want it to contain %q", result.Stdout, "hello from host")
	}
}

func TestHostBackend_Exec_DefaultsToRealBash(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	filesDir, err := os.MkdirTemp(job.FilesRoot(), "step-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}

	// BASH_VERSION is only set inside an actual bash process — this
	// fails under sh, proving the no-shell-specified default is bash,
	// not sh (the Docker backend's own default).
	result, err := job.Exec(context.Background(), StepSpec{
		Command:  `echo "version=$BASH_VERSION"`,
		Env:      map[string]string{},
		FilesDir: filesDir,
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if strings.Contains(result.Stdout, "version=\n") || !strings.Contains(result.Stdout, "version=") {
		t.Errorf("Stdout = %q, want a non-empty BASH_VERSION (proving the default shell is bash)", result.Stdout)
	}
}

func TestHostBackend_CopyToContainer_RewritesMirrorPath(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "marker.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := job.CopyToContainer(context.Background(), srcDir, "/mirror-node"); err != nil {
		t.Fatalf("CopyToContainer() error = %v", err)
	}

	hj := job.(*hostJob)
	copied := filepath.Join(hj.root, "/mirror-node", "marker.txt")
	data, err := os.ReadFile(copied)
	if err != nil {
		t.Fatalf("expected copied file at %s: %v", copied, err)
	}
	if string(data) != "hi" {
		t.Errorf("copied content = %q, want %q", data, "hi")
	}
}

func TestHostBackend_Exec_TranslatesMirrorPrefixedArgs(t *testing.T) {
	requireDarwin(t)

	backend := NewHostBackend()
	job, err := backend.StartJob(context.Background(), "test-job", t.TempDir(), nil, nil)
	if err != nil {
		t.Fatalf("StartJob() error = %v", err)
	}
	defer job.Stop(context.Background())

	// Stage a tiny "action" so Args[0] can be a real, executable file
	// under the translated /mirror-node path, proving Exec rewrites
	// Args entries the same way CopyToContainer rewrites its destination.
	srcDir := t.TempDir()
	scriptPath := filepath.Join(srcDir, "bin")
	if err := os.MkdirAll(scriptPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(scriptPath, "node"), []byte("#!/bin/sh\necho ran-fake-node\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := job.CopyToContainer(context.Background(), srcDir, "/mirror-node"); err != nil {
		t.Fatalf("CopyToContainer() error = %v", err)
	}

	filesDir, err := os.MkdirTemp(job.FilesRoot(), "step-")
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	result, err := job.Exec(context.Background(), StepSpec{
		Args:     []string{"/mirror-node/bin/node"},
		Env:      map[string]string{},
		FilesDir: filesDir,
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d, want 0 (stderr: %s)", result.ExitCode, result.Stderr)
	}
	if !strings.Contains(result.Stdout, "ran-fake-node") {
		t.Errorf("Stdout = %q, want it to contain %q (proving /mirror-node/bin/node resolved to the real translated path)", result.Stdout, "ran-fake-node")
	}
}
