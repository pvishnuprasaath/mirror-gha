package actions

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// requireDocker skips the test if the docker CLI isn't installed — mirrors
// internal/runner's requireDocker(t) pattern (duplicated per package, same
// as requireNetwork already is between internal/actions and internal/engine).
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed, skipping Docker action test")
	}
}

func TestResolveDockerImage_DockerPrefixUsedDirectly(t *testing.T) {
	image, err := ResolveDockerImage(context.Background(), t.TempDir(), "some/action@v1", ActionRuns{Image: "docker://alpine:3.19"})
	if err != nil {
		t.Fatalf("ResolveDockerImage() error = %v", err)
	}
	if image != "alpine:3.19" {
		t.Errorf("image = %q, want %q", image, "alpine:3.19")
	}
}

func TestResolveDockerImage_EmptyImageIsError(t *testing.T) {
	_, err := ResolveDockerImage(context.Background(), t.TempDir(), "some/action@v1", ActionRuns{})
	if err == nil {
		t.Fatal("ResolveDockerImage() error = nil, want error when runs.image is empty")
	}
}

func TestBuildActionImage_BuildsAndCaches(t *testing.T) {
	requireDocker(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	actionRef := "test/docker-action@v1-" + t.Name()
	t.Cleanup(func() {
		tag, _ := imageTagFor(actionRef)
		exec.Command("docker", "rmi", tag).Run()
	})

	image, err := BuildActionImage(context.Background(), dir, "Dockerfile", actionRef)
	if err != nil {
		t.Fatalf("BuildActionImage() error = %v", err)
	}

	inspect := exec.Command("docker", "image", "inspect", image)
	if err := inspect.Run(); err != nil {
		t.Errorf("docker image inspect %s failed after build: %v", image, err)
	}

	image2, err := BuildActionImage(context.Background(), dir, "Dockerfile", actionRef)
	if err != nil {
		t.Fatalf("BuildActionImage() second call error = %v", err)
	}
	if image2 != image {
		t.Errorf("second BuildActionImage() = %q, want same tag %q", image2, image)
	}
}

func TestResolveDockerImage_DockerfilePathBuilds(t *testing.T) {
	requireDocker(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	actionRef := "test/via-resolve@v1-" + t.Name()
	t.Cleanup(func() {
		tag, _ := imageTagFor(actionRef)
		exec.Command("docker", "rmi", tag).Run()
	})

	image, err := ResolveDockerImage(context.Background(), dir, actionRef, ActionRuns{Image: "Dockerfile"})
	if err != nil {
		t.Fatalf("ResolveDockerImage() error = %v", err)
	}
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		t.Errorf("docker image inspect %s failed: %v", image, err)
	}
}
