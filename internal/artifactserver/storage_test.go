package artifactserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStore_WriteAtAndExists(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	if store.Exists("my-artifact/file.txt") {
		t.Fatal("Exists() = true before any write, want false")
	}

	if err := store.WriteAt("my-artifact/file.txt", 0, strings.NewReader("hello")); err != nil {
		t.Fatalf("WriteAt() error = %v", err)
	}
	if !store.Exists("my-artifact/file.txt") {
		t.Error("Exists() = false after write, want true")
	}

	path, err := store.Resolve("my-artifact/file.txt")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", data, "hello")
	}
}

func TestStore_WriteAtOffsetAppends(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	if err := store.WriteAt("chunked.txt", 0, strings.NewReader("hello")); err != nil {
		t.Fatalf("first WriteAt() error = %v", err)
	}
	if err := store.WriteAt("chunked.txt", 5, strings.NewReader(" world")); err != nil {
		t.Fatalf("second WriteAt() error = %v", err)
	}

	path, err := store.Resolve("chunked.txt")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("content = %q, want %q", data, "hello world")
	}
}

func TestStore_Resolve_RejectsPathTraversal(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	_, err = store.Resolve("../../etc/passwd")
	if err == nil {
		t.Fatal("Resolve() error = nil, want error for a path escaping the store root")
	}
}

func TestNewTempStore_CreatesRealDirectory(t *testing.T) {
	store, root, err := NewTempStore()
	if err != nil {
		t.Fatalf("NewTempStore() error = %v", err)
	}
	if root == "" {
		t.Fatal("root is empty")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("temp store root doesn't exist: %v", err)
	}

	if err := store.WriteAt("proof.txt", 0, strings.NewReader("x")); err != nil {
		t.Fatalf("WriteAt() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "proof.txt")); err != nil {
		t.Errorf("file not found under the returned root: %v", err)
	}
}
