package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// hashFiles implements the hashFiles() expression function: match every
// pattern against files under root (github.workspace), then return a
// single SHA-256 hex digest over the concatenated contents of every
// matched file, read in sorted relative-path order. Matches act's own
// pure-Go fallback algorithm (pkg/exprparser/functions.go) — a single
// running hash over sorted file contents, not a hash-of-hashes — rather
// than shelling out to a real @actions/glob-based JS implementation the
// way act's primary path does. Zero matches returns "", nil, not an
// error, matching real GitHub Actions behavior.
//
// Pattern matching supports literal path segments, "*" (any run of
// characters within one segment), "?", and "**" (any number of
// directory segments, including zero) — enough for the overwhelmingly
// common real-world patterns like "**/package-lock.json" or "go.sum".
// It does not support "!" negation or brace expansion, unlike the real
// @actions/glob library — a documented v1 gap, not a silent wrong result
// (an unmatched pattern just matches nothing, same as today).
func hashFiles(root string, patterns []string) (string, error) {
	matched := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, pattern := range patterns {
			if globMatch(pattern, rel) {
				matched[rel] = true
				break
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(matched) == 0 {
		return "", nil
	}

	relPaths := make([]string, 0, len(matched))
	for rel := range matched {
		relPaths = append(relPaths, rel)
	}
	sort.Strings(relPaths)

	h := sha256.New()
	for _, rel := range relPaths {
		f, err := os.Open(filepath.Join(root, rel))
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// globMatch reports whether relPath (slash-separated) matches pattern
// (also slash-separated), supporting "**" as a whole path segment
// matching any number of directory segments — the classic doublestar
// glob algorithm, absent from the standard library's path/filepath.Match
// (which only ever matches within a single segment).
func globMatch(pattern, relPath string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(relPath, "/"))
}

func matchSegments(pat, path []string) bool {
	if len(pat) == 0 {
		return len(path) == 0
	}
	if pat[0] == "**" {
		if matchSegments(pat[1:], path) {
			return true
		}
		if len(path) == 0 {
			return false
		}
		return matchSegments(pat, path[1:])
	}
	if len(path) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], path[0])
	if err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], path[1:])
}
