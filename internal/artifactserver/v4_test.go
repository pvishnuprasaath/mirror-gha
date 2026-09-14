package artifactserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

	// Finalize. size is sent as a JSON STRING, not a number — confirmed
	// for real against the actual actions/upload-artifact@v4 client:
	// protojson encodes int64/uint64 fields as decimal strings to avoid
	// precision loss in JS, and the real client does exactly this.
	finalizeBody, _ := json.Marshal(map[string]interface{}{"name": "my-v4-artifact", "size": fmt.Sprintf("%d", len(content))})
	finalizeResp, err := http.Post(ts.URL+v4RouteBase+"/FinalizeArtifact", "application/json", bytes.NewReader(finalizeBody))
	if err != nil {
		t.Fatalf("FinalizeArtifact error = %v", err)
	}
	var finalizeRespBody struct {
		OK         bool  `json:"ok"`
		ArtifactID int64 `json:"artifactId,string"`
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
			Size int64  `json:"size,string"`
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

func TestV4_UploadArtifact_BlocklistCompIsNoOp(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	mux := http.NewServeMux()
	registerV4Routes(mux, store)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	realContent := []byte("real zip bytes")
	uploadReq, _ := http.NewRequest(http.MethodPut, ts.URL+v4RouteBase+"/UploadArtifact?artifactName=blocklist-test&comp=block", bytes.NewReader(realContent))
	uploadResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		t.Fatalf("comp=block upload error = %v", err)
	}
	uploadResp.Body.Close()

	// The real client's final comp=blocklist request carries an XML
	// manifest, not content — it must NOT overwrite the real bytes
	// already written by the comp=block request above.
	manifestReq, _ := http.NewRequest(http.MethodPut, ts.URL+v4RouteBase+"/UploadArtifact?artifactName=blocklist-test&comp=blocklist", strings.NewReader("<BlockList>fake manifest</BlockList>"))
	manifestResp, err := http.DefaultClient.Do(manifestReq)
	if err != nil {
		t.Fatalf("comp=blocklist request error = %v", err)
	}
	manifestResp.Body.Close()
	if manifestResp.StatusCode != http.StatusCreated {
		t.Fatalf("comp=blocklist status = %d, want 201", manifestResp.StatusCode)
	}

	path, err := store.Resolve(v4BlobRel("blocklist-test"))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	if string(data) != string(realContent) {
		t.Errorf("blob content = %q, want %q (comp=blocklist must not overwrite it)", data, realContent)
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
