package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// TestDotNetSplitMatchesRegexSplit pins the shape .NET's Regex.Split produces,
// which is what the legacy expressions indexed. Go's regexp.Split cannot be used
// here because it drops the capture groups.
func TestDotNetSplitMatchesRegexSplit(t *testing.T) {
	t.Parallel()
	// Two DEBUG tags, as the .NET probe reported: the text before the match, the
	// two groups, the text between the matches, the two groups again, and the
	// trailing text.
	script := "A\n#OKE:DEBUG\nDebug = 1\nB\n#OKE:DEBUG\nDebug = 0\nC\n"
	parts := dotNetSplit(profile.DebugTagPattern, script)
	want := []string{
		"A\n",
		"\nDebug = ",
		"1",
		"\nB\n",
		"\nDebug = ",
		"0",
		"\nC\n",
	}
	if len(parts) != len(want) {
		t.Fatalf("dotNetSplit() returned %d parts, want %d: %q", len(parts), len(want), parts)
	}
	for i := range want {
		if parts[i] != want[i] {
			t.Errorf("part %d = %q, want %q", i, parts[i], want[i])
		}
	}

	// No match yields the input as the only element, so the callers' length
	// check is what turns "no tag" into an error.
	if got := dotNetSplit(profile.DebugTagPattern, "no tag here\n"); len(got) != 1 || got[0] != "no tag here\n" {
		t.Errorf("dotNetSplit() on a tagless script = %q, want the input alone", got)
	}
}

// TestBuildVpyTemplates pins the INPUTFILE splice against the shipped scripts.
// The template is [before, "\na=", "\"00000.m2ts\"", after], and the legacy
// expression drops the captured value and writes an r-string literal.
func TestBuildVpyTemplates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		file string
		// assignment is the prefix the tag's group 1 captured.
		assignment string
	}{
		{"demo.vpy", "\na="},
		{"demo_720p.vpy", "\na="},
		{"vfr.vpy", "\nA="},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(exampleDir, tc.file))
			if err != nil {
				t.Skipf("example script not available: %v", err)
			}
			script := string(raw)
			input := `D:\Main_Disc\BDMV\STREAM\00000.m2ts`

			got, err := buildVpy(script, input)
			if err != nil {
				t.Fatalf("buildVpy() error = %v", err)
			}
			if !strings.Contains(got, tc.assignment+`R"`+input+`"`) {
				t.Errorf("buildVpy() did not splice the source into %s: %q", tc.file, got)
			}
			// The old value is gone and everything else is unchanged.
			parts := dotNetSplit(profile.InputTagPattern, script)
			if len(parts) != 4 {
				t.Fatalf("the fixture %s no longer has exactly one INPUTFILE tag", tc.file)
			}
			want := parts[0] + parts[1] + `R"` + input + `"` + parts[3]
			if got != want {
				t.Errorf("buildVpy() = %q, want %q", got, want)
			}
		})
	}
}

// TestBuildVpyRejectsMissingTag pins the error the wizard showed when a script
// was not written for OKEGui.
func TestBuildVpyRejectsMissingTag(t *testing.T) {
	t.Parallel()
	if _, err := buildVpy("import vapoursynth as vs\n", "x.m2ts"); err == nil {
		t.Fatal("buildVpy() = nil error, want a missing-tag rejection")
	}
}

// TestBuildVpyOnlyRewritesTheFirstTag pins the .NET split result: the text after
// the first match is parts[3], so the text between the first match and the
// second tag survives, while the second tag itself is gone.
func TestBuildVpyOnlyRewritesTheFirstTag(t *testing.T) {
	t.Parallel()
	script := "head\n#OKE:INPUTFILE\na=\"one\"\nmid\n#OKE:INPUTFILE\nb=\"two\"\ntail\n"
	got, err := buildVpy(script, `C:\x.m2ts`)
	if err != nil {
		t.Fatalf("buildVpy() error = %v", err)
	}
	if !strings.Contains(got, `a=R"C:\x.m2ts"`) {
		t.Errorf("buildVpy() did not rewrite the first tag: %q", got)
	}
	if strings.Contains(got, "two") {
		t.Errorf("buildVpy() kept the second tag: %q", got)
	}
	if !strings.Contains(got, "mid") {
		t.Errorf("buildVpy() dropped the text between the two tags: %q", got)
	}
}

// TestRewriteScriptKeepsProjectDirWithoutATag pins that a script with no
// PROJECTDIR tag keeps its own projDir line: the rewrite only fires on a tag,
// and the wizard never invented one. vfr.vpy is that script.
func TestRewriteScriptKeepsProjectDirWithoutATag(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "vfr.json")
	dir := filepath.Dir(profilePath)

	raw, err := os.ReadFile(filepath.Join(dir, filepath.Base(p.InputScript)))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	got, err := rewriteScript(p, dir)
	if err != nil {
		t.Fatalf("rewriteScript() error = %v", err)
	}
	if strings.Contains(string(raw), "#OKE:PROJECTDIR") {
		t.Fatalf("the fixture %s now has a PROJECTDIR tag", p.InputScript)
	}
	if strings.Contains(got, `R"`+dir+`"`) {
		t.Errorf("rewriteScript() injected a project directory into a script with no tag: %q", got)
	}
	// Only the DEBUG tag changed. The expectation is built with dotNetSplit,
	// which is the reference transcription of the .NET operation: the tag line
	// is consumed and the captured prefix (which still contains the newline
	// before the assignment) is emitted in front of the literal None.
	parts := dotNetSplit(profile.DebugTagPattern, string(raw))
	if len(parts) != 4 {
		t.Fatalf("the fixture %s no longer has exactly one DEBUG tag", p.InputScript)
	}
	want := parts[0] + parts[1] + "None" + parts[3]
	if got != want {
		t.Errorf("rewriteScript() changed more than the DEBUG tag:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "#OKE:DEBUG") {
		t.Errorf("rewriteScript() kept the DEBUG tag: %q", got)
	}
}

// TestRewriteScriptResolvesRelativeInputScript pins that the profile's
// InputScript is resolved against the profile's directory, which is what
// AddTaskService.LoadVsScript did before reading it.
func TestRewriteScriptResolvesRelativeInputScript(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	dir := filepath.Dir(profilePath)
	// The parsed profile carries the bare name; only the reader resolves it.
	if filepath.IsAbs(p.InputScript) {
		t.Skipf("the fixture already carries an absolute script path: %q", p.InputScript)
	}
	if _, err := rewriteScript(p, dir); err != nil {
		t.Fatalf("rewriteScript() error = %v", err)
	}
}

// TestRewriteScriptRewritesDebugTag pins the DEBUG rewrite: the value becomes
// the literal None, which is how the wizard forced the debug branch off.
func TestRewriteScriptRewritesDebugTag(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	dir := filepath.Dir(profilePath)

	got, err := rewriteScript(p, dir)
	if err != nil {
		t.Fatalf("rewriteScript() error = %v", err)
	}
	if !containsLine(got, "Debug = None") {
		t.Errorf("rewriteScript() did not rewrite DEBUG: %q", got)
	}
	if !containsLine(got, `projDir = R"`+dir+`"`) {
		t.Errorf("rewriteScript() did not rewrite PROJECTDIR: %q", got)
	}
}
