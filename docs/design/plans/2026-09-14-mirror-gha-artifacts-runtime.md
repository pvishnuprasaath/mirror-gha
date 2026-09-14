# Artifacts Runtime Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `actions/upload-artifact` and `actions/download-artifact` work for real against a new local HTTP server — both the legacy v3 REST protocol and the current v4 protocol — so a real, unmodified action of either major version can upload a file in one job and a later job can download it back.

**Architecture:** A new package, `internal/artifactserver`, mirrors `internal/cacheserver`'s exact shape (`Store`/routes/`Server`) but shares one server and one storage layer across both protocol generations, since they're small enough together not to warrant Cache Runtime's own further split. Storage is a fresh temp directory per `mirror run` invocation — never persisted across invocations, unlike the cache store, since real GitHub Actions artifacts belong to one run, not restored across later ones. `cmd/mirror/main.go` starts it alongside the cache server (both always-on, no flags) and extends the same `extraEnv` map with the artifact server's URL.

**Tech Stack:** Go 1.27 stdlib only (`net/http.ServeMux`'s method+path routing, `hash/fnv` for artifact IDs). No new dependencies — v4's wire format is plain JSON despite act using real protobuf internally to produce it, so mirror-gha hand-writes matching structs instead of adding a protobuf dependency.

**Spec:** `docs/design/specs/2026-09-14-mirror-gha-design.md`, "Artifacts Runtime" section.

## Global Constraints

- Both v3 and v4 protocols in one sub-project, sharing one server (`internal/artifactserver`) and one `Store` — not split further.
- Artifact storage does NOT persist across `mirror run` invocations (a fresh `os.MkdirTemp` per run, path printed at the end) — the opposite of the cache store, which deliberately does persist.
- Server starts always-on, alongside the cache server, in the same `if !mode.dryRun` block in `cmd/mirror/main.go` — no new CLI flag, matching this project's low-friction precedent from Cache Runtime (a deliberate divergence from act's own opt-in `--artifact-server-path`).
- `ACTIONS_RUNTIME_URL` and `ACTIONS_RESULTS_URL` both carry the identical artifact-server base URL — no separate `ACTIONS_ARTIFACT_URL` exists in the real protocol.
- v4's wire format is hand-written plain JSON with camelCase field names (matching protojson's real output, confirmed from act's `.pb.go` struct tags — NOT the snake_case shown in act's own misleading doc comment) — no `google.golang.org/protobuf` dependency.
- mirror-gha's `CreateArtifact`/`GetSignedArtifactURL` responses skip act's own HMAC signature on the returned URL — no trust boundary to protect locally, so replicating a fake signature is pure complexity with no payoff.
- TDD throughout: every task writes the failing test before the implementation.
- The exact wire-format field names and semantics (Tasks 1-2) are based on act's real source, not independently verified against a live `actions/upload-artifact`/`download-artifact` client this session — Task 4's real end-to-end run against both a v3 and v4 pinned action version is what actually proves or disproves them; fix real mismatches found there rather than assuming Tasks 1-2 are correct as written.

---

### Task 1: `internal/artifactserver` — storage layer

**Files:**
- Create: `internal/artifactserver/storage.go`
- Test: `internal/artifactserver/storage_test.go`

**Interfaces:**
- Produces: `type Store struct{...}` (unexported fields), `func OpenStore(root string) (*Store, error)`, `func NewTempStore() (*Store, string, error)`, `func (s *Store) Resolve(rel string) (string, error)`, `func (s *Store) WriteAt(rel string, offset int64, r io.Reader) error`, `func (s *Store) Exists(rel string) bool`

- [ ] **Step 1: Write the failing tests**

```go
// internal/artifactserver/storage_test.go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/vishnu.prasaath/workspace/mirror-gha && go test ./internal/artifactserver/... -v`
Expected: FAIL — package `internal/artifactserver` doesn't exist yet

- [ ] **Step 3: Implement**

```go
// internal/artifactserver/storage.go
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
	full := filepath.Join(s.root, filepath.Clean("/"+rel))
	root := filepath.Clean(s.root)
	if full != root && !strings.HasPrefix(full, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the artifact store root", rel)
	}
	return full, nil
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/artifactserver/... -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/artifactserver/storage.go internal/artifactserver/storage_test.go
git commit -m "feat(artifactserver): per-invocation blob storage with path-traversal guard"
```

---

### Task 2: `internal/artifactserver` — v3 routes

**Files:**
- Create: `internal/artifactserver/v3.go`
- Test: `internal/artifactserver/v3_test.go`

**Interfaces:**
- Consumes: `Store` (Task 1)
- Produces: `func registerV3Routes(mux *http.ServeMux, store *Store)`

- [ ] **Step 1: Write the failing test**

