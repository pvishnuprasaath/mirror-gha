package commands

import (
	"reflect"
	"testing"
)

func TestParseLegacyOutputs_ColonFormat(t *testing.T) {
	stdout := "some log line\n::set-output name=time::13:31:15 GMT+0000\nmore log\n"
	got := ParseLegacyOutputs(stdout)
	want := map[string]string{"time": "13:31:15 GMT+0000"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLegacyOutputs() = %v, want %v", got, want)
	}
}

func TestParseLegacyOutputs_BracketFormat(t *testing.T) {
	// Exact format confirmed for real against actions/hello-world-javascript-action@v1.
	stdout := "Hello mirror-gha!\n##[set-output name=time;]13:31:15 GMT+0000 (Coordinated Universal Time)\nThe event payload: {}\n"
	got := ParseLegacyOutputs(stdout)
	want := map[string]string{"time": "13:31:15 GMT+0000 (Coordinated Universal Time)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLegacyOutputs() = %v, want %v", got, want)
	}
}

func TestParseLegacyOutputs_NoMatchesReturnsEmptyMap(t *testing.T) {
	got := ParseLegacyOutputs("just some regular output\nnothing special here\n")
	if len(got) != 0 {
		t.Errorf("ParseLegacyOutputs() = %v, want empty map", got)
	}
}

func TestParseLegacyOutputs_MultipleOutputs(t *testing.T) {
	stdout := "::set-output name=first::one\n::set-output name=second::two\n"
	got := ParseLegacyOutputs(stdout)
	want := map[string]string{"first": "one", "second": "two"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseLegacyOutputs() = %v, want %v", got, want)
	}
}
