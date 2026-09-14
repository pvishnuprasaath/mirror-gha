package artifactserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
)

// Server is a running artifact HTTP server, serving both v3 and v4
// routes on one listener.
type Server struct {
	listener net.Listener
	httpSrv  *http.Server
}

// Start begins serving the artifact API (v3 and v4 together) on an
// OS-assigned port, bound to all interfaces (0.0.0.0) so a job container
// can reach it via host.docker.internal — same reasoning as
// cacheserver.Start (see the design spec's Cache Runtime section): this
// project's actual dev/test environment is macOS Docker Desktop, where
// host.docker.internal is a built-in DNS alias; plain Linux dockerd
// needs extra configuration not yet wired up here.
func Start(store *Store) (*Server, error) {
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("listen for artifact server: %w", err)
	}
	mux := http.NewServeMux()
	registerV3Routes(mux, store)
	registerV4Routes(mux, store)
	httpSrv := &http.Server{Handler: mux}
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