```go
// internal/artifactserver/v3_test.go
package artifactserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestV3_FullRoundTrip_ReserveUploadFinalizeListDownload(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	mux := http.NewServeMux()
	registerV3Routes(mux, store)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	const runID = "1"

	// Reserve.
	reserveResp, err := http.Post(ts.URL+"/_apis/pipelines/workflows/"+runID+"/artifacts", "application/json", nil)
	if err != nil {
		t.Fatalf("reserve error = %v", err)
	}
	var reserveBody struct {
		FileContainerResourceURL string `json:"fileContainerResourceUrl"`
	}
	if err := json.NewDecoder(reserveResp.Body).Decode(&reserveBody); err != nil {
		t.Fatalf("decode reserve response: %v", err)
	}
	reserveResp.Body.Close()
	if !strings.Contains(reserveBody.FileContainerResourceURL, "/upload/"+runID) {
		t.Fatalf("fileContainerResourceUrl = %q, want it to contain /upload/%s", reserveBody.FileContainerResourceURL, runID)
	}

	// Upload.
	content := []byte("artifact file content")
	uploadReq, err := http.NewRequest(http.MethodPut, reserveBody.FileContainerResourceURL+"?itemPath=my-artifact/file.txt", bytes.NewReader(content))
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	uploadResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		t.Fatalf("upload error = %v", err)
	}
	uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("upload status = %d, want 200", uploadResp.StatusCode)
	}

	// Finalize.
	finalizeReq, err := http.NewRequest(http.MethodPatch, ts.URL+"/_apis/pipelines/workflows/"+runID+"/artifacts", nil)
	if err != nil {
		t.Fatalf("build finalize request: %v", err)
	}
	finalizeResp, err := http.DefaultClient.Do(finalizeReq)
	if err != nil {
		t.Fatalf("finalize error = %v", err)
	}
	finalizeResp.Body.Close()
	if finalizeResp.StatusCode != http.StatusOK {
		t.Fatalf("finalize status = %d, want 200", finalizeResp.StatusCode)
	}

	// List.
	listResp, err := http.Get(ts.URL + "/_apis/pipelines/workflows/" + runID + "/artifacts")
	if err != nil {
		t.Fatalf("list error = %v", err)
	}
	var listBody struct {
		Count int `json:"count"`
		Value []struct {
			Name                     string `json:"name"`
			FileContainerResourceURL string `json:"fileContainerResourceUrl"`
		} `json:"value"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listBody); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	listResp.Body.Close()
	if listBody.Count != 1 || listBody.Value[0].Name != "my-artifact" {
		t.Fatalf("list = %+v, want one entry named my-artifact", listBody)
	}

	// Download list (container item listing).
	downloadListResp, err := http.Get(listBody.Value[0].FileContainerResourceURL)
	if err != nil {
		t.Fatalf("download list error = %v", err)
	}
	var downloadListBody struct {
		Value []struct {
			Path            string `json:"path"`
			ItemType        string `json:"itemType"`
			ContentLocation string `json:"contentLocation"`
		} `json:"value"`
	}
	if err := json.NewDecoder(downloadListResp.Body).Decode(&downloadListBody); err != nil {
		t.Fatalf("decode download list response: %v", err)
	}
	downloadListResp.Body.Close()
	if len(downloadListBody.Value) != 1 || downloadListBody.Value[0].ItemType != "file" {
		t.Fatalf("download list = %+v, want one file entry", downloadListBody)
	}

	// Download the actual file.
	fileResp, err := http.Get(downloadListBody.Value[0].ContentLocation)
	if err != nil {
		t.Fatalf("file download error = %v", err)
	}
	defer fileResp.Body.Close()
	downloaded, err := io.ReadAll(fileResp.Body)
	if err != nil {
		t.Fatalf("read downloaded content: %v", err)
	}
	if string(downloaded) != string(content) {
		t.Errorf("downloaded content = %q, want %q", downloaded, content)
	}
}

func TestV3_Upload_GzipSuffix(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	mux := http.NewServeMux()
	registerV3Routes(mux, store)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/upload/1?itemPath=gz-artifact/file.bin", bytes.NewReader([]byte("compressed")))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload error = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	if !store.Exists("1/gz-artifact/file.bin.gz__") {
		t.Error("expected the .gz__-suffixed file to exist in the store")
	}
}

