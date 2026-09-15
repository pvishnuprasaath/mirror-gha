package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}

func TestHashFiles_SingleFileMatchesRealSHA256(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.sum"), "some content")

	got, err := hashFiles(root, []string{"go.sum"})
	if err != nil {
		t.Fatalf("hashFiles() error = %v", err)
	}
	want := sha256.Sum256([]byte("some content"))
	if got != hex.EncodeToString(want[:]) {
		t.Errorf("hashFiles() = %q, want %q", got, hex.EncodeToString(want[:]))
	}
}

func TestHashFiles_NoMatchesReturnsEmptyString(t *testing.T) {
	root := t.TempDir()

	got, err := hashFiles(root, []string{"nonexistent.txt"})
	if err != nil {
		t.Fatalf("hashFiles() error = %v", err)
	}
	if got != "" {
		t.Errorf("hashFiles() = %q, want empty string for zero matches", got)
	}
}

func TestHashFiles_DoubleStarMatchesNestedFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a", "b", "package-lock.json"), "lock-a")
	writeFile(t, filepath.Join(root, "c", "package-lock.json"), "lock-c")
	writeFile(t, filepath.Join(root, "other.txt"), "ignored")

	got, err := hashFiles(root, []string{"**/package-lock.json"})
	if err != nil {
		t.Fatalf("hashFiles() error = %v", err)
	}

	// Combined hash over both matched files' contents, in sorted relative
	// path order ("a/b/package-lock.json" < "c/package-lock.json").
	h := sha256.New()
	h.Write([]byte("lock-a"))
	h.Write([]byte("lock-c"))
	want := hex.EncodeToString(h.Sum(nil))
	if got != want {
		t.Errorf("hashFiles() = %q, want %q", got, want)
	}
}

func TestHashFiles_MultiplePatternsAreOred(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.sum"), "go-sum-content")
	writeFile(t, filepath.Join(root, "go.mod"), "go-mod-content")

	got, err := hashFiles(root, []string{"go.sum", "go.mod"})
	if err != nil {
		t.Fatalf("hashFiles() error = %v", err)
	}
	h := sha256.New()
	h.Write([]byte("go-mod-content")) // "go.mod" sorts before "go.sum"
	h.Write([]byte("go-sum-content"))
	want := hex.EncodeToString(h.Sum(nil))
	if got != want {
		t.Errorf("hashFiles() = %q, want %q", got, want)
	}
}

func TestEvalExpression_HashFilesReal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.sum"), "content")

	ctx := newTestContext()
	ctx.GitHub["workspace"] = root

	val, err := EvalExpression("hashFiles('go.sum')", ctx)
	if err != nil {
		t.Fatalf("EvalExpression() error = %v", err)
	}
	want := sha256.Sum256([]byte("content"))
	if val != hex.EncodeToString(want[:]) {
		t.Errorf("EvalExpression() = %v, want %q", val, hex.EncodeToString(want[:]))
	}
}
