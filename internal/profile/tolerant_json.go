package profile

// The profile format is frozen, and the files that are actually in use are not
// strict JSON: the technical directors write them by hand and every shipped
// example contains a trailing comma before a closing bracket. The legacy loader
// accepted that because Newtonsoft.Json and YamlDotNet both tolerate it.
//
// Go's encoding/json does not, so the raw text is normalised before decoding.
// The transformation is deliberately narrow: it removes commas that are
// immediately followed (ignoring whitespace) by a closing bracket or brace, and
// it never touches characters inside string literals.

// stripTrailingCommas removes JSON trailing commas from raw text.
func stripTrailingCommas(raw string) string {
	// Fast path: the overwhelming majority of profiles have no trailing comma,
	// and scanning is cheap, so only allocate when a change is needed.
	if !hasTrailingComma(raw) {
		return raw
	}

	out := make([]byte, 0, len(raw))
	inString := false
	escaped := false

	for i := 0; i < len(raw); i++ {
		c := raw[i]

		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		switch c {
		case '"':
			inString = true
			out = append(out, c)
		case ',':
			if nextNonSpaceCloses(raw, i+1) {
				// Drop the comma entirely.
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return string(out)
}

// hasTrailingComma reports whether raw contains a comma followed only by
// whitespace and a closing bracket, outside of any string literal.
func hasTrailingComma(raw string) bool {
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case ',':
			if nextNonSpaceCloses(raw, i+1) {
				return true
			}
		}
	}
	return false
}

// nextNonSpaceCloses reports whether the first non-whitespace byte at or after
// start is a closing bracket or brace.
func nextNonSpaceCloses(raw string, start int) bool {
	for i := start; i < len(raw); i++ {
		switch raw[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case ']', '}':
			return true
		default:
			return false
		}
	}
	return false
}