func TestV3_Upload_MissingItemPathIs400(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	mux := http.NewServeMux()
	registerV3Routes(mux, store)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPut, ts.URL+"/upload/1", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestV3_List_EmptyRunIsNotAnError(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	mux := http.NewServeMux()
	registerV3Routes(mux, store)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/_apis/pipelines/workflows/never-uploaded/artifacts")
	if err != nil {
		t.Fatalf("list error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var body struct {
		Count int `json:"count"`
	}
	json.NewDecoder(resp.Body).Decode(&body)
	if body.Count != 0 {
		t.Errorf("count = %d, want 0", body.Count)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/artifactserver/... -run TestV3 -v`
Expected: FAIL — `registerV3Routes` undefined

- [ ] **Step 3: Implement**

```go
// internal/artifactserver/v3.go
package artifactserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type fileContainerResourceURL struct {
	FileContainerResourceURL string `json:"fileContainerResourceUrl"`
}

type namedFileContainerResourceURL struct {
	Name                     string `json:"name"`
	FileContainerResourceURL string `json:"fileContainerResourceUrl"`
}

type namedFileContainerResourceURLResponse struct {
	Count int                              `json:"count"`
	Value []namedFileContainerResourceURL `json:"value"`
}

type containerItem struct {
	Path            string `json:"path"`
	ItemType        string `json:"itemType"`
	ContentLocation string `json:"contentLocation"`
}

type containerItemResponse struct {
	Value []containerItem `json:"value"`
}

type responseMessage struct {
	Message string `json:"message"`
}

// registerV3Routes wires up act's real v3 artifact API surface
// (pkg/artifacts/server.go) — the legacy REST-ish protocol used by
// actions/upload-artifact@v3 and earlier.
func registerV3Routes(mux *http.ServeMux, store *Store) {
	mux.HandleFunc("POST /_apis/pipelines/workflows/{runId}/artifacts", v3Reserve)
	mux.HandleFunc("PUT /upload/{runId}", v3Upload(store))
	mux.HandleFunc("PATCH /_apis/pipelines/workflows/{runId}/artifacts", v3Finalize)
	mux.HandleFunc("GET /_apis/pipelines/workflows/{runId}/artifacts", v3List(store))
	mux.HandleFunc("GET /download/{container}", v3DownloadList(store))
	mux.HandleFunc("GET /artifact/{path...}", v3DownloadArtifact(store))
}

func v3Reserve(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fileContainerResourceURL{
		FileContainerResourceURL: fmt.Sprintf("http://%s/upload/%s", r.Host, runID),
	})
}

func v3Upload(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runId")
		itemPath := r.URL.Query().Get("itemPath")
		if itemPath == "" {
			http.Error(w, "itemPath query parameter is required", http.StatusBadRequest)
			return
		}
		rel := filepath.Join(runID, itemPath)
		if r.Header.Get("Content-Encoding") == "gzip" {
			rel += ".gz__"
		}
		offset := parseContentRangeStart(r.Header.Get("Content-Range"))
		if err := store.WriteAt(rel, offset, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responseMessage{Message: "success"})
	}
}

func v3Finalize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(responseMessage{Message: "success"})
}

func v3List(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		runID := r.PathValue("runId")
		runDir, err := store.Resolve(runID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		entries, err := os.ReadDir(runDir)
		if err != nil && !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp := namedFileContainerResourceURLResponse{}
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".gz__")
			resp.Value = append(resp.Value, namedFileContainerResourceURL{
				Name:                     name,
				FileContainerResourceURL: fmt.Sprintf("http://%s/download/%s", r.Host, runID),
			})
		}
		resp.Count = len(resp.Value)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func v3DownloadList(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		container := r.PathValue("container")
		itemPath := r.URL.Query().Get("itemPath")
		base := container
		if itemPath != "" {
			base = filepath.Join(container, itemPath)
		}
		dir, err := store.Resolve(base)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp := containerItemResponse{}
		filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			displayName := strings.TrimSuffix(rel, ".gz__")
			resp.Value = append(resp.Value, containerItem{
				Path:            filepath.ToSlash(filepath.Join(filepath.Base(container), displayName)),
				ItemType:        "file",
				ContentLocation: fmt.Sprintf("http://%s/artifact/%s", r.Host, filepath.ToSlash(filepath.Join(base, rel))),
			})
			return nil
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func v3DownloadArtifact(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rel := r.PathValue("path")
		path, err := store.Resolve(rel)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := os.Stat(path); err != nil {
			gzPath := path + ".gz__"
			if _, gzErr := os.Stat(gzPath); gzErr == nil {
				w.Header().Set("Content-Encoding", "gzip")
				http.ServeFile(w, r, gzPath)
				return
			}
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	}
}

// parseContentRangeStart parses a "bytes <start>-<end>/*" Content-Range
// header into its start offset. A missing or unparseable header defaults
// to 0 (a single-chunk upload, no range at all) — duplicated from
// internal/cacheserver's identical private helper rather than exported/
// shared, same precedent as requireDocker/requireNetwork being
// duplicated per package throughout this project.
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

Run: `go test ./internal/artifactserver/... -v`
Expected: PASS — full package suite

- [ ] **Step 5: Commit**

```bash
git add internal/artifactserver/v3.go internal/artifactserver/v3_test.go
git commit -m "feat(artifactserver): v3 artifact API routes matching act's real wire format"
```

---

### Task 3: `internal/artifactserver` — v4 routes

**Files:**
- Create: `internal/artifactserver/v4.go`
- Test: `internal/artifactserver/v4_test.go`

**Interfaces:**
- Consumes: `Store` (Task 1)
- Produces: `func registerV4Routes(mux *http.ServeMux, store *Store)`, `const v4RouteBase`

- [ ] **Step 1: Write the failing test**

```go
// internal/artifactserver/v4_test.go
package artifactserver

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestV4_FullRoundTrip_CreateUploadFinalizeListGetURLDownloadDelete(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	mux := http.NewServeMux()
	registerV4Routes(mux, store)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// Create.
	createBody, _ := json.Marshal(map[string]string{"name": "my-v4-artifact"})
	createResp, err := http.Post(ts.URL+v4RouteBase+"/CreateArtifact", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("CreateArtifact error = %v", err)
	}
	var createRespBody struct {
		OK              bool   `json:"ok"`
		SignedUploadURL string `json:"signedUploadUrl"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&createRespBody); err != nil {
		t.Fatalf("decode CreateArtifact response: %v", err)
	}
	createResp.Body.Close()
	if !createRespBody.OK || !strings.Contains(createRespBody.SignedUploadURL, "UploadArtifact") {
		t.Fatalf("CreateArtifact response = %+v, want ok=true and an UploadArtifact URL", createRespBody)
	}

	// Upload.
	content := []byte("fake zip bytes")
	uploadReq, err := http.NewRequest(http.MethodPut, createRespBody.SignedUploadURL, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	uploadResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		t.Fatalf("UploadArtifact error = %v", err)
	}
	uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d, want 201", uploadResp.StatusCode)
	}

	// Finalize.
	finalizeBody, _ := json.Marshal(map[string]interface{}{"name": "my-v4-artifact", "size": len(content)})
	finalizeResp, err := http.Post(ts.URL+v4RouteBase+"/FinalizeArtifact", "application/json", bytes.NewReader(finalizeBody))
	if err != nil {
		t.Fatalf("FinalizeArtifact error = %v", err)
	}
	var finalizeRespBody struct {
		OK         bool  `json:"ok"`
		ArtifactID int64 `json:"artifactId"`
	}
	if err := json.NewDecoder(finalizeResp.Body).Decode(&finalizeRespBody); err != nil {
		t.Fatalf("decode FinalizeArtifact response: %v", err)
	}
	finalizeResp.Body.Close()
	if !finalizeRespBody.OK || finalizeRespBody.ArtifactID == 0 {
		t.Fatalf("FinalizeArtifact response = %+v, want ok=true and a non-zero artifactId", finalizeRespBody)
	}

	// List.
	listResp, err := http.Post(ts.URL+v4RouteBase+"/ListArtifacts", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("ListArtifacts error = %v", err)
	}
	var listRespBody struct {
		Artifacts []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listRespBody); err != nil {
		t.Fatalf("decode ListArtifacts response: %v", err)
	}
	listResp.Body.Close()
	if len(listRespBody.Artifacts) != 1 || listRespBody.Artifacts[0].Name != "my-v4-artifact" {
		t.Fatalf("ListArtifacts = %+v, want one entry named my-v4-artifact", listRespBody)
	}

	// GetSignedArtifactURL.
	getURLBody, _ := json.Marshal(map[string]string{"name": "my-v4-artifact"})
	getURLResp, err := http.Post(ts.URL+v4RouteBase+"/GetSignedArtifactURL", "application/json", bytes.NewReader(getURLBody))
	if err != nil {
		t.Fatalf("GetSignedArtifactURL error = %v", err)
	}
	var getURLRespBody struct {
		SignedURL string `json:"signedUrl"`
	}
	if err := json.NewDecoder(getURLResp.Body).Decode(&getURLRespBody); err != nil {
		t.Fatalf("decode GetSignedArtifactURL response: %v", err)
	}
	getURLResp.Body.Close()
	if !strings.Contains(getURLRespBody.SignedURL, "DownloadArtifact") {
		t.Fatalf("signedUrl = %q, want it to contain DownloadArtifact", getURLRespBody.SignedURL)
	}

	// Download.
	downloadResp, err := http.Get(getURLRespBody.SignedURL)
	if err != nil {
		t.Fatalf("download error = %v", err)
	}
	defer downloadResp.Body.Close()
	downloaded, err := io.ReadAll(downloadResp.Body)
	if err != nil {
		t.Fatalf("read downloaded content: %v", err)
	}
	if string(downloaded) != string(content) {
		t.Errorf("downloaded content = %q, want %q", downloaded, content)
	}

	// Delete.
	deleteBody, _ := json.Marshal(map[string]string{"name": "my-v4-artifact"})
	deleteResp, err := http.Post(ts.URL+v4RouteBase+"/DeleteArtifact", "application/json", bytes.NewReader(deleteBody))
	if err != nil {
		t.Fatalf("DeleteArtifact error = %v", err)
	}
	defer deleteResp.Body.Close()
	if deleteResp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", deleteResp.StatusCode)
	}

	afterDeleteResp, err := http.Get(getURLRespBody.SignedURL)
	if err != nil {
		t.Fatalf("post-delete download error = %v", err)
	}
	defer afterDeleteResp.Body.Close()
	if afterDeleteResp.StatusCode != http.StatusNotFound {
		t.Errorf("post-delete download status = %d, want 404", afterDeleteResp.StatusCode)
	}
}

