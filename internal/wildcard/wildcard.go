// Package wildcard implements the Windows file-search pattern matching that
// Directory.GetFiles performs, because two ported components depend on it: the
// intermediate-file cleaner and the chapter-file finder.
//
// Why this is not filepath.Match: the legacy build only ever ran on Windows,
// and the operators' working directories are full of names whose match depends
// on the DOS wildcard rules. The clearest example is that `ep01*.*` finds the
// extension-less `ep01` that sits next to the episode, while `ep01.*txt` does
// *not* find `ep01.txtx`. Go's own matcher gets both wrong.
//
// The rules are the MS-FSA "Algorithm for Determining if a FileName Is in an
// Expression": a pattern containing a wildcard is translated so that `*`
// becomes DOS_STAR, `?` becomes DOS_QM and `.` becomes DOS_DOT, and the two
// strings are then matched with backtracking. The implementation below was
// checked against 455 verdicts recorded from Directory.GetFiles on a real
// Windows machine (testdata/win_glob_matrix.tsv).
//
// What is deliberately not reproduced: Windows also matches against each
// file's 8.3 short name, so `dir *.txt` lists `ep01.txtx` in a normal
// directory. That aliasing is a filesystem artefact rather than a pattern
// rule, it cannot be reproduced without the on-disk short names, and the
// patterns this repository builds are prefix-shaped so the difference does not
// reach them.
package wildcard

import "strings"

// The DOS wildcard tokens. A real `*`, `?` or `.` in a pattern that contains
// any wildcard is translated into these before matching.
const (
	dosStar = '<'
	dosQM   = '>'
	dosDot  = '"'
)

// Match reports whether name matches a Windows search pattern.
//
// Matching is case-insensitive, as it is on Windows. A pattern with no
// wildcard character is a plain case-insensitive comparison.
func Match(name, pattern string) bool {
	expr := translate(pattern)
	n := []rune(strings.ToLower(name))
	e := []rune(strings.ToLower(expr))

	if len(n) == 0 || len(e) == 0 {
		return len(n) == 0 && len(e) == 0
	}
	// The two patterns Windows short-circuits: "*" and "*.*" match any name.
	if (len(e) == 1 && e[0] == dosStar) ||
		(len(e) == 3 && e[0] == dosStar && e[1] == dosDot && e[2] == dosStar) {
		return true
	}

	namePos, exprPos := 0, 0
	// starExpr is -1 while no star has been seen; from the first star on it
	// remembers where to resume the pattern when the greedy match fails.
	starExpr, starName := -1, 0

	for {
		if namePos >= len(n) && exprPos >= len(e) {
			return true
		}
		var nameChar, exprChar rune
		if namePos < len(n) {
			nameChar = n[namePos]
		}
		if exprPos < len(e) {
			exprChar = e[exprPos]
		}

		switch {
		case exprPos < len(e) && namePos < len(n) && isLiteral(exprChar) && exprChar == nameChar:
			namePos++
			exprPos++
			continue

		case exprChar == dosStar:
			starExpr, starName = exprPos, namePos
			exprPos++
			continue

		case exprChar == dosQM:
			if namePos >= len(n) || nameChar == '.' {
				// At a dot or the end of the name, DOS_QM consumes nothing
				// and the whole run of them is skipped.
				for exprPos < len(e) && e[exprPos] == dosQM {
					exprPos++
				}
				continue
			}
			namePos++
			exprPos++
			continue

		case exprChar == dosDot:
			// DOS_DOT matches a period or zero characters beyond the name.
			if namePos >= len(n) {
				exprPos++
				continue
			}
			if nameChar == '.' {
				namePos++
				exprPos++
				continue
			}
		}

		if starExpr >= 0 && starName < len(n) {
			starName++
			namePos = starName
			exprPos = starExpr + 1
			continue
		}
		return false
	}
}

// translate replaces the wildcard characters with their DOS tokens, but only
// when the pattern actually contains a wildcard. A pattern of pure literals
// keeps its dots literal, so `ntdll.dll` does not match `ntdllxdll`.
//
// The translation follows TranslateWin32Expression, and the asymmetry is what
// makes the trailing shapes behave: only a dot that sits immediately before a
// `*` or `?` becomes DOS_DOT, so `ep01.txt` keeps a literal dot and cannot match
// `ep01xtxt`, while `ep01.*txt` anchors on its dot. A dot directly after a star
// at the very end of the pattern is folded away, because the star may already
// match nothing there.
func translate(pattern string) string {
	if !strings.ContainsAny(pattern, `*?`) {
		return pattern
	}
	rs := []rune(pattern)
	var b strings.Builder
	b.Grow(len(pattern))
	for i, c := range rs {
		switch c {
		case '*':
			b.WriteRune(dosStar)
		case '?':
			b.WriteRune(dosQM)
		case '.':
			if i == len(rs)-1 && i >= 1 && rs[i-1] == '*' {
				// Trailing "*.": the star written above already covers the dot.
				continue
			}
			if i < len(rs)-1 && (rs[i+1] == '*' || rs[i+1] == '?') {
				b.WriteRune(dosDot)
			} else {
				b.WriteRune('.')
			}
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

// isLiteral reports whether c is a plain character or DOS_DOT. A DOS_DOT that
// does not match a period is not a literal here: the caller falls through to
// its "zero characters beyond the name" rule instead.
func isLiteral(c rune) bool {
	return c != '*' && c != dosStar && c != '?' && c != dosQM && c != dosDot
}
