package runner

import "fmt"

// splitDockerOptions tokenizes a ContainerSpec.Options string into
// individual docker CLI arguments. It's a hand-rolled, deliberately
// simple scanner — not a full shell-word tokenizer — because the only
// real-world need is whitespace-separated tokens with occasional quoted
// substrings that themselves contain spaces (e.g.
// `--health-cmd "pg_isready -U postgres"`). No escape-sequence support:
// GitHub Actions' own `options:` field doesn't document any either.
func splitDockerOptions(s string) ([]string, error) {
	var tokens []string
	var current []rune
	inToken := false
	var quote rune // 0 when not inside a quoted region

	flush := func() {
		if inToken {
			tokens = append(tokens, string(current))
			current = nil
			inToken = false
		}
	}

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			current = append(current, r)
			continue
		}
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		case r == '"' || r == '\'':
			quote = r
			inToken = true
		default:
			inToken = true
			current = append(current, r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote in options: %q", s)
	}
	flush()
	return tokens, nil
}
