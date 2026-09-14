package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileSet is the set of temp files GitHub Actions uses for the file-based
// workflow command protocol (GITHUB_ENV, GITHUB_PATH, GITHUB_OUTPUT,
// GITHUB_STEP_SUMMARY).
type FileSet struct {
	EnvFile     string
	PathFile    string
	OutputFile  string
	SummaryFile string
}

// CreateFileSet creates empty workflow-command files in dir and returns
// their paths, ready to be bind-mounted/injected into a step's process.
func CreateFileSet(dir string) (*FileSet, error) {
	fs := &FileSet{
		EnvFile:     filepath.Join(dir, "github_env"),
		PathFile:    filepath.Join(dir, "github_path"),
		OutputFile:  filepath.Join(dir, "github_output"),
		SummaryFile: filepath.Join(dir, "github_step_summary"),
	}
	for _, path := range []string{fs.EnvFile, fs.PathFile, fs.OutputFile, fs.SummaryFile} {
		if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
			return nil, fmt.Errorf("create %s: %w", path, err)
		}
	}
	return fs, nil
}

// ParseKeyValueFile parses GITHUB_ENV / GITHUB_OUTPUT format: either
// `NAME=value` single-line entries, or heredoc-style `NAME<<EOF` / value
// lines / `EOF` multiline entries — the same format GitHub Actions itself
// writes and reads.
func ParseKeyValueFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	result := map[string]string{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if idx := strings.Index(line, "<<"); idx != -1 {
			name := line[:idx]
			delim := line[idx+2:]
			var valueLines []string
			for scanner.Scan() {
				l := scanner.Text()
				if l == delim {
					break
				}
				valueLines = append(valueLines, l)
			}
			result[name] = strings.Join(valueLines, "\n")
			continue
		}
		if idx := strings.Index(line, "="); idx != -1 {
			result[line[:idx]] = line[idx+1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return result, nil
}

// ParseLines reads GITHUB_PATH format: one path-to-prepend per line.
func ParseLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var lines []string
	for _, l := range strings.Split(string(data), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}
