# Cache Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `actions/cache` (save/restore) works for real against a new local HTTP server matching act's own cache API surface and restore-keys matching semantics — the real, unmodified `actions/cache@v4` action runs through mirror-gha's existing JS-actions machinery with zero special-casing, and a cache saved on one `mirror run` invocation is genuinely restorable on a later one.

**Architecture:** A new package, `internal/cacheserver`, owns three layers — a persistent on-disk `Store` (blobs + a JSON index, restore-keys matching), stdlib-`net/http` route handlers matching act's real wire format, and a `Server` wrapping their lifecycle (start once per `mirror run` invocation, not per-job). `cmd/mirror/main.go` starts/stops it around `engine.RunWorkflow` and injects `ACTIONS_CACHE_URL`/`ACTIONS_RUNTIME_TOKEN` through a new `JobRunOptions.ExtraEnv` field, merged into every step's env at the same layer `wf.Env`/`job.Env` already merge at — no new action-type dispatch, since `actions/cache` is itself a JS action.

**Tech Stack:** Go 1.27 stdlib only (`net/http.ServeMux`'s Go 1.22+ method+path routing, `crypto/rand`+`encoding/hex` for IDs) — no third-party router, unlike act's `httprouter`. No new dependencies.

**Spec:** `docs/design/specs/2026-09-14-mirror-gha-design.md`, "Cache Runtime" section (and the corrected "Artifacts and cache must be real local HTTP servers" note in "Data flow").

## Global Constraints

- Artifacts (`upload-artifact`/`download-artifact`, v3+v4) are explicitly out of scope — a separate, still-unscheduled follow-up.
- Cache store persists across `mirror run` invocations at `~/.cache/mirror-gha/action-cache/` (via `actions.CacheRoot()` + `"action-cache"`) — an ephemeral per-run store would make `actions/cache` permanently miss and defeat the feature.
- Restore-keys matching mirrors act's real `findCache` exactly: for each key in the ordered list, exact match first, then anchored-regex prefix match, most-recently-created wins; move to the next key only if both fail for the current one. Not simplified away — real workflows depend on this exact behavior.
- Cache server starts once per `mirror run` invocation (before any job runs), not per-job — matches act's lifecycle, not mirror-gha's per-job Docker-action container model.
- Server binds `0.0.0.0:0` (OS-assigned port); a job container reaches it via `host.docker.internal:<port>` — a deliberate divergence from act's outbound-IP-binding trick, chosen because this project's actual dev/test environment is macOS Docker Desktop. Documented as a known Linux-dockerd gap, not silently assumed to work everywhere.
- `ACTIONS_CACHE_URL`/`ACTIONS_RUNTIME_TOKEN` are injected at the same shared env layer every step type already uses — no per-step-type branching.
- The exact request/response wire format (field names, `Content-Range` handling) is based on `@actions/cache`'s documented historical behavior, not independently re-verified against its current source this session — Task 5's real end-to-end run is what actually proves or disproves it; fix real mismatches found there rather than assuming Tasks 1-2's guesses are correct.
- TDD throughout: every task writes the failing test before the implementation.
- Docker-dependent tests skip gracefully via the existing `requireDocker(t)` pattern (duplicated per-package).

---

### Task 1: `internal/cacheserver` — persistent store + restore-keys matching

**Files:**
- Create: `internal/cacheserver/store.go`
- Test: `internal/cacheserver/store_test.go`

**Interfaces:**
- Produces: `type CacheEntry struct{ ID, Key, Version string; Size int64; CreatedAt time.Time }`, `func OpenStore(root string) (*Store, error)`, `func (s *Store) Reserve(key, version string) (string, error)`, `func (s *Store) BlobPath(id string) string`, `func (s *Store) Commit(id string, size int64) error`, `func (s *Store) Find(keys []string, version string) (*CacheEntry, error)`, `func (s *Store) Get(id string) (*CacheEntry, bool)`

- [ ] **Step 1: Write the failing tests**

```go
// internal/cacheserver/store_test.go
package cacheserver

import (
	"testing"
	"time"
)

func TestStore_ReserveCommitFind_ExactMatch(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	id, err := store.Reserve("npm-deps-abc123", "v1")
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.Commit(id, 42); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	entry, err := store.Find([]string{"npm-deps-abc123"}, "v1")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if entry == nil {
		t.Fatal("Find() = nil, want a match")
	}
	if entry.ID != id || entry.Key != "npm-deps-abc123" || entry.Size != 42 {
		t.Errorf("entry = %+v, want ID=%q Key=npm-deps-abc123 Size=42", entry, id)
	}
}

func TestStore_Find_GenuineMissReturnsNilNoError(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	entry, err := store.Find([]string{"nothing-stored"}, "v1")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if entry != nil {
		t.Errorf("Find() = %+v, want nil for a genuine miss", entry)
	}
}

func TestStore_Find_PrefixMatchOnRestoreKey(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	id, err := store.Reserve("npm-deps-abc123", "v1")
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.Commit(id, 10); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	// keys[0] ("npm-deps-xyz999") has no exact match and no prefix
	// match; keys[1] ("npm-deps-") has no exact match but does
	// prefix-match the stored "npm-deps-abc123" — the ordered-list
	// semantics, not just a single-key lookup.
	entry, err := store.Find([]string{"npm-deps-xyz999", "npm-deps-"}, "v1")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if entry == nil || entry.ID != id {
		t.Errorf("Find() = %+v, want the npm-deps-abc123 entry via prefix match", entry)
	}
}

func TestStore_Find_MostRecentPrefixMatchWins(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	idOld, err := store.Reserve("deps-old", "v1")
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.Commit(idOld, 1); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	time.Sleep(2 * time.Millisecond) // ensure a distinct, later CreatedAt

	idNew, err := store.Reserve("deps-new", "v1")
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store.Commit(idNew, 1); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	entry, err := store.Find([]string{"deps-"}, "v1")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if entry == nil || entry.ID != idNew {
		t.Errorf("Find() = %+v, want the more-recently-created deps-new entry", entry)
	}
}

func TestStore_Commit_UnreservedIDIsError(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	if err := store.Commit("never-reserved", 1); err == nil {
		t.Fatal("Commit() error = nil, want error for an unreserved id")
	}
}

func TestStore_PersistsAcrossOpenStoreCalls(t *testing.T) {
	root := t.TempDir()

	store1, err := OpenStore(root)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	id, err := store1.Reserve("persisted-key", "v1")
	if err != nil {
		t.Fatalf("Reserve() error = %v", err)
	}
	if err := store1.Commit(id, 5); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}

	// A fresh OpenStore against the same root — simulating a second,
	// separate `mirror run` invocation — must still find the entry.
	store2, err := OpenStore(root)
	if err != nil {
		t.Fatalf("second OpenStore() error = %v", err)
	}
	entry, err := store2.Find([]string{"persisted-key"}, "v1")
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if entry == nil || entry.ID != id {
		t.Errorf("Find() after reopening = %+v, want the persisted entry", entry)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/vishnu.prasaath/workspace/mirror-gha && go test ./internal/cacheserver/... -v`
Expected: FAIL — package `internal/cacheserver` doesn't exist yet

- [ ] **Step 3: Implement**

```go
// internal/cacheserver/store.go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cacheserver/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/cacheserver/store.go internal/cacheserver/store_test.go
git commit -m "feat(cacheserver): persistent cache store with restore-keys matching"
```

---

### Task 2: `internal/cacheserver` — HTTP handlers

**Files:**
- Create: `internal/cacheserver/handlers.go`
- Test: `internal/cacheserver/handlers_test.go`

**Interfaces:**
- Consumes: `Store` and its methods (Task 1)
- Produces: `func NewHandler(store *Store) http.Handler`

- [ ] **Step 1: Write the failing tests**

```go
// internal/cacheserver/handlers_test.go
package cacheserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandlers_FullRoundTrip_ReserveUploadCommitFindDownload(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	ts := httptest.NewServer(NewHandler(store))
	defer ts.Close()

	// Reserve.
	reserveBody, _ := json.Marshal(map[string]string{"key": "my-cache-key", "version": "v1"})
	resp, err := http.Post(ts.URL+"/_apis/artifactcache/caches", "application/json", bytes.NewReader(reserveBody))
	if err != nil {
		t.Fatalf("POST /caches error = %v", err)
	}
	var reserveResp struct {
		CacheID string `json:"cacheId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&reserveResp); err != nil {
		t.Fatalf("decode reserve response: %v", err)
	}
	resp.Body.Close()
	if reserveResp.CacheID == "" {
		t.Fatal("cacheId is empty")
	}

	// Upload.
	content := []byte("cached blob content")
	req, err := http.NewRequest(http.MethodPatch, ts.URL+"/_apis/artifactcache/caches/"+reserveResp.CacheID, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("build PATCH request: %v", err)
	}
	req.Header.Set("Content-Range", "bytes 0-19/*")
	uploadResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PATCH /caches/:id error = %v", err)
	}
	uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200", uploadResp.StatusCode)
	}

	// Commit.
	commitBody, _ := json.Marshal(map[string]int64{"size": int64(len(content))})
	commitResp, err := http.Post(ts.URL+"/_apis/artifactcache/caches/"+reserveResp.CacheID, "application/json", bytes.NewReader(commitBody))
	if err != nil {
		t.Fatalf("POST /caches/:id (commit) error = %v", err)
	}
	commitResp.Body.Close()
	if commitResp.StatusCode != http.StatusOK {
		t.Fatalf("commit status = %d, want 200", commitResp.StatusCode)
	}

	// Find.
	findResp, err := http.Get(ts.URL + "/_apis/artifactcache/cache?keys=my-cache-key&version=v1")
	if err != nil {
		t.Fatalf("GET /cache error = %v", err)
	}
	if findResp.StatusCode != http.StatusOK {
		t.Fatalf("find status = %d, want 200", findResp.StatusCode)
	}
	var findBody struct {
		CacheKey        string `json:"cacheKey"`
		ArchiveLocation string `json:"archiveLocation"`
	}
	if err := json.NewDecoder(findResp.Body).Decode(&findBody); err != nil {
		t.Fatalf("decode find response: %v", err)
	}
	findResp.Body.Close()
	if findBody.CacheKey != "my-cache-key" {
		t.Errorf("cacheKey = %q, want %q", findBody.CacheKey, "my-cache-key")
	}
	if !strings.Contains(findBody.ArchiveLocation, reserveResp.CacheID) {
		t.Errorf("archiveLocation = %q, want it to contain %q", findBody.ArchiveLocation, reserveResp.CacheID)
	}

	// Download.
	downloadResp, err := http.Get(findBody.ArchiveLocation)
	if err != nil {
		t.Fatalf("download error = %v", err)
	}
	defer downloadResp.Body.Close()
	downloaded, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		t.Fatalf("read download body: %v", err)
	}
	if string(downloaded) != string(content) {
		t.Errorf("downloaded content = %q, want %q", downloaded, content)
	}
}

