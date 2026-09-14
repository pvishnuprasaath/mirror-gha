package cacheserver

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// CacheEntry is one committed cache entry — a real actions/cache save,
// findable by later restores.
type CacheEntry struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	Version   string    `json:"version"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store is a persistent, on-disk cache — blobs plus a JSON index — that
// survives across separate `mirror run` invocations. A cache's entire
// purpose is being restorable in a LATER run; an ephemeral per-invocation
// store would make actions/cache permanently miss.
type Store struct {
	mu      sync.Mutex
	root    string
	index   []CacheEntry
	pending map[string]CacheEntry // reserved but not yet committed
}

// OpenStore opens (creating if necessary) a cache store rooted at root.
// A missing index (a store that's never had anything committed to it)
// is not an error — it's just an empty store.
func OpenStore(root string) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "blobs"), 0o755); err != nil {
		return nil, fmt.Errorf("create cache store blobs dir: %w", err)
	}
	s := &Store{root: root, pending: map[string]CacheEntry{}}

	data, err := os.ReadFile(s.indexPath())
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("read cache index: %w", err)
	}
	if err := json.Unmarshal(data, &s.index); err != nil {
		return nil, fmt.Errorf("parse cache index: %w", err)
	}
	return s, nil
}

func (s *Store) indexPath() string {
	return filepath.Join(s.root, "index.json")
}

// BlobPath is where a cache entry's blob lives on disk, used both for
// writing (the upload handler) and reading (the download handler) —
// same path, keyed by id, whether or not it's been committed yet.
func (s *Store) BlobPath(id string) string {
	return filepath.Join(s.root, "blobs", id)
}

// Reserve allocates a new cache id for key/version, ready to receive
// uploaded bytes at BlobPath(id). Not yet visible to Find until Commit.
func (s *Store) Reserve(key, version string) (string, error) {
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", fmt.Errorf("generate cache id: %w", err)
	}
	id := hex.EncodeToString(idBytes)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[id] = CacheEntry{ID: id, Key: key, Version: version}
	return id, nil
}

// Commit finalizes a reserved cache entry, making it visible to Find and
// persisting the index to disk.
func (s *Store) Commit(id string, size int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.pending[id]
	if !ok {
		return fmt.Errorf("commit: unknown cache id %q (not reserved)", id)
	}
	delete(s.pending, id)
	entry.Size = size
	entry.CreatedAt = time.Now()
	s.index = append(s.index, entry)

	data, err := json.MarshalIndent(s.index, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache index: %w", err)
	}
	if err := os.WriteFile(s.indexPath(), data, 0o644); err != nil {
		return fmt.Errorf("write cache index: %w", err)
	}
	return nil
}

// Find looks up a cache entry matching keys (the primary key followed by
// restore-keys fallbacks, in the order the real @actions/cache client
// sends them) and version — mirrors act's own findCache
// (pkg/artifactcache/handler.go:372-404) exactly: for each key in order,
// try an exact match first; on a miss, an anchored-regex prefix match
// against every stored key (most-recently-created wins); only move to
// the next key if both fail for the current one. Returns (nil, nil) for
// a genuine cache miss — not an error.
func (s *Store) Find(keys []string, version string) (*CacheEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, key := range keys {
		var exact []CacheEntry
		for _, e := range s.index {
			if e.Key == key && e.Version == version {
				exact = append(exact, e)
			}
		}
		if len(exact) > 0 {
			sort.Slice(exact, func(i, j int) bool { return exact[i].CreatedAt.After(exact[j].CreatedAt) })
			result := exact[0]
			return &result, nil
		}

		pattern, err := regexp.Compile("^" + regexp.QuoteMeta(key))
		if err != nil {
			return nil, fmt.Errorf("compile restore-key pattern for %q: %w", key, err)
		}
		var prefixed []CacheEntry
		for _, e := range s.index {
			if e.Version == version && pattern.MatchString(e.Key) {
				prefixed = append(prefixed, e)
			}
		}
		if len(prefixed) > 0 {
			sort.Slice(prefixed, func(i, j int) bool { return prefixed[i].CreatedAt.After(prefixed[j].CreatedAt) })
			result := prefixed[0]
			return &result, nil
		}
	}
	return nil, nil
}

// Get looks up a committed entry directly by id, for the download handler.
func (s *Store) Get(id string) (*CacheEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.index {
		if e.ID == id {
			result := e
			return &result, true
		}
	}
	return nil, false
}
