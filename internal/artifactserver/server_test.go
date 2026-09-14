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
