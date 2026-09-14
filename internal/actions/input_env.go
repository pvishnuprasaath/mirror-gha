package actions

import (
	"regexp"
	"strings"
)

var inputEnvSanitizer = regexp.MustCompile(`[^A-Z0-9-]`)

func inputEnvKey(name string) string {
	return "INPUT_" + inputEnvSanitizer.ReplaceAllString(strings.ToUpper(name), "_")
}

// InputEnv computes the INPUT_* environment variables for an action
// invocation: action.yml's declared defaults, overridden by whatever the
// step's `with:` block set. with's values must already be fully resolved
// (expression-substituted) by the caller.
func InputEnv(metadata *ActionMetadata, with map[string]string) map[string]string {
	env := map[string]string{}
	for name, input := range metadata.Inputs {
		if input.Default != "" {
			env[inputEnvKey(name)] = input.Default
		}
	}
	for name, val := range with {
		env[inputEnvKey(name)] = val
	}
	return env
}
