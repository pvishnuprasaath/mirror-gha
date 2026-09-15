package commands

import (
	"regexp"
	"strings"
)

// GitHub Actions' stdout-based workflow commands beyond the already-built
// $GITHUB_OUTPUT-adjacent set-output forms (legacy.go). Real GitHub
// Actions renders these specially in its own log UI (foldable groups,
// annotated errors/warnings, hidden debug lines, redacted masked values);
// mirror-gha has no such UI, so ProcessWorkflowCommands renders a
// plain-terminal equivalent instead of act's own choice (which just logs
// every unrecognized command verbatim with a "❓" prefix) — this project
// aims for more legible local output, not byte-for-byte act parity here.
var (
	addMaskPattern  = regexp.MustCompile(`(?m)^::add-mask::(.*)$`)
	groupPattern    = regexp.MustCompile(`(?m)^::group::(.*)$`)
	endGroupPattern = regexp.MustCompile(`(?m)^::endgroup::\s*$`)
	// error/warning/notice optionally carry comma-separated key=value
	// parameters (file,line,col,endLine,endColumn,title) before the
	// closing "::" — real GitHub Actions uses these to annotate a
	// specific source location, but mirror-gha's plain-terminal output
	// only ever renders the message itself, matching act's own choice not
	// to render them either (though act doesn't implement notice: at
	// all, and treats error/warning as simple log lines with no special
	// handling of these parameters either).
	annotationPattern = regexp.MustCompile(`(?m)^::(error|warning|notice)(?:\s+[^:]*)?::(.*)$`)
	debugPattern      = regexp.MustCompile(`(?m)^::debug::(.*)$`)
)

// ProcessWorkflowCommands scans text line-by-line for the workflow
// commands above, returning the rendered display text (with those lines
// replaced by a plain-terminal equivalent, or removed entirely for
// ::add-mask::/::endgroup:: and for ::debug:: when showDebug is false)
// and every value registered via ::add-mask:: found in text, for the
// caller to redact from this and all subsequent output in the same job —
// matching act's own real ::add-mask:: mechanism (a per-job mask
// registry, applied via literal case-sensitive substring replacement),
// which is the one command among this set act actually implements for
// real rather than just logging verbatim.
func ProcessWorkflowCommands(text string, showDebug bool) (display string, masks []string) {
	for _, m := range addMaskPattern.FindAllStringSubmatch(text, -1) {
		masks = append(masks, m[1])
	}

	lines := strings.Split(text, "\n")
	var out []string
	for _, line := range lines {
		switch {
		case addMaskPattern.MatchString(line):
			continue // operational, not meant for a user watching output
		case endGroupPattern.MatchString(line):
			continue // no foldable UI locally — the group's start marker is enough
		case groupPattern.MatchString(line):
			title := groupPattern.FindStringSubmatch(line)[1]
			out = append(out, "▶ "+title)
		case debugPattern.MatchString(line):
			if showDebug {
				out = append(out, "🐛 "+debugPattern.FindStringSubmatch(line)[1])
			}
			// hidden by default, matching real GitHub Actions' own
			// default-hidden step-debug logging — act doesn't gate this
			// at all, mirror-gha is more correct here.
		default:
			if m := annotationPattern.FindStringSubmatch(line); m != nil {
				out = append(out, annotationPrefix(m[1])+m[2])
			} else {
				out = append(out, line)
			}
		}
	}
	return strings.Join(out, "\n"), masks
}

func annotationPrefix(level string) string {
	switch level {
	case "error":
		return "❌ "
	case "warning":
		return "⚠️  "
	case "notice":
		return "ℹ️  "
	default:
		return ""
	}
}

// RedactMasks replaces every occurrence of each mask value in text with
// "***" — a case-sensitive literal substring replace, matching act's own
// real ::add-mask:: redaction mechanism exactly (logger.go's valueMasker).
func RedactMasks(text string, masks []string) string {
	for _, v := range masks {
		if v != "" {
			text = strings.ReplaceAll(text, v, "***")
		}
	}
	return text
}
