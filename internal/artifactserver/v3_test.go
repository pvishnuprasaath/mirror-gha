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
