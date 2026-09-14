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
