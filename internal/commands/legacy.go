package commands

import "regexp"

// GitHub Actions supports two now-deprecated stdout-based workflow command
// formats for setting outputs, both predating the 2022 migration to the
// $GITHUB_OUTPUT file protocol (Phase 1's ParseKeyValueFile). Real GitHub
// Actions still parses both for backward compatibility, and a large
// fraction of real-world actions — including GitHub's own canonical demo,
// actions/hello-world-javascript-action — still use one of them:
//
//   - `::set-output name=<name>::<value>` — the commonly-documented
//     "deprecated" form.
//   - `##[set-output name=<name>;]<value>` — an older form still emitted
//     by some actions built against early @actions/core releases;
//     confirmed for real against actions/hello-world-javascript-action@v1's
//     actual output.
var (
	legacyColonSetOutputPattern   = regexp.MustCompile(`(?m)^::set-output name=([^:]+)::(.*)$`)
	legacyBracketSetOutputPattern = regexp.MustCompile(`(?m)^##\[set-output name=([^;]+);\](.*)$`)
)

// ParseLegacyOutputs extracts outputs set via either deprecated stdout-based
// workflow command format. Prefer $GITHUB_OUTPUT-file-based outputs when
// both are present for the same name — callers should merge this after
// the file-based parse so the file wins.
func ParseLegacyOutputs(stdout string) map[string]string {
	outputs := map[string]string{}
	for _, m := range legacyColonSetOutputPattern.FindAllStringSubmatch(stdout, -1) {
		outputs[m[1]] = m[2]
	}
	for _, m := range legacyBracketSetOutputPattern.FindAllStringSubmatch(stdout, -1) {
		outputs[m[1]] = m[2]
	}
	return outputs
}
