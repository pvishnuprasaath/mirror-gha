package runner

import (
	"reflect"
	"testing"
)

func TestSplitDockerOptions_Empty(t *testing.T) {
	got, err := splitDockerOptions("")
	if err != nil {
		t.Fatalf("splitDockerOptions(\"\") error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("splitDockerOptions(\"\") = %v, want empty", got)
	}
}

func TestSplitDockerOptions_WhitespaceOnly(t *testing.T) {
	got, err := splitDockerOptions("   \t  ")
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("splitDockerOptions() = %v, want empty", got)
	}
}

func TestSplitDockerOptions_SingleToken(t *testing.T) {
	got, err := splitDockerOptions("--cpus")
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	want := []string{"--cpus"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitDockerOptions() = %v, want %v", got, want)
	}
}

func TestSplitDockerOptions_MultipleTokens(t *testing.T) {
	got, err := splitDockerOptions("--cpus 2 --memory 512m")
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	want := []string{"--cpus", "2", "--memory", "512m"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitDockerOptions() = %v, want %v", got, want)
	}
}

func TestSplitDockerOptions_DoubleQuotedWithSpace(t *testing.T) {
	got, err := splitDockerOptions(`--health-cmd "pg_isready -U postgres" --health-interval 2s`)
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	want := []string{"--health-cmd", "pg_isready -U postgres", "--health-interval", "2s"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitDockerOptions() = %v, want %v", got, want)
	}
}

func TestSplitDockerOptions_SingleQuoted(t *testing.T) {
	got, err := splitDockerOptions(`--label 'a value with spaces'`)
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	want := []string{"--label", "a value with spaces"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitDockerOptions() = %v, want %v", got, want)
	}
}

func TestSplitDockerOptions_AdjacentQuoteNoSpace(t *testing.T) {
	// A quote can start mid-token — everything from the quote character
	// onward (up to the matching close quote) is treated as part of that
	// same token, with the quote characters themselves stripped.
	got, err := splitDockerOptions(`--foo"bar baz"`)
	if err != nil {
		t.Fatalf("splitDockerOptions() error = %v", err)
	}
	want := []string{"--foobar baz"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("splitDockerOptions() = %v, want %v", got, want)
	}
}

func TestSplitDockerOptions_UnterminatedQuote(t *testing.T) {
	_, err := splitDockerOptions(`--health-cmd "pg_isready`)
	if err == nil {
		t.Fatal("splitDockerOptions() error = nil, want error for unterminated quote")
	}
}
