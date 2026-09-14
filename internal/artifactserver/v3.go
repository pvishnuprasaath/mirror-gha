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
	Count int                             `json:"count"`
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
		// The "path" a real v3 client expects is prefixed by the artifact
		// name (itemPath here), NOT the run id (container) — found via
		// real end-to-end testing against actions/download-artifact@v3:
		// using container (e.g. "1") produced paths like "1/greeting.txt"
		// instead of "greeting-v3/greeting.txt", and the real client
		// silently reported "No downloadable files were found" rather
		// than erroring, making this easy to miss without a real client.
		pathPrefix := itemPath
		if pathPrefix == "" {
			pathPrefix = filepath.Base(container)
		}
		resp := containerItemResponse{}
		filepath.WalkDir(dir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(dir, path)
			displayName := strings.TrimSuffix(rel, ".gz__")
			resp.Value = append(resp.Value, containerItem{
				Path:            filepath.ToSlash(filepath.Join(pathPrefix, displayName)),
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
