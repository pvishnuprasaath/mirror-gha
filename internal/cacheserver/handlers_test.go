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