func TestHandlers_Find_MissReturns204(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	ts := httptest.NewServer(NewHandler(store))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_apis/artifactcache/cache?keys=nothing-here&version=v1")
	if err != nil {
		t.Fatalf("GET /cache error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func TestHandlers_Find_MissingKeysParamIs400(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	ts := httptest.NewServer(NewHandler(store))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_apis/artifactcache/cache?version=v1")
	if err != nil {
		t.Fatalf("GET /cache error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHandlers_Commit_UnreservedIDIs500(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	ts := httptest.NewServer(NewHandler(store))
	defer ts.Close()

	body, _ := json.Marshal(map[string]int64{"size": 1})
	resp, err := http.Post(ts.URL+"/_apis/artifactcache/caches/never-reserved", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST commit error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

func TestHandlers_Download_UnknownIDIs404(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	ts := httptest.NewServer(NewHandler(store))
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_apis/artifactcache/artifacts/unknown-id")
	if err != nil {
		t.Fatalf("GET download error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestHandlers_Clean_AlwaysOK(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	ts := httptest.NewServer(NewHandler(store))
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/_apis/artifactcache/clean", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /clean error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cacheserver/... -run TestHandlers -v`
Expected: FAIL — `NewHandler` undefined

- [ ] **Step 3: Implement**

```go
// internal/cacheserver/handlers.go
package cacheserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

type findResponse struct {
	CacheKey        string `json:"cacheKey"`
	ArchiveLocation string `json:"archiveLocation"`
}

type reserveRequest struct {
	Key     string `json:"key"`
	Version string `json:"version"`
}

// reserveResponse's CacheID is a string, not GitHub's/act's integer —
// a deliberate divergence. The real protocol's cacheId is opaque from
// the client's perspective: @actions/cache's client just round-trips
// whatever value the server gave it back into the PATCH/POST commit
// URL, so a string id works identically to an integer one as long as
// it's used consistently, and mirror-gha's Store already generates
// random hex ids (Task 1), not sequential integers.
type reserveResponse struct {
	CacheID string `json:"cacheId"`
}

type commitRequest struct {
	Size int64 `json:"size"`
}

// NewHandler builds the cache server's HTTP routes, matching act's real
// API surface (pkg/artifactcache/handler.go:98-103) and, through it,
// @actions/cache's actual client expectations.
func NewHandler(store *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_apis/artifactcache/cache", handleFind(store))
	mux.HandleFunc("POST /_apis/artifactcache/caches", handleReserve(store))
	mux.HandleFunc("PATCH /_apis/artifactcache/caches/{id}", handleUpload(store))
	mux.HandleFunc("POST /_apis/artifactcache/caches/{id}", handleCommit(store))
	mux.HandleFunc("GET /_apis/artifactcache/artifacts/{id}", handleDownload(store))
	mux.HandleFunc("POST /_apis/artifactcache/clean", handleClean)
	return mux
}

func handleFind(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		keysParam := r.URL.Query().Get("keys")
		if keysParam == "" {
			http.Error(w, "keys query parameter is required", http.StatusBadRequest)
			return
		}
		version := r.URL.Query().Get("version")
		keys := strings.Split(keysParam, ",")

		entry, err := store.Find(keys, version)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if entry == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(findResponse{
			CacheKey:        entry.Key,
			ArchiveLocation: fmt.Sprintf("http://%s/_apis/artifactcache/artifacts/%s", r.Host, entry.ID),
		})
	}
}

func handleReserve(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req reserveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		id, err := store.Reserve(req.Key, req.Version)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reserveResponse{CacheID: id})
	}
}

func handleUpload(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f, err := os.OpenFile(store.BlobPath(id), os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()

		offset := parseContentRangeStart(r.Header.Get("Content-Range"))
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := io.Copy(f, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func handleCommit(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req commitRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if err := store.Commit(id, req.Size); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func handleDownload(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if _, ok := store.Get(id); !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, store.BlobPath(id))
	}
}

func handleClean(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// parseContentRangeStart parses a "bytes <start>-<end>/*" Content-Range
// header into its start offset. A missing or unparseable header defaults
// to 0 (a single-chunk upload, no range at all).
func parseContentRangeStart(headerValue string) int64 {
	rest := strings.TrimPrefix(headerValue, "bytes ")
	parts := strings.SplitN(rest, "-", 2)
	if len(parts) == 0 {
		return 0
	}
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0
	}
	return n
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cacheserver/... -v`
Expected: PASS — full package suite

- [ ] **Step 5: Commit**

```bash
git add internal/cacheserver/handlers.go internal/cacheserver/handlers_test.go
git commit -m "feat(cacheserver): HTTP handlers matching act's cache API surface"
```

---

### Task 3: `internal/cacheserver` — server lifecycle

**Files:**
- Create: `internal/cacheserver/server.go`
- Test: `internal/cacheserver/server_test.go`

**Interfaces:**
- Consumes: `NewHandler` (Task 2), `mirror-gha/internal/actions.CacheRoot()`
- Produces: `func Start(store *Store) (*Server, error)`, `func (s *Server) Port() int`, `func (s *Server) Stop(ctx context.Context) error`, `func StoreRoot() (string, error)`

- [ ] **Step 1: Write the failing test**

```go
// internal/cacheserver/server_test.go
package cacheserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestServer_StartServeStop(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}

	srv, err := Start(store)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if srv.Port() == 0 {
		t.Fatal("Port() = 0, want a real assigned port")
	}

	url := fmt.Sprintf("http://localhost:%d/_apis/artifactcache/cache?keys=x&version=y", srv.Port())
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET against running server error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (genuine miss)", resp.StatusCode)
	}

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	if _, err := http.Get(url); err == nil {
		t.Error("GET after Stop() succeeded, want a connection error")
	}
}

func TestStoreRoot_ReturnsPathUnderCacheRoot(t *testing.T) {
	root, err := StoreRoot()
	if err != nil {
		t.Fatalf("StoreRoot() error = %v", err)
	}
	if root == "" {
		t.Fatal("StoreRoot() = empty string")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cacheserver/... -run 'TestServer_StartServeStop|TestStoreRoot' -v`
Expected: FAIL — `Start`/`StoreRoot` undefined

- [ ] **Step 3: Implement**

```go
// internal/cacheserver/server.go
package cacheserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"

	"mirror-gha/internal/actions"
)

// Server is a running cache HTTP server.
type Server struct {
	listener net.Listener
	httpSrv  *http.Server
}

// Start begins serving the cache API on an OS-assigned port, bound to
// all interfaces (0.0.0.0) so a job container can reach it via
// host.docker.internal — see the design spec's Cache Runtime section
// for why this, not act's outbound-IP-binding approach, is this
// project's v1 choice (this project's actual dev/test environment is
// macOS Docker Desktop, where host.docker.internal is a built-in DNS
// alias; plain Linux dockerd needs extra --add-host configuration not
// yet wired up here).
func Start(store *Store) (*Server, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("listen for cache server: %w", err)
	}
	httpSrv := &http.Server{Handler: NewHandler(store)}
	go httpSrv.Serve(listener) //nolint:errcheck // http.ErrServerClosed on Stop() is expected, not a real error
	return &Server{listener: listener, httpSrv: httpSrv}, nil
}

// Port is the OS-assigned TCP port the server is listening on.
func (s *Server) Port() int {
	return s.listener.Addr().(*net.TCPAddr).Port
}

// Stop gracefully shuts the server down.
func (s *Server) Stop(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

// StoreRoot is where the cache store persists across mirror run
// invocations — a subdirectory of mirror-gha's shared cache root,
// alongside the existing actions/ and node/ subdirectories.
func StoreRoot() (string, error) {
	root, err := actions.CacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "action-cache"), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cacheserver/... -v`
Expected: PASS — full package suite

- [ ] **Step 5: Commit**

```bash
git add internal/cacheserver/server.go internal/cacheserver/server_test.go
git commit -m "feat(cacheserver): server lifecycle (Start/Stop, StoreRoot)"
```

---

### Task 4: Engine + CLI wiring

**Files:**
- Modify: `internal/engine/executor.go`
- Modify: `internal/engine/workflow_run.go`
- Modify: `cmd/mirror/main.go`
- Test: `internal/engine/executor_test.go`, `internal/engine/workflow_run_test.go`

**Interfaces:**
- Consumes: `cacheserver.StoreRoot`, `cacheserver.OpenStore`, `cacheserver.Start` (Tasks 1-3)
- Produces: `JobRunOptions.ExtraEnv map[string]string`, `RunWorkflow`'s new 7th parameter `extraEnv map[string]string`

- [ ] **Step 1: Write the failing test**

```go
// internal/engine/executor_test.go — add this test
func TestRunJob_ExtraEnvReachesStepExec(t *testing.T) {
	wf := &Workflow{Name: "test"}
	job := &Job{
		RunsOn: "ubuntu-latest",
		Steps:  []Step{{ID: "one", Run: "echo $SOME_EXTRA_VAR"}},
	}
	backend := &fakeBackend{results: []runner.StepResult{{ExitCode: 0}}}

	_, err := RunJob(context.Background(), wf, job, backend, JobRunOptions{
		WorkspaceDir: t.TempDir(),
		ExtraEnv:     map[string]string{"SOME_EXTRA_VAR": "extra-value"},
	})
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}

	spec := backend.lastJob.execSpecs[0]
	if spec.Env["SOME_EXTRA_VAR"] != "extra-value" {
		t.Errorf(`Env["SOME_EXTRA_VAR"] = %q, want %q`, spec.Env["SOME_EXTRA_VAR"], "extra-value")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/engine/... -run TestRunJob_ExtraEnvReachesStepExec -v`
Expected: FAIL — `JobRunOptions.ExtraEnv` undefined

- [ ] **Step 3: Implement**

In `internal/engine/executor.go`, add a field to `JobRunOptions`:

```go
type JobRunOptions struct {
	Needs                    map[string]JobOutcome
	Matrix                   MatrixCombination
	Vars                     map[string]string
	WorkspaceDir             string // host directory bind-mounted as the job's workspace
	LocalRepositoryOverrides map[string]string
	ExtraEnv                 map[string]string
}
```

In `RunJob`, right after `actx.Vars = opts.Vars`, add:

```go
	for k, v := range opts.ExtraEnv {
		actx.Env[k] = v
	}
```

In `internal/engine/workflow_run.go`, add a parameter to `RunWorkflow`:

```go
func RunWorkflow(ctx context.Context, wf *Workflow, selectBackend BackendSelector, workspaceDir string, localRepositoryOverrides map[string]string, vars map[string]string, extraEnv map[string]string) (*WorkflowResult, error) {
```

And in its `RunJob` call:

```go
			jr, err := RunJob(runCtx, wf, &job, backend, JobRunOptions{
				Needs:                    outcomes,
				Matrix:                   combo,
				WorkspaceDir:             workspaceDir,
				LocalRepositoryOverrides: localRepositoryOverrides,
				Vars:                     vars,
				ExtraEnv:                 extraEnv,
			})
```

Update every existing `RunWorkflow(...)` call in `internal/engine/workflow_run_test.go` (all six calls) to add a trailing `nil` argument — e.g. `RunWorkflow(context.Background(), wf, succeedSelector, t.TempDir(), nil, nil)` becomes `RunWorkflow(context.Background(), wf, succeedSelector, t.TempDir(), nil, nil, nil)`.

In `cmd/mirror/main.go`'s `runCommand`, after resolving `workspaceDir` and before picking `selectBackend` (or after — order relative to `selectBackend` doesn't matter, but it must happen before the `engine.RunWorkflow` call), add:

```go
	extraEnv := map[string]string{}
	if !mode.dryRun {
		storeRoot, err := cacheserver.StoreRoot()
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve cache store root: %v\n", err)
			return 1
		}
		store, err := cacheserver.OpenStore(storeRoot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "open cache store: %v\n", err)
			return 1
		}
		cacheSrv, err := cacheserver.Start(store)
		if err != nil {
			fmt.Fprintf(os.Stderr, "start cache server: %v\n", err)
			return 1
		}
		defer cacheSrv.Stop(context.Background())

		extraEnv["ACTIONS_CACHE_URL"] = fmt.Sprintf("http://host.docker.internal:%d/", cacheSrv.Port())
		extraEnv["ACTIONS_RUNTIME_TOKEN"] = "mirror-gha-local-token"
	}
```

Add `"mirror-gha/internal/cacheserver"` to `main.go`'s imports. Update the `RunWorkflow` call site:

```go
	result, err := engine.RunWorkflow(context.Background(), wf, selectBackend, workspaceDir, mode.localRepositoryOverrides, mode.vars, extraEnv)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./... -v 2>&1 | tail -40`
Expected: PASS — full repo suite

- [ ] **Step 5: Commit**

```bash
git add internal/engine/executor.go internal/engine/workflow_run.go internal/engine/executor_test.go internal/engine/workflow_run_test.go cmd/mirror/main.go
git commit -m "feat(engine,cli): wire cache server env vars into every job"
```

---

### Task 5: Real end-to-end verification — `actions/cache@v4`

**Files:**
- Create: `examples/workflows/uses-cache.yml`
- Modify: `examples/README.md`, `docs/usage.md`, `CHANGELOG.md`, `docs/design/specs/2026-09-14-mirror-gha-design.md`

**Interfaces:**
- None new — this task is the real, no-fakes proof that Tasks 1-4 work together end-to-end against the actual, unmodified `actions/cache@v4` action.

This is the task where the assumptions behind Tasks 1-2's wire format (field names, `Content-Range` handling — see Global Constraints) either hold up or don't. **If the real action's requests don't match what the handlers expect, this is a real bug to find and fix, not a reason to weaken the example or skip verification** — the established pattern all session: add temporary request logging to `handlers.go` (log method, path, headers, and body for every request), re-run against the real action, inspect exactly what it sent, fix `handlers.go`/`store.go` to match reality, remove the temporary logging, re-verify, and describe what was actually wrong in the commit message.

- [ ] **Step 1: Create the example workflow**

```yaml
# examples/workflows/uses-cache.yml
# Demonstrates real actions/cache@v4 save/restore — cache server details
# are entirely invisible to the workflow itself, exactly like real
# GitHub Actions: the action just calls ACTIONS_CACHE_URL, which mirror
# run points at its own local cache server.
#
# Try it (from the repo root) — run it TWICE to see the second-run hit:
#   mirror run examples/workflows/uses-cache.yml
name: uses cache
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: restore cache
        id: cache
        uses: actions/cache@v4
        with:
          path: cache-data
          key: mirror-gha-demo-cache-v1
      - name: report cache status
        run: 'echo "cache-hit output: [${{ steps.cache.outputs.cache-hit }}]"'
      - name: populate cache data on a miss
        if: steps.cache.outputs.cache-hit != 'true'
        run: 'mkdir -p cache-data && echo "cached-content" > cache-data/file.txt'
```

- [ ] **Step 2: Clear any stale local cache store before the first verification run**

Run: `rm -rf ~/.cache/mirror-gha/action-cache`
Expected: no output — this guarantees the very next run observes a genuine miss, since the store persists across invocations by design (see Global Constraints) and a prior manual test run could otherwise produce a false "already passing" result.

- [ ] **Step 3: Run it for real (first time) and observe the actual miss behavior**

Run:
```bash
cd /Users/vishnu.prasaath/workspace/mirror-gha
go build -o bin/mirror ./cmd/mirror
./bin/mirror run examples/workflows/uses-cache.yml
```
Expected: all steps report `success`. Observe the actual printed value of `cache-hit output: [...]` — real `actions/cache@v4` reports `cache-hit` as an empty string or `'false'` on a genuine miss (not verified this session, see Global Constraints) — record what it actually prints. If any step fails or the action errors out making its HTTP calls, this is the "add temporary logging and fix the real mismatch" path described in this task's introduction — do not proceed to Step 4 until this run succeeds end-to-end with real, working cache save behavior (confirm via `ls ~/.cache/mirror-gha/action-cache/blobs/` showing a new file after this run).

- [ ] **Step 4: Run it again and confirm a genuine cache hit**

Run: `./bin/mirror run examples/workflows/uses-cache.yml`
Expected: all steps report `success`; `cache-hit output: [true]`; the "populate cache data on a miss" step reports `skipped` (its `if:` is now false) — proving the SECOND run's `restore cache` step genuinely found and restored the cache the FIRST run saved, via a real HTTP round trip from inside the container back to the host cache server, persisted across two separate `mirror run` process invocations.

- [ ] **Step 5: Re-run the full existing example suite to check for regressions**

Run:
```bash
for f in examples/workflows/*.yml; do
  echo "=== $f ==="
  ./bin/mirror run "$f" || echo "FAILED: $f (expected only for uses-local-repository.yml, which requires --local-repository)"
done
```
Expected: every workflow except `uses-local-repository.yml` prints `success` for all its steps.

- [ ] **Step 6: Update documentation**

In `examples/README.md`, add to the table:

```markdown
| [`uses-cache.yml`](workflows/uses-cache.yml) | Real `actions/cache@v4` save/restore against mirror-gha's own local cache server — run it twice to see the second run's genuine cache hit |
```

In `docs/usage.md`, find the existing "What's not supported yet" bullet that bundles artifacts and caching together (something like `Artifacts (...) and caching (actions/cache)`) and split it — caching moves to "What's supported today" with its own bullet, artifacts stays in "not supported yet" alone:

```markdown
- **Real `actions/cache` support** — a local HTTP server matching
  GitHub's actual cache API (not a filesystem shim, which
  `actions/cache` would never call into) serves save/restore requests
  from the real, unmodified action. Restore-keys matching mirrors
  GitHub's actual semantics (exact match, then prefix-match fallback,
  most-recently-created wins) rather than a simplified approximation.
  The cache store persists across separate `mirror run` invocations —
  a cache saved in one run is genuinely restorable in a later one, the
  entire point of the feature. Artifacts
  (`actions/upload-artifact`/`download-artifact`) are not supported yet
  (see below) — a separate feature area with its own, larger API
  surface (both the legacy v3 and current v4 protocols).
```

Remove `actions/cache` from the "not supported yet" bullet, leaving artifacts alone there.

In `CHANGELOG.md`, add under `### Added`:

```markdown
- **Real `actions/cache` support.** A new local HTTP server
  (`internal/cacheserver`) implements GitHub's actual cache API —
  `GET/POST/PATCH /_apis/artifactcache/...` — matching act's own
  `pkg/artifactcache` route surface and restore-keys matching
  (exact match, then anchored-prefix match, most-recently-created
  wins). Started once per `mirror run` invocation (not per-job,
  matching act's lifecycle), reachable from job containers via
  `host.docker.internal` (this project's actual dev/test environment
  is macOS Docker Desktop; plain Linux `dockerd` needs extra
  configuration not yet wired up). The cache store persists across
  separate `mirror run` invocations at `~/.cache/mirror-gha/action-cache/`
  — ephemeral-per-run would defeat the entire point of caching. No new
  action-type dispatch was needed: `actions/cache` is itself a bundled
  JS action, so it runs through the existing JS-actions machinery
  unmodified once the right env vars (`ACTIONS_CACHE_URL`,
  `ACTIONS_RUNTIME_TOKEN`) are present. Verified for real against the
  actual, unmodified `actions/cache@v4` action: a save on one `mirror
  run` invocation is genuinely restored (confirmed cache-hit output,
  the populate-on-miss step correctly skipped) on a second, separate
  invocation. Artifacts (`upload-artifact`/`download-artifact`, v3+v4)
  remain a separate, unscheduled follow-up.
```

In `docs/design/specs/2026-09-14-mirror-gha-design.md`, add `**Implemented.**` right after the "Cache Runtime" heading:

```markdown
## Cache Runtime

**Implemented.**
```

- [ ] **Step 7: Full verification pass**

Run:
```bash
go build ./... && go test ./... && make fmt-check && go vet ./...
```
Expected: build succeeds, all tests pass, `fmt-check`/`vet` produce no output/errors.

- [ ] **Step 8: Commit**

```bash
git add examples/workflows/uses-cache.yml examples/README.md docs/usage.md CHANGELOG.md docs/design/specs/2026-09-14-mirror-gha-design.md
git commit -m "feat: verify actions/cache end-to-end across two mirror run invocations"
```

## Self-Review Notes

- **Spec coverage:** Every piece of the "Cache Runtime" spec section maps to a task — persistent store + restore-keys matching (Task 1), HTTP API surface (Task 2), server lifecycle + `StoreRoot` (Task 3), env injection at the shared layer (Task 4), real end-to-end proof against the unmodified action + doc updates (Task 5). Artifacts are explicitly out of scope per the spec and not touched.
- **Placeholder scan:** No TBD/TODO; every step has complete, real code. Task 5's "add temporary logging if it doesn't work" instruction is a documented contingency procedure, not a placeholder — it doesn't skip verification, it's what verification requires if the untested wire-format assumptions from Tasks 1-2 turn out wrong.
- **Type consistency:** `CacheEntry`'s fields (Task 1) are used identically in Task 2's `handleFind`/`handleDownload` and Task 1's own `Find`/`Get`. `Store`'s methods (`Reserve`, `BlobPath`, `Commit`, `Find`, `Get`) match exactly between Task 1's definitions and Task 2's handler calls. `Server`'s `Port`/`Stop` (Task 3) match how Task 4's `cmd/mirror/main.go` wiring calls them. `JobRunOptions.ExtraEnv` (Task 4) is set consistently in both `workflow_run.go`'s `RunJob` call and read in `executor.go`'s `RunJob`.
- **Known simplifications carried over from the design spec, restated at point of use:** `host.docker.internal` networking (Task 3/4, Global Constraints) is a documented macOS-Docker-Desktop-first choice, not universal; the exact wire-format field names (Task 2) are the one part of this plan not independently re-verified against `@actions/cache`'s current source before Task 5 runs the real action against it.
