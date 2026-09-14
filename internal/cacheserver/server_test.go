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
