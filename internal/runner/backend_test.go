package runner

import (
	"errors"
	"testing"
)

func TestSelectBackend_SupportedLinux(t *testing.T) {
	for _, label := range []string{"ubuntu-latest", "ubuntu-22.04", "ubuntu-24.04"} {
		backend, err := SelectBackend(label)
		if err != nil {
			t.Errorf("SelectBackend(%q) error = %v", label, err)
		}
		if _, ok := backend.(*LinuxDockerBackend); !ok {
			t.Errorf("SelectBackend(%q) = %T, want *LinuxDockerBackend", label, backend)
		}
	}
}

func TestSelectBackend_UnsupportedRunner(t *testing.T) {
	_, err := SelectBackend("windows-latest")
	if err == nil {
		t.Fatal("SelectBackend(\"windows-latest\") error = nil, want ErrUnsupportedRunner")
	}
	var unsupported *ErrUnsupportedRunner
	if !errors.As(err, &unsupported) {
		t.Errorf("SelectBackend(\"windows-latest\") error = %T, want *ErrUnsupportedRunner", err)
	}
	if unsupported.RunsOn != "windows-latest" {
		t.Errorf("RunsOn = %q, want %q", unsupported.RunsOn, "windows-latest")
	}
}
