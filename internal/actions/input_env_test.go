package actions

import "testing"

func TestInputEnv_UsesDefaultWhenNotOverridden(t *testing.T) {
	meta := &ActionMetadata{
		Inputs: map[string]ActionInput{
			"who-to-greet": {Default: "World"},
		},
	}
	env := InputEnv(meta, map[string]string{})
	if env["INPUT_WHO-TO-GREET"] != "World" {
		t.Errorf(`env["INPUT_WHO-TO-GREET"] = %q, want %q`, env["INPUT_WHO-TO-GREET"], "World")
	}
}

func TestInputEnv_WithOverridesDefault(t *testing.T) {
	meta := &ActionMetadata{
		Inputs: map[string]ActionInput{
			"who-to-greet": {Default: "World"},
		},
	}
	env := InputEnv(meta, map[string]string{"who-to-greet": "mirror-gha"})
	if env["INPUT_WHO-TO-GREET"] != "mirror-gha" {
		t.Errorf(`env["INPUT_WHO-TO-GREET"] = %q, want %q`, env["INPUT_WHO-TO-GREET"], "mirror-gha")
	}
}

func TestInputEnv_NonAlphanumericBecomesUnderscore(t *testing.T) {
	meta := &ActionMetadata{Inputs: map[string]ActionInput{}}
	env := InputEnv(meta, map[string]string{"my input.name": "value"})
	if env["INPUT_MY_INPUT_NAME"] != "value" {
		t.Errorf("env = %v, want INPUT_MY_INPUT_NAME=value", env)
	}
}
