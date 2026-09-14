package actions

import (
	"os"
	"path/filepath"
)

// CacheRoot is where mirror-gha caches downloaded action source and the
// pinned Node runtime — os.UserCacheDir()/mirror-gha (e.g. ~/.cache/mirror-gha
// on Linux, ~/Library/Caches/mirror-gha on macOS).
func CacheRoot() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mirror-gha"), nil
}
