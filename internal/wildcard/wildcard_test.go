package wildcard

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		// Plain comparisons: with no wildcard in the pattern every character
		// is literal, dots included.
		{"ep01.txt", "ep01.txt", true},
		{"EP01.TXT", "ep01.txt", true},
		{"ep01.txt", "EP01.TXT", true},
		{"ep01.txt", "ep02.txt", false},
		{"ep01xtxt", "ep01.txt", false},
		{"", "", true},
		{"ep01", "", false},
		{"", "ep01", false},

		// '*'.
		{"ep01", "ep01*", true},
		{"ep01.flac", "ep01*", true},
		{"ep01x.txt", "ep01*", true},
		{"ep02.flac", "ep01*", false},
		{"anything", "*", true},
		{"anything.at.all", "*.*", true},
		{"no-extension", "*.*", true},
		{"", "*", false},

		// '?' matches a single character, except that at a dot or at the end
		// of the name a run of them collapses to nothing. Windows really does
		// answer TRUE for the first three cases; they were measured.
		{"ep01", "ep01?", true},
		{"ep01x", "ep01?", true},
		{"ep01_", "ep01?", true},
		{"ep01x", "ep01??", true},
		{"ep01xy", "ep01??", true},
		{"ep01x.txt", "ep01?.txt", true},
		{"ep01.txt", "ep01?.txt", true},
		{"ep01.txt", "ep01?txt", false},

		// The legacy search pattern in its two shapes. `ep01*.*` finds the
		// extension-less `ep01` because the trailing `.*` may match nothing;
		// this is the case that makes filepath.Match unusable here.
		{"ep01", "ep01*.*", true},
		{"ep01.flac", "ep01*.*", true},
		{"ep01.a.b.c", "ep01*.*", true},
		{"ep01_extra.flac", "ep01*.*", true},
		{"ep01x", "ep01*.*", true},
		{"ep02.flac", "ep01*.*", false},
		{"Show.01.flac", "ep01*.*", false},

		// `ep01.*txt` anchors on a dot and must not run past the extension.
		{"ep01.txt", "ep01.*txt", true},
		{"ep01.jpn.txt", "ep01.*txt", true},
		{"EP01.TXT", "ep01.*txt", true},
		{"ep01.txtx", "ep01.*txt", false},
		{"ep01.jpn.txtx", "ep01.*txt", false},
		{"ep01jpn.txt", "ep01.*txt", false},
		{"ep01x.txt", "ep01.*txt", false},
		{"ep01", "ep01.*txt", false},

		// `*.mpls` is the Blu-ray playlist probe.
		{"a.mpls", "*.mpls", true},
		{"b.MPLS", "*.mpls", true},
		{"ep01.mplsx", "*.mpls", false},
		{"c.mpls.bak", "*.mpls", false},
		{"d", "*.mpls", false},

		// `*.txt` without an 8.3 alias present.
		{"ep01.txt", "*.txt", true},
		{"ep01.txtx", "*.txt", false},
		{"ep01", "*.txt", false},
	}
	for _, tt := range tests {
		t.Run(tt.pattern+"/"+tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Match(tt.name, tt.pattern); got != tt.want {
				t.Errorf("Match(%q, %q) = %v, want %v", tt.name, tt.pattern, got, tt.want)
			}
		})
	}
}

// TestMatchAgainstWindowsMatrix replays verdicts recorded from a real Windows
// machine, one candidate name per directory, via Directory.GetFiles.
//
// Each line is "pattern<TAB>name<TAB>1|0". The fixture only contains rows
// whose verdict is attributable to the long name: rows where the file's 8.3
// short name matched the pattern were dropped when it was generated, because
// the port deliberately does not reproduce that aliasing (see the package
// comment).
func TestMatchAgainstWindowsMatrix(t *testing.T) {
	t.Parallel()
	path := filepath.Join("testdata", "win_glob_matrix.tsv")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	lines := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			t.Fatalf("malformed fixture line %d: %q", lines+1, line)
		}
		pattern, name, verdict := fields[0], fields[1], fields[2]
		lines++
		want := verdict == "1"
		if got := Match(name, pattern); got != want {
			t.Errorf("Match(%q, %q) = %v, want %v", name, pattern, got, want)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if lines < 400 {
		t.Fatalf("fixture only had %d lines; it looks truncated", lines)
	}
}
