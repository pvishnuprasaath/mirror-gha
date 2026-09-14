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

// Field names below are camelCase for RESPONSES mirror-gha sends (this
// works — confirmed for real against the actual actions/upload-artifact@v4
// client, which parses them fine) — but the REAL client's own outgoing
// REQUESTS use snake_case (e.g. "workflow_run_backend_id"), not the
// camelCase act's .pb.go struct tags suggested. protojson's Unmarshal
// accepts both forms by design, which is presumably why act's server
// never needed to care; mirror-gha's plain encoding/json structs don't
// get that leniency for free, but the only fields actually READ here
// (name, size) happen to be single words with no casing ambiguity, so
// this was a correctness non-issue in practice — found and confirmed via
// real end-to-end testing, not assumed. The genuinely load-bearing find
// from that same testing: 64-bit int fields (size, artifactId) are
// wire-encoded as JSON STRINGS, not numbers — standard protojson
// behavior for int64/uint64 to avoid precision loss in JS — handled
// below via Go's built-in `,string` json tag option.
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
	Size int64  `json:"size,string"`
}

type finalizeArtifactResponse struct {
	OK         bool  `json:"ok"`
	ArtifactID int64 `json:"artifactId,string"`
}

type listArtifactsRequest struct {
	NameFilter string `json:"nameFilter"`
}

type artifactInfo struct {
	Name      string `json:"name"`
	Size      int64  `json:"size,string"`
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
	ArtifactID int64 `json:"artifactId,string"`
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
		// The real v4 client speaks Azure Blob Storage's block-upload
		// protocol against this URL: comp=block/appendBlock requests
		// carry real content bytes; a final comp=blocklist request
		// carries an XML block-list manifest, not content — found via
		// real end-to-end testing: writing every PUT unconditionally
		// let that manifest silently overwrite the real uploaded zip
		// bytes with garbage. Only comp=block/appendBlock (or no comp
		// param at all) writes to the blob; comp=blocklist is a no-op.
		if comp := r.URL.Query().Get("comp"); comp == "blocklist" {
			w.WriteHeader(http.StatusCreated)
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
