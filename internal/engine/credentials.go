package engine

import (
	"fmt"
	"strings"
)

// validateCredentials checks a container/service's `credentials:` map has
// exactly the two keys GitHub Actions requires — "username" and
// "password" — matching act's own validation
// (pkg/runner/run_context.go's handleCredentials/handleServiceCredentials
// both reject anything other than exactly 2 keys). A nil map (no
// credentials: field at all) is not an error — it just means no registry
// auth is needed for this image.
func validateCredentials(creds map[string]string) (username, password string, err error) {
	if creds == nil {
		return "", "", nil
	}
	if len(creds) != 2 {
		return "", "", fmt.Errorf("credentials must have exactly \"username\" and \"password\" keys")
	}
	username, ok := creds["username"]
	if !ok || username == "" {
		return "", "", fmt.Errorf("credentials must have exactly \"username\" and \"password\" keys")
	}
	password, ok = creds["password"]
	if !ok || password == "" {
		return "", "", fmt.Errorf("credentials must have exactly \"username\" and \"password\" keys")
	}
	return username, password, nil
}

// registryHostFor parses the registry host out of an image reference,
// matching act's/Docker's own default-registry heuristic: the reference's
// first "/"-separated segment is the registry host only if it looks like
// one (contains a "." or ":", or is exactly "localhost") — otherwise the
// whole reference is a Docker Hub image (e.g. "node:20" or
// "myuser/myimage:latest") and the registry defaults to Docker Hub's
// canonical host, index.docker.io.
func registryHostFor(image string) string {
	const dockerHub = "index.docker.io"
	first, _, found := strings.Cut(image, "/")
	if !found {
		return dockerHub
	}
	if strings.ContainsAny(first, ".:") || first == "localhost" {
		return first
	}
	return dockerHub
}
