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
