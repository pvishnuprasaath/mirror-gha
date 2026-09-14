package artifactserver

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Store is where uploaded artifact blobs live on disk for one mirror run
// invocation. Unlike cacheserver.Store, it is never persisted across
// invocations — real GitHub Actions artifacts belong to one workflow
// run, not restored across later ones the way a dependency cache is.
type Store struct {
	root string
}

// OpenStore opens (creating if necessary) an artifact store rooted at root.
func OpenStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("create artifact store dir: %w", err)
	}
	return &Store{root: root}, nil
}

// NewTempStore creates a fresh store under a new OS temp directory and
// returns both the store and its root path — cmd/mirror prints this path
// so a user can inspect uploaded artifacts after the run finishes. It is
// never reused as a restore source by a later mirror run invocation.
func NewTempStore() (*Store, string, error) {
	root, err := os.MkdirTemp("", "mirror-artifacts-")
	if err != nil {
		return nil, "", fmt.Errorf("create artifact store temp dir: %w", err)
	}
	store, err := OpenStore(root)
	if err != nil {
		return nil, "", err
	}
	return store, root, nil
}

// Resolve returns the safe, absolute host path for a store-relative
// path, rejecting anything that would escape root (e.g. via ..),
// mirroring act's own path-traversal guard.
func (s *Store) Resolve(rel string) (string, error) {
	cleaned := filepath.Clean(rel)
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the artifact store root", rel)
	}
	return filepath.Join(s.root, cleaned), nil
}

// WriteAt writes r into the file at rel starting at offset, creating
// parent directories and the file itself as needed. offset 0 against a
// fresh file is the common case; a non-zero offset supports chunked
// (Content-Range) uploads.
func (s *Store) WriteAt(rel string, offset int64, r io.Reader) error {
	path, err := s.Resolve(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", rel, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", rel, err)
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek %s: %w", rel, err)
	}
	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	return nil
}

// Exists reports whether rel exists in the store.
func (s *Store) Exists(rel string) bool {
	path, err := s.Resolve(rel)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}