func TestV4_UploadArtifact_MissingNameIs400(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	mux := http.NewServeMux()
	registerV4Routes(mux, store)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	req, err := http.NewRequest(http.MethodPut, ts.URL+v4RouteBase+"/UploadArtifact", bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload error = %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestV4_ListArtifacts_NameFilter(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	mux := http.NewServeMux()
	registerV4Routes(mux, store)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	for _, name := range []string{"artifact-a", "artifact-b"} {
		createBody, _ := json.Marshal(map[string]string{"name": name})
		createResp, err := http.Post(ts.URL+v4RouteBase+"/CreateArtifact", "application/json", bytes.NewReader(createBody))
		if err != nil {
			t.Fatalf("CreateArtifact(%s) error = %v", name, err)
		}
		var createRespBody struct {
			SignedUploadURL string `json:"signedUploadUrl"`
		}
		json.NewDecoder(createResp.Body).Decode(&createRespBody)
		createResp.Body.Close()

		uploadReq, _ := http.NewRequest(http.MethodPut, createRespBody.SignedUploadURL, strings.NewReader("x"))
		uploadResp, err := http.DefaultClient.Do(uploadReq)
		if err != nil {
			t.Fatalf("upload(%s) error = %v", name, err)
		}
		uploadResp.Body.Close()
	}

	filterBody, _ := json.Marshal(map[string]string{"nameFilter": "artifact-a"})
	listResp, err := http.Post(ts.URL+v4RouteBase+"/ListArtifacts", "application/json", bytes.NewReader(filterBody))
	if err != nil {
		t.Fatalf("ListArtifacts error = %v", err)
	}
	defer listResp.Body.Close()
	var listRespBody struct {
		Artifacts []struct {
			Name string `json:"name"`
		} `json:"artifacts"`
	}
	json.NewDecoder(listResp.Body).Decode(&listRespBody)
	if len(listRespBody.Artifacts) != 1 || listRespBody.Artifacts[0].Name != "artifact-a" {
		t.Errorf("filtered list = %+v, want exactly one entry named artifact-a", listRespBody.Artifacts)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/artifactserver/... -run TestV4 -v`
Expected: FAIL — `registerV4Routes`/`v4RouteBase` undefined

- [ ] **Step 3: Implement**

```go
// internal/artifactserver/v4.go
package artifactserver

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// v4RouteBase matches act's twirp-shaped URL paths (artifacts_v4.go) —
// the real wire bytes are plain JSON despite the twirp-looking URLs and
// despite act using real protobuf-generated types internally to produce
// them; there's no actual protobuf-over-the-wire framing here, in act's
// server or in mirror-gha's.
const v4RouteBase = "/twirp/github.actions.results.api.v1.ArtifactService"

// Field names below are camelCase, matching protojson's real json_name
// output confirmed from act's own .pb.go struct tags — NOT the
// snake_case shown in act's own artifacts_v4.go doc comment, which is
// misleading. mirror-gha hand-writes these as plain encoding/json
// structs rather than adding a protobuf dependency, since the wire
// format is plain JSON either way.
type createArtifactRequest struct {
	WorkflowRunBackendId    string `json:"workflowRunBackendId"`
	WorkflowJobRunBackendId string `json:"workflowJobRunBackendId"`
	Name                    string `json:"name"`
	Version                 int32  `json:"version"`
}

type createArtifactResponse struct {
	OK              bool   `json:"ok"`
	SignedUploadURL string `json:"signedUploadUrl"`
}

type finalizeArtifactRequest struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type finalizeArtifactResponse struct {
	OK         bool  `json:"ok"`
	ArtifactID int64 `json:"artifactId"`
}

type listArtifactsRequest struct {
	NameFilter string `json:"nameFilter"`
}

type artifactInfo struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	CreatedAt string `json:"createdAt"`
}

type listArtifactsResponse struct {
	Artifacts []artifactInfo `json:"artifacts"`
}

type getSignedArtifactURLRequest struct {
	Name string `json:"name"`
}

type getSignedArtifactURLResponse struct {
	SignedURL string `json:"signedUrl"`
}

type deleteArtifactRequest struct {
	Name string `json:"name"`
}

type deleteArtifactResponse struct {
	OK         bool  `json:"ok"`
	ArtifactID int64 `json:"artifactId"`
}

// artifactNameToID matches act's own artifactNameToID — an FNV-32a hash
// of the artifact name, not a real incrementing database id. The real
// v4 client treats this as an opaque identifier, so any deterministic
// mapping from name to id works.
func artifactNameToID(name string) int64 {
	h := fnv.New32a()
	h.Write([]byte(name))
	return int64(h.Sum32())
}

// v4BlobRel is where a v4 artifact's single zip blob lives, relative to
// the store root. The real v4 client always zips client-side before
// uploading; act's server never unzips it, just stores the blob as-is —
// mirror-gha does the same.
func v4BlobRel(name string) string {
	return filepath.Join(name, name+".zip")
}

func registerV4Routes(mux *http.ServeMux, store *Store) {
	mux.HandleFunc("POST "+v4RouteBase+"/CreateArtifact", v4CreateArtifact)
	mux.HandleFunc("PUT "+v4RouteBase+"/UploadArtifact", v4UploadArtifact(store))
	mux.HandleFunc("POST "+v4RouteBase+"/FinalizeArtifact", v4FinalizeArtifact)
	mux.HandleFunc("POST "+v4RouteBase+"/ListArtifacts", v4ListArtifacts(store))
	mux.HandleFunc("POST "+v4RouteBase+"/GetSignedArtifactURL", v4GetSignedArtifactURL)
	mux.HandleFunc("GET "+v4RouteBase+"/DownloadArtifact", v4DownloadArtifact(store))
	mux.HandleFunc("POST "+v4RouteBase+"/DeleteArtifact", v4DeleteArtifact(store))
}

// v4CreateArtifact returns a "signed" upload URL pointing back at this
// same server's UploadArtifact route — matching act's own local
// simulation (no real separate blob-storage origin exists). Unlike act,
// mirror-gha skips the HMAC signature act still adds to this URL: there
// is no trust boundary to protect on a local, single-user server, so
// replicating a fake signature would be complexity with no payoff.
func v4CreateArtifact(w http.ResponseWriter, r *http.Request) {
	var req createArtifactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	uploadURL := fmt.Sprintf("http://%s%s/UploadArtifact?artifactName=%s", r.Host, v4RouteBase, url.QueryEscape(req.Name))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(createArtifactResponse{OK: true, SignedUploadURL: uploadURL})
}

func v4UploadArtifact(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("artifactName")
		if name == "" {
			http.Error(w, "artifactName query parameter is required", http.StatusBadRequest)
			return
		}
		if err := store.WriteAt(v4BlobRel(name), 0, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}
}

func v4FinalizeArtifact(w http.ResponseWriter, r *http.Request) {
	var req finalizeArtifactRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(finalizeArtifactResponse{OK: true, ArtifactID: artifactNameToID(req.Name)})
}

func v4ListArtifacts(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req listArtifactsRequest
		_ = json.NewDecoder(r.Body).Decode(&req) // best-effort; an empty/missing body means "list everything"

		entries, err := os.ReadDir(store.root)
		if err != nil && !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp := listArtifactsResponse{}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			name := e.Name()
			if req.NameFilter != "" && req.NameFilter != name {
				continue
			}
			blobPath, err := store.Resolve(v4BlobRel(name))
			if err != nil {
				continue
			}
			info, err := os.Stat(blobPath)
			if err != nil {
				continue
			}
			resp.Artifacts = append(resp.Artifacts, artifactInfo{
				Name:      name,
				Size:      info.Size(),
				CreatedAt: info.ModTime().UTC().Format(time.RFC3339),
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}

func v4GetSignedArtifactURL(w http.ResponseWriter, r *http.Request) {
	var req getSignedArtifactURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	downloadURL := fmt.Sprintf("http://%s%s/DownloadArtifact?artifactName=%s", r.Host, v4RouteBase, url.QueryEscape(req.Name))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(getSignedArtifactURLResponse{SignedURL: downloadURL})
}

func v4DownloadArtifact(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("artifactName")
		path, err := store.Resolve(v4BlobRel(name))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if _, err := os.Stat(path); err != nil {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, path)
	}
}

func v4DeleteArtifact(store *Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req deleteArtifactRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if path, err := store.Resolve(v4BlobRel(req.Name)); err == nil {
			os.RemoveAll(filepath.Dir(path))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(deleteArtifactResponse{OK: true, ArtifactID: artifactNameToID(req.Name)})
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/artifactserver/... -v`
Expected: PASS — full package suite

- [ ] **Step 5: Commit**

```bash
git add internal/artifactserver/v4.go internal/artifactserver/v4_test.go
git commit -m "feat(artifactserver): v4 artifact API routes matching act's real wire format"
```

---

### Task 4: Server lifecycle + engine/CLI wiring

**Files:**
- Create: `internal/artifactserver/server.go`
- Test: `internal/artifactserver/server_test.go`
- Modify: `internal/engine/context.go`
- Modify: `cmd/mirror/main.go`
- Test: `internal/engine/context_test.go`

**Interfaces:**
- Consumes: `registerV3Routes`, `registerV4Routes` (Tasks 2-3)
- Produces: `func Start(store *Store) (*Server, error)`, `func (s *Server) Port() int`, `func (s *Server) Stop(ctx context.Context) error`

- [ ] **Step 1: Write the failing tests**

```go
// internal/artifactserver/server_test.go
package artifactserver

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

func TestServer_StartServesBothProtocolsAndStop(t *testing.T) {
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

	v3URL := fmt.Sprintf("http://localhost:%d/_apis/pipelines/workflows/never-uploaded/artifacts", srv.Port())
	v3Resp, err := http.Get(v3URL)
	if err != nil {
		t.Fatalf("v3 GET error = %v", err)
	}
	v3Resp.Body.Close()
	if v3Resp.StatusCode != http.StatusOK {
		t.Errorf("v3 status = %d, want 200", v3Resp.StatusCode)
	}

	v4URL := fmt.Sprintf("http://localhost:%d%s/ListArtifacts", srv.Port(), v4RouteBase)
	v4Resp, err := http.Post(v4URL, "application/json", nil)
	if err != nil {
		t.Fatalf("v4 POST error = %v", err)
	}
	v4Resp.Body.Close()
	if v4Resp.StatusCode != http.StatusOK {
		t.Errorf("v4 status = %d, want 200", v4Resp.StatusCode)
	}

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if _, err := http.Get(v3URL); err == nil {
		t.Error("GET after Stop() succeeded, want a connection error")
	}
}
```

```go
// internal/engine/context_test.go — add this test
func TestNewContext_ExposesRunIdentity(t *testing.T) {
	ctx := NewContext(&Workflow{}, &Job{})

	if ctx.GitHub["run_id"] == "" {
		t.Error(`GitHub["run_id"] is empty, want a real placeholder value`)
	}
	if ctx.GitHub["run_number"] == "" {
		t.Error(`GitHub["run_number"] is empty, want a real placeholder value`)
	}
	if ctx.GitHub["run_attempt"] == "" {
		t.Error(`GitHub["run_attempt"] is empty, want a real placeholder value`)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/artifactserver/... -run TestServer -v` and `go test ./internal/engine/... -run TestNewContext_ExposesRunIdentity -v`
Expected: FAIL — `Start` undefined; `GitHub["run_id"]` empty

- [ ] **Step 3: Implement**

```go
// internal/artifactserver/server.go
package artifactserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
)

// Server is a running artifact HTTP server, serving both v3 and v4
// routes on one listener.
type Server struct {
	listener net.Listener
	httpSrv  *http.Server
}

// Start begins serving the artifact API (v3 and v4 together) on an
// OS-assigned port, bound to all interfaces (0.0.0.0) so a job container
// can reach it via host.docker.internal — same reasoning as
// cacheserver.Start (see the design spec's Cache Runtime section): this
// project's actual dev/test environment is macOS Docker Desktop, where
// host.docker.internal is a built-in DNS alias; plain Linux dockerd
// needs extra configuration not yet wired up here.
func Start(store *Store) (*Server, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("listen for artifact server: %w", err)
	}
	mux := http.NewServeMux()
	registerV3Routes(mux, store)
	registerV4Routes(mux, store)
	httpSrv := &http.Server{Handler: mux}
	go func() {
		_ = httpSrv.Serve(listener) // http.ErrServerClosed on Stop() is expected, not a real error
	}()
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
```

In `internal/engine/context.go`, add three entries to `NewContext`'s `GitHub` map (alongside the existing `event_name`/`ref`/`sha`/`repository`/`workflow`/`actor`):

```go
			"run_id":      "1",
			"run_number":  "1",
			"run_attempt": "1",
```

These reach every step as `GITHUB_RUN_ID`/`GITHUB_RUN_NUMBER`/`GITHUB_RUN_ATTEMPT` automatically via `executor.go`'s existing `exportGitHubContextEnv` helper — no other engine-layer change needed. This is a proactive fix: `actions/cache@v4`'s real bug during Cache Runtime (gating on `GITHUB_REF` existing) showed that real actions often read `GITHUB_*` env vars the runner is supposed to export, not just expression-context values — `actions/upload-artifact`/`download-artifact`'s real client very likely needs `GITHUB_RUN_ID` to construct its own requests, so this closes the same bug class proactively rather than rediscovering it via a failed real end-to-end run in Task 5.

In `cmd/mirror/main.go`, add `"mirror-gha/internal/artifactserver"` to the imports, and extend the existing `if !mode.dryRun { ... }` block (which already starts the cache server) with:

```go
		artifactStore, artifactRoot, err := artifactserver.NewTempStore()
		if err != nil {
			fmt.Fprintf(os.Stderr, "create artifact store: %v\n", err)
			return 1
		}
		artifactSrv, err := artifactserver.Start(artifactStore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "start artifact server: %v\n", err)
			return 1
		}
		defer artifactSrv.Stop(context.Background())
		defer fmt.Printf("artifacts stored at: %s\n", artifactRoot)

		extraEnv["ACTIONS_RUNTIME_URL"] = fmt.Sprintf("http://host.docker.internal:%d/", artifactSrv.Port())
		extraEnv["ACTIONS_RESULTS_URL"] = extraEnv["ACTIONS_RUNTIME_URL"]
```

placed right after the existing `extraEnv["ACTIONS_RUNTIME_TOKEN"] = "mirror-gha-local-token"` line, still inside the same `if !mode.dryRun` block. No signature changes needed anywhere else — `extraEnv` already exists and already threads through `RunWorkflow`'s existing parameter.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go build ./... && go test ./... -v 2>&1 | tail -40`
Expected: PASS — full repo suite

- [ ] **Step 5: Commit**

```bash
git add internal/artifactserver/server.go internal/artifactserver/server_test.go internal/engine/context.go internal/engine/context_test.go cmd/mirror/main.go
git commit -m "feat(artifactserver,engine,cli): server lifecycle, run identity, env wiring"
```

---

### Task 5: Real end-to-end verification — v4 and v3

**Files:**
- Create: `examples/workflows/uses-artifact-v4.yml`
- Create: `examples/workflows/uses-artifact-v3.yml`
- Modify: `examples/README.md`, `docs/usage.md`, `CHANGELOG.md`, `docs/design/specs/2026-09-14-mirror-gha-design.md`

**Interfaces:**
- None new — this task is the real, no-fakes proof that Tasks 1-4 work together end-to-end against both real, unmodified protocol generations.

This is the task where Tasks 2-3's wire-format assumptions (field names, `Content-Range`/gzip handling, the v4 signed-URL shape) either hold up against a real client or don't. **If either real run doesn't work as expected, this is a real bug to find and fix, not a reason to weaken the example or skip verification** — the established pattern all session: add temporary request logging to the relevant handler (method, path, headers, body), re-run against the real action, inspect exactly what it sent, fix the mismatch, remove the logging, re-verify, and describe what was actually wrong in the commit message.

- [ ] **Step 1: Create the v4 example workflow**

```yaml
# examples/workflows/uses-artifact-v4.yml
# Demonstrates real actions/upload-artifact@v4 / download-artifact@v4 —
# one job uploads a file, a later job downloads it back and verifies the
# content round-trips. Artifact server details are entirely invisible to
# the workflow, exactly like real GitHub Actions.
#
# Try it (from the repo root):
#   mirror run examples/workflows/uses-artifact-v4.yml
name: uses artifact v4
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: create artifact content
        run: 'mkdir -p out && echo "hello from build (v4)" > out/greeting.txt'
      - name: upload artifact
        uses: actions/upload-artifact@v4
        with:
          name: greeting-v4
          path: out/greeting.txt
  deploy:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - name: download artifact
        uses: actions/download-artifact@v4
        with:
          name: greeting-v4
          path: downloaded
      - name: verify content round-tripped
        run: 'cat downloaded/greeting.txt'
```

- [ ] **Step 2: Run it for real and verify the content round-trips**

Run:
```bash
cd /Users/vishnu.prasaath/workspace/mirror-gha
go build -o bin/mirror ./cmd/mirror
./bin/mirror run examples/workflows/uses-artifact-v4.yml
```
Expected: all steps in both jobs report `success`; output includes `hello from build (v4)` from the `deploy` job's "verify content round-tripped" step — proving a genuine upload in `build` and download in `deploy` through the real v4 protocol. If the upload or download step fails, inspect what actually went wrong per this task's introduction (temporary request logging, real fix, re-verify) before proceeding.

- [ ] **Step 3: Create the v3 example workflow**

```yaml
# examples/workflows/uses-artifact-v3.yml
# Same shape as uses-artifact-v4.yml, but pinned to the legacy v3 major —
# proves the older REST protocol works against a real, unmodified action
# too, not just v4.
#
# Try it (from the repo root):
#   mirror run examples/workflows/uses-artifact-v3.yml
name: uses artifact v3
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - name: create artifact content
        run: 'mkdir -p out && echo "hello from build (v3)" > out/greeting.txt'
      - name: upload artifact
        uses: actions/upload-artifact@v3
        with:
          name: greeting-v3
          path: out/greeting.txt
  deploy:
    needs: build
    runs-on: ubuntu-latest
    steps:
      - name: download artifact
        uses: actions/download-artifact@v3
        with:
          name: greeting-v3
          path: downloaded
      - name: verify content round-tripped
        run: 'cat downloaded/greeting.txt'
```

- [ ] **Step 4: Run it for real and verify the content round-trips**

Run: `./bin/mirror run examples/workflows/uses-artifact-v3.yml`
Expected: all steps in both jobs report `success`; output includes `hello from build (v3)` — proving the legacy v3 REST protocol also genuinely works against a real, unmodified action. If this fails while v4 succeeded (or vice versa), that confirms the two protocols need independently-verified fixes — debug and fix each on its own terms per this task's introduction.

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
| [`uses-artifact-v4.yml`](workflows/uses-artifact-v4.yml) | Real `actions/upload-artifact@v4`/`download-artifact@v4` — one job uploads a file, a later job downloads and verifies it round-tripped |
| [`uses-artifact-v3.yml`](workflows/uses-artifact-v3.yml) | Same as above, pinned to the legacy v3 major — proves the older REST protocol works too |
```

Update its "What's not shown here (yet)" paragraph — artifacts are no longer unsupported; only matrix `include`/`exclude` and Windows/macOS runners remain (if nothing else is currently listed there, update accordingly based on what's actually still in `docs/usage.md`'s own "not supported yet" list at the time this task runs).

In `docs/usage.md`, add a bullet under "What's supported today" (right after the existing cache-support bullet):

```markdown
- **Real `actions/upload-artifact`/`download-artifact` support** — both
  the legacy v3 REST protocol and the current v4 protocol, served by one
  local HTTP server (`internal/artifactserver`). Unlike the cache store,
  artifact storage is a fresh directory per `mirror run` invocation
  (printed at the end of the run so it's inspectable) — never restored
  by a later invocation, matching real GitHub Actions' own per-run
  artifact model. No new action-type dispatch was needed: both actions
  are themselves bundled JS actions, running through the existing
  JS-actions machinery unmodified once the right env vars
  (`ACTIONS_RUNTIME_URL`, `ACTIONS_RESULTS_URL`) are present.
```

Remove the "Artifacts" bullet from "What's not supported yet" entirely — check the current wording there first (it should currently read something like `- Artifacts (\`actions/upload-artifact\` / \`download-artifact\`, v3 and v4)`) and delete that line.

In `CHANGELOG.md`, add under `### Added`:

```markdown
- **Real `actions/upload-artifact`/`download-artifact` support.** A new
  local HTTP server (`internal/artifactserver`) implements both act's
  real legacy v3 REST routes and its v4 routes — confirmed the v4 wire
  format is plain JSON with camelCase field names (matching protojson's
  actual output from act's own `.pb.go` struct tags, not the misleading
  snake_case in act's own doc comment), hand-written without adding a
  protobuf dependency. Shares one server with a fresh, non-persisted
  store per `mirror run` invocation (the opposite of the cache store,
  which deliberately does persist — artifacts belong to one run, not
  restored across later ones). Always-on alongside the cache server, no
  new flag — a deliberate divergence from act's own opt-in
  `--artifact-server-path`. Also adds `GITHUB_RUN_ID`/`GITHUB_RUN_NUMBER`/
  `GITHUB_RUN_ATTEMPT` as real env vars (fixed placeholder values,
  proactively closing the same bug class Cache Runtime found with
  `GITHUB_REF`). Verified for real against both a current
  (`@v4`) and legacy pinned (`@v3`) version of the real, unmodified
  actions in a two-job upload-then-download workflow, confirming genuine
  content round-tripping through both protocols.
```

In `docs/design/specs/2026-09-14-mirror-gha-design.md`, add `**Implemented.**` right after the "Artifacts Runtime" heading:

```markdown
## Artifacts Runtime

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
git add examples/workflows/uses-artifact-v4.yml examples/workflows/uses-artifact-v3.yml examples/README.md docs/usage.md CHANGELOG.md docs/design/specs/2026-09-14-mirror-gha-design.md
git commit -m "feat: verify actions/upload-artifact and download-artifact end-to-end (v3+v4)"
```

## Self-Review Notes

- **Spec coverage:** Every piece of the "Artifacts Runtime" spec section maps to a task — storage layer (Task 1), v3 wire format (Task 2), v4 wire format (Task 3), server lifecycle + engine/CLI env wiring + proactive run-identity fix (Task 4), real end-to-end proof against both protocols + docs (Task 5).
- **Placeholder scan:** No TBD/TODO; every step has complete, real code. Task 5's "add temporary logging if it doesn't work" instruction is a documented contingency procedure, not a placeholder — same pattern used in the Cache Runtime plan, which caught three real bugs this way.
- **Type consistency:** `Store`'s methods (`OpenStore`, `NewTempStore`, `Resolve`, `WriteAt`, `Exists` — Task 1) are used identically in Task 2's v3 handlers and Task 3's v4 handlers. `v4BlobRel`/`artifactNameToID` (Task 3) are used consistently across `v4UploadArtifact`/`v4FinalizeArtifact`/`v4ListArtifacts`/`v4DownloadArtifact`/`v4DeleteArtifact`. `Server`'s `Port`/`Stop` (Task 4) match exactly how `cmd/mirror/main.go`'s wiring calls them, mirroring `cacheserver.Server`'s identical shape.
- **Known simplifications carried over from the design spec, restated at point of use:** no HMAC signature on mirror-gha's fake signed URLs (Task 3, Global Constraints) — a further simplification beyond what act itself already fakes; `host.docker.internal` networking (Task 4) is the same documented macOS-Docker-Desktop-first choice Cache Runtime already established; the exact v3/v4 wire-format field names (Tasks 2-3) are the one part of this plan not independently re-verified against a live client before Task 5 runs the real actions against them.
