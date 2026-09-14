package commands

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCreateFileSet(t *testing.T) {
	dir := t.TempDir()
	fs, err := CreateFileSet(dir)
	if err != nil {
		t.Fatalf("CreateFileSet() error = %v", err)
	}
	for _, path := range []string{fs.EnvFile, fs.PathFile, fs.OutputFile, fs.SummaryFile} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file %s to exist: %v", path, err)
		}
	}
}

func TestParseKeyValueFile_SimpleAssignment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output")
	if err := os.WriteFile(path, []byte("NAME=value\nOTHER=thing\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	result, err := ParseKeyValueFile(path)
	if err != nil {
		t.Fatalf("ParseKeyValueFile() error = %v", err)
	}
	want := map[string]string{"NAME": "value", "OTHER": "thing"}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

func TestParseKeyValueFile_Heredoc(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output")
	content := "NAME<<EOF\nline one\nline two\nEOF\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	result, err := ParseKeyValueFile(path)
	if err != nil {
		t.Fatalf("ParseKeyValueFile() error = %v", err)
	}
	want := map[string]string{"NAME": "line one\nline two"}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}

func TestParseLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "path")
	if err := os.WriteFile(path, []byte("/usr/local/bin\n/opt/tool/bin\n"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	result, err := ParseLines(path)
	if err != nil {
		t.Fatalf("ParseLines() error = %v", err)
	}
	want := []string{"/usr/local/bin", "/opt/tool/bin"}
	if !reflect.DeepEqual(result, want) {
		t.Errorf("result = %v, want %v", result, want)
	}
}
