package cacheserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"

	"mirror-gha/internal/actions"
)

// Server is a running cache HTTP server.
type Server struct {
	listener net.Listener
	httpSrv  *http.Server
}

// Start begins serving the cache API on an OS-assigned port, bound to
// all interfaces (0.0.0.0) so a job container can reach it via
// host.docker.internal — see the design spec's Cache Runtime section
// for why this, not act's outbound-IP-binding approach, is this
// project's v1 choice (this project's actual dev/test environment is
// macOS Docker Desktop, where host.docker.internal is a built-in DNS
// alias; plain Linux dockerd needs extra --add-host configuration not
// yet wired up here).
func Start(store *Store) (*Server, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("listen for cache server: %w", err)
	}
	httpSrv := &http.Server{Handler: NewHandler(store)}
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

// StoreRoot is where the cache store persists across mirror run
// invocations — a subdirectory of mirror-gha's shared cache root,
// alongside the existing actions/ and node/ subdirectories.
func StoreRoot() (string, error) {
	root, err := actions.CacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "action-cache"), nil
}
