package wizard

import (
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// loadExample copies one shipped profile into a temporary directory, copies the
// script it names next to it, and parses it. The profile's directory becomes the
// project directory, so nothing is ever written into the repository.
//
// The returned path is the profile's; its directory is the root of the working
// and output trees.
func loadExample(t *testing.T, name string) (*profile.Profile, string) {
	t.Helper()
	profilePath, dir := exampleProfile(t, name)
	p, err := profile.Load(profilePath)
	if err != nil {
		t.Fatalf("profile.Load(%s) error = %v", profilePath, err)
	}
	if p.InputScript == "" {
		t.Fatalf("%s has no InputScript", name)
	}
	copyExampleVpy(t, dir, filepath.Base(p.InputScript))
	return p, profilePath
}

// legacyWorking is an independent transcription of the legacy expression
// (WizardWindow.xaml.cs:272-311) that the tests use as their oracle. It is
// deliberately a second implementation: when it and the package agree, the port
// is faithful, and a bug in one shows up as a disagreement rather than as two
// copies of the same mistake.
//
// The .NET semantics that matter are reproduced explicitly:
//
//   - `Replace(':', '_')` runs over the whole path.
//   - The component strip is a case-sensitive Regex.Replace of
//     `[/\\]comp[/\\]`, scanning left to right without overlapping matches.
//   - `Path.Combine` lets a rooted second argument win.
//   - `Regex.Replace(path, "[/\\]._[/\\]", "\\output\\")` rewrites any
//     single-character level whose name ends in "_".
func legacyWorking(projectDir, inputFile string, reducePath bool) string {
	dir := normalizeSeparators(projectDir)
	input := normalizeSeparators(inputFile)
	if !isRooted(input) {
		input = filepath.Join(dir, input)
	}

	suffix := strings.ReplaceAll(input, ":", "_")
	for _, comp := range StripComponents {
		suffix = legacyStrip(suffix, comp)
	}
	components := splitComponents(suffix)
	if len(components) > 3 && reducePath {
		last := components[len(components)-2]
		file := components[len(components)-1]
		if volNumberPattern.MatchString(last) {
			suffix = strings.Join([]string{components[0], last, file}, string(filepath.Separator))
		} else {
			prefix := strings.Join(components[1:len(components)-2], `\`)
			suffix = strings.Join([]string{
				components[0],
				upperHex8(crc32.ChecksumIEEE([]byte(prefix))) + "-" + last,
				file,
			}, string(filepath.Separator))
		}
	}

	if isRooted(suffix) {
		return suffix
	}
	return filepath.Join(dir, suffix)
}

// legacyStrip is the .NET Regex.Replace scan: leftmost match, then resume after
// the separator the match consumed.
func legacyStrip(path, comp string) string {
	sep := string(filepath.Separator)
	needle := sep + comp + sep
	var b strings.Builder
	for i := 0; i < len(path); {
		j := strings.Index(path[i:], needle)
		if j < 0 {
			b.WriteString(path[i:])
			break
		}
		b.WriteString(path[i : i+j])
		b.WriteString(sep)
		i += j + len(needle)
	}
	return b.String()
}

// legacyOutput is the legacy `Regex.Replace(newPath, "[/\\]._[/\\]", "\\output\\")`.
func legacyOutput(working string) string {
	sep := byte(filepath.Separator)
	replacement := string(sep) + outputDirName + string(sep)
	var b strings.Builder
	for i := 0; i < len(working); {
		j := -1
		for k := i; k+4 <= len(working); k++ {
			if working[k] == sep && working[k+2] == '_' && working[k+3] == sep {
				j = k
				break
			}
		}
		if j < 0 {
			b.WriteString(working[i:])
			break
		}
		b.WriteString(working[i:j])
		b.WriteString(replacement)
		i = j + 4
	}
	return b.String()
}

// shallowInput is a rooted path with as few components as the platform allows:
// a drive and a file name on Windows, a file name under the root elsewhere.
func shallowInput(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(rootDir(t), name)
}

// rootDir is the volume root of the working directory, which is the shallowest
// place a test can put a source file.
func rootDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	return filepath.VolumeName(dir) + string(filepath.Separator)
}

// TestDeriveFromRealProfile assembles demo.json, whose three inputs are the
// relative `Main_Disc\BDMV\STREAM\NNNNN.m2ts` entries the shipped example uses.
//
// The expectations come from legacyWorking, the independent transcription of
// the legacy expression.
func TestDeriveFromRealProfile(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	dir := filepath.Dir(profilePath)

	res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	if len(res.Tasks) != len(p.InputFiles) {
		t.Fatalf("got %d tasks, want %d", len(res.Tasks), len(p.InputFiles))
	}

	for i, task := range res.Tasks {
		wantInput := filepath.Join(dir, normalizeSeparators(p.InputFiles[i]))
		if task.InputFile != wantInput {
			t.Errorf("task %d InputFile = %q, want %q", i, task.InputFile, wantInput)
		}
		wantWork := legacyWorking(dir, p.InputFiles[i], true)
		if task.WorkingPathPrefix != wantWork {
			t.Errorf("task %d WorkingPathPrefix = %q, want %q", i, task.WorkingPathPrefix, wantWork)
		}
		wantOut := legacyOutput(wantWork)
		if task.OutputPathPrefix != wantOut {
			t.Errorf("task %d OutputPathPrefix = %q, want %q", i, task.OutputPathPrefix, wantOut)
		}
		// The generated name appends the timestamp to the working prefix, so
		// the source extension is still in it.
		wantVpy := wantWork + "-09200905.vpy"
		if task.VpyFile != wantVpy {
			t.Errorf("task %d VpyFile = %q, want %q", i, task.VpyFile, wantVpy)
		}
		if task.Profile == p {
			t.Fatalf("task %d shares the input profile", i)
		}
		if task.Profile.InputScript != wantVpy {
			t.Errorf("task %d profile InputScript = %q, want %q", i, task.Profile.InputScript, wantVpy)
		}
		if task.Profile.WorkingPathPrefix != wantWork || task.Profile.OutputPathPrefix != wantOut {
			t.Errorf("task %d profile paths = %q / %q, want %q / %q",
				i, task.Profile.WorkingPathPrefix, task.Profile.OutputPathPrefix, wantWork, wantOut)
		}
		if !strings.Contains(task.Script, `R"`+wantInput+`"`) {
			t.Errorf("task %d script does not reference %q", i, wantInput)
		}
		// The three inputs are siblings, so the shortening yields one directory
		// for all of them.
		if !strings.Contains(task.WorkingPathPrefix, "-Main_Disc"+string(filepath.Separator)) {
			t.Errorf("task %d WorkingPathPrefix = %q, want the hashed Main_Disc level", i, task.WorkingPathPrefix)
		}
	}

	// The input profile must be untouched.
	if p.InputScript != "demo.vpy" {
		t.Errorf("input profile InputScript = %q, want the original", p.InputScript)
	}
	if p.WorkingPathPrefix != "" || p.OutputPathPrefix != "" {
		t.Errorf("input profile paths = %q / %q, want empty", p.WorkingPathPrefix, p.OutputPathPrefix)
	}

	// The three inputs share a parent, so reducePath records one entry — unless
	// the project directory is shallow enough that the stripped path has three
	// components or fewer, in which case the legacy code did not shorten at all.
	if len(res.ReducePathMap) == 0 {
		stripped, _, err := PathSuffix(filepath.Join(dir, normalizeSeparators(p.InputFiles[0])), false)
		if err != nil {
			t.Fatalf("PathSuffix() error = %v", err)
		}
		if len(splitComponents(stripped)) <= 3 {
			t.Skipf("the project directory %q is too shallow for reducePath", dir)
		}
		t.Fatalf("ReducePathMap is empty, want one entry for %q", stripped)
	}
	if len(res.ReducePathMap) != 1 {
		t.Fatalf("ReducePathMap = %+v, want one entry", res.ReducePathMap)
	}
	// The prefix is everything between the escaped drive level and the last
	// level: the project directory's own levels, without "Main_Disc" (which is
	// the level that gets hashed) and without the file name.
	volume := filepath.VolumeName(dir)
	parts := strings.Split(strings.TrimLeft(strings.TrimPrefix(dir, volume), `/\`), string(filepath.Separator))
	wantPrefix := strings.Join(parts, `\`)
	if res.ReducePathMap[0].Prefix != wantPrefix {
		t.Errorf("ReducePathMap prefix = %q, want %q", res.ReducePathMap[0].Prefix, wantPrefix)
	}
	if want := crc32.ChecksumIEEE([]byte(wantPrefix)); res.ReducePathMap[0].CRC != want {
		t.Errorf("ReducePathMap CRC = %08X, want %08X (the CRC of %q)",
			res.ReducePathMap[0].CRC, want, wantPrefix)
	}
	if res.MapFile != filepath.Join(dir, outputDirName, reducePathMapName) {
		t.Errorf("MapFile = %q, want the output directory next to the profile", res.MapFile)
	}
}

// TestDeriveWithoutReducePath pins the switch: with reducePath off the whole
// stripped path is used and no mapping is recorded.
func TestDeriveWithoutReducePath(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	dir := filepath.Dir(profilePath)

	res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: false, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	if len(res.ReducePathMap) != 0 {
		t.Errorf("ReducePathMap = %+v, want none when reducePath is off", res.ReducePathMap)
	}
	for i, task := range res.Tasks {
		want := legacyWorking(dir, p.InputFiles[i], false)
		if task.WorkingPathPrefix != want {
			t.Errorf("task %d WorkingPathPrefix = %q, want %q", i, task.WorkingPathPrefix, want)
		}
		if strings.Contains(task.WorkingPathPrefix, "-Main_Disc") {
			t.Errorf("task %d WorkingPathPrefix = %q, want no CRC32 tag", i, task.WorkingPathPrefix)
		}
	}
}

// TestDeriveShortensLongPath pins the CRC32 of a known prefix. The value is the
// one the legacy expression produced in .NET for the prefix "A\B\C", and it is
// asserted independently of the code under test.
func TestDeriveShortensLongPath(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo_720p.json")
	dir := filepath.Dir(profilePath)
	// A rooted path keeps the project directory out of the effective path, so
	// the expectation is the same on every platform.
	root := rootDir(t)
	p.InputFiles = []string{filepath.Join(root, "A", "B", "C", "D", "00000.m2ts")}

	res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	if len(res.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(res.Tasks))
	}
	wantWork := filepath.Join(dir, driveLevel(dir), "A8AF6D22-D", "00000.m2ts")
	if got := res.Tasks[0].WorkingPathPrefix; got != wantWork {
		t.Errorf("WorkingPathPrefix = %q, want %q", got, wantWork)
	}
	if len(res.ReducePathMap) != 1 {
		t.Fatalf("ReducePathMap = %+v, want one entry", res.ReducePathMap)
	}
	if res.ReducePathMap[0].CRC != 0xA8AF6D22 {
		t.Errorf("CRC = %08X, want A8AF6D22", res.ReducePathMap[0].CRC)
	}
	if res.ReducePathMap[0].Prefix != `A\B\C` {
		t.Errorf("Prefix = %q, want %q", res.ReducePathMap[0].Prefix, `A\B\C`)
	}
}

// TestDeriveKeepsVolumeNumber pins the Vol branch of the reducePath rule: a
// last level carrying a volume number is kept instead of hashed, and the middle
// levels are dropped either way.
func TestDeriveKeepsVolumeNumber(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo_720p.json")
	dir := filepath.Dir(profilePath)
	root := rootDir(t)
	p.InputFiles = []string{filepath.Join(root, "Disc1", "Vol.2", "BDMV", "STREAM", "00000.m2ts")}

	res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	want := filepath.Join(dir, driveLevel(dir), "Vol.2", "00000.m2ts")
	if got := res.Tasks[0].WorkingPathPrefix; got != want {
		t.Errorf("WorkingPathPrefix = %q, want %q", got, want)
	}
	if len(res.ReducePathMap) != 0 {
		t.Errorf("ReducePathMap = %+v, want none for a volume-numbered path", res.ReducePathMap)
	}
}

// TestDeriveTooFewComponents pins the `length > 3` guard: a path with three
// components or fewer is used as it is, with no CRC and no mapping.
//
// The escaped drive level is one of the components, so the number of directory
// levels the guard tolerates depends on the platform; the test derives the
// expectation from the stripped component count instead of hardcoding it.
func TestDeriveTooFewComponents(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo_720p.json")
	dir := filepath.Dir(profilePath)
	root := rootDir(t)

	inputs := []string{
		filepath.Join(root, "00000.m2ts"),
		filepath.Join(root, "a", "00000.m2ts"),
		filepath.Join(root, "a", "b", "00000.m2ts"),
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			p.InputFiles = []string{input}
			res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
			if err != nil {
				t.Fatalf("Derive() error = %v", err)
			}
			stripped, _, err := PathSuffix(input, false)
			if err != nil {
				t.Fatalf("PathSuffix(%q) error = %v", input, err)
			}
			tooShort := len(splitComponents(stripped)) <= 3
			if !tooShort {
				t.Skipf("%q has more than three components on this platform; covered by the shortening tests", input)
			}
			want := legacyWorking(dir, input, true)
			if got := res.Tasks[0].WorkingPathPrefix; got != want {
				t.Errorf("WorkingPathPrefix = %q, want %q", got, want)
			}
			if len(res.ReducePathMap) != 0 {
				t.Errorf("ReducePathMap = %+v, want none for %q", res.ReducePathMap, stripped)
			}
		})
	}
}

// TestDeriveReducesExactlyAboveThreeComponents pins the guard as a property:
// the CRC32 shortening happens exactly when the stripped path has more than
// three components, which is what the legacy `inputSuffixComponents.Length > 3`
// meant.
func TestDeriveReducesExactlyAboveThreeComponents(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo_720p.json")
	dir := filepath.Dir(profilePath)
	root := rootDir(t)

	// A rooted input keeps the project directory out of the component count.
	inputs := []string{
		filepath.Join(root, "00000.m2ts"),
		filepath.Join(root, "a", "00000.m2ts"),
		filepath.Join(root, "a", "b", "00000.m2ts"),
		filepath.Join(root, "a", "b", "c", "00000.m2ts"),
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			p.InputFiles = []string{input}
			res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
			if err != nil {
				t.Fatalf("Derive() error = %v", err)
			}
			stripped, _, err := PathSuffix(input, false)
			if err != nil {
				t.Fatalf("PathSuffix(%q) error = %v", input, err)
			}
			wantReduced := len(splitComponents(stripped)) > 3
			if gotReduced := len(res.ReducePathMap) == 1; gotReduced != wantReduced {
				t.Errorf("reduced = %v, want %v (%q has %d components)",
					gotReduced, wantReduced, stripped, len(splitComponents(stripped)))
			}
			// The derived path always matches the legacy expression, reduced or
			// not.
			if got, want := res.Tasks[0].WorkingPathPrefix, legacyWorking(dir, input, true); got != want {
				t.Errorf("WorkingPathPrefix = %q, want %q", got, want)
			}
		})
	}
}

// TestDeriveStripsBDLevels walks the hardcoded strip list through the public
// entry point, including the case where stripping everything leaves nothing but
// the file name.
func TestDeriveStripsBDLevels(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo_720p.json")
	dir := filepath.Dir(profilePath)
	root := rootDir(t)
	p.InputFiles = []string{filepath.Join(root, "BD_VIDEO", "BDBOX", "BDROM", "BD", "BDMV", "STREAM", "00000.m2ts")}

	res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	want := filepath.Join(dir, driveLevel(dir), "00000.m2ts")
	if got := res.Tasks[0].WorkingPathPrefix; got != want {
		t.Errorf("WorkingPathPrefix = %q, want %q", got, want)
	}
}

// TestDeriveOutputPrefixUsesTheOutputDirectory pins the last derivation step:
// the escaped drive level becomes "output", which is where the deliverable and
// ReducePathMap.log live.
func TestDeriveOutputPrefixUsesTheOutputDirectory(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	dir := filepath.Dir(profilePath)

	res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	for i, task := range res.Tasks {
		want := legacyOutput(legacyWorking(dir, p.InputFiles[i], true))
		if task.OutputPathPrefix != want {
			t.Errorf("task %d OutputPathPrefix = %q, want %q", i, task.OutputPathPrefix, want)
		}
		if want != task.WorkingPathPrefix && !strings.HasPrefix(task.OutputPathPrefix, filepath.Join(dir, outputDirName)) {
			t.Errorf("task %d OutputPathPrefix = %q, want it under %q",
				i, task.OutputPathPrefix, filepath.Join(dir, outputDirName))
		}
	}
}

// TestDeriveMultipleInputsAreIndependent pins that each source gets its own
// script text and its own name, which is the point of the wizard loop.
func TestDeriveMultipleInputsAreIndependent(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "vfr.json")

	res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	if len(res.Tasks) != 3 {
		t.Fatalf("got %d tasks, want 3", len(res.Tasks))
	}
	seen := make(map[string]struct{}, len(res.Tasks))
	for _, task := range res.Tasks {
		if _, dup := seen[task.VpyFile]; dup {
			t.Errorf("two tasks share the script name %q", task.VpyFile)
		}
		seen[task.VpyFile] = struct{}{}
		if !strings.Contains(task.Script, `R"`+task.InputFile+`"`) {
			t.Errorf("script for %s does not reference its own source", task.InputFile)
		}
		// vfr.vpy has no PROJECTDIR tag, so nothing about it may change.
		if strings.Contains(task.Script, "projDir") {
			t.Errorf("script for %s grew a projDir line", task.InputFile)
		}
		// The DEBUG tag is rewritten for the whole pass. The shipped script
		// uses CRLF, so the comparison goes line by line.
		if !containsLine(task.Script, "Debug = None") {
			t.Errorf("script for %s has no rewritten DEBUG", task.InputFile)
		}
		if containsLine(task.Script, "Debug = 0") || containsLine(task.Script, "Debug = 1") {
			t.Errorf("script for %s still has the original DEBUG value", task.InputFile)
		}
	}
}

// TestDeriveRewritesProjectDirTag pins the PROJECTDIR rewrite against demo.vpy,
// which has one.
func TestDeriveRewritesProjectDirTag(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	dir := filepath.Dir(profilePath)

	res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	for _, task := range res.Tasks {
		// The old value ('.') is dropped and the project directory is spliced
		// in as an r-string.
		if !containsLine(task.Script, `projDir = R"`+dir+`"`) {
			t.Errorf("script for %s has no rewritten PROJECTDIR", task.InputFile)
		}
		if containsLine(task.Script, "projDir = '.'") {
			t.Errorf("script for %s still has the original projDir value", task.InputFile)
		}
	}
}

// TestDeriveOnlyRewritesTheFirstTag pins the .NET Regex.Split behaviour the
// legacy expression depended on: only the first tag of a kind is rewritten and
// everything from the second one on is dropped from the script.
func TestDeriveOnlyRewritesTheFirstTag(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo_720p.json")
	dir := filepath.Dir(profilePath)
	script := strings.Join([]string{
		"import vapoursynth as vs",
		"#OKE:INPUTFILE",
		`a="first.m2ts"`,
		"#OKE:INPUTFILE",
		`b="second.m2ts"`,
		"tail",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(p.InputScript)), []byte(script), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: false, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	got := res.Tasks[0].Script
	if !strings.Contains(got, `a=R"`+res.Tasks[0].InputFile+`"`) {
		t.Errorf("script does not rewrite the first tag: %q", got)
	}
	// The text after the first match's replacement is the third element, so
	// everything from the second tag on is gone.
	if strings.Contains(got, "second.m2ts") {
		t.Errorf("script kept the second tag: %q", got)
	}
}

// TestDeriveTaskName pins TaskDetail.TaskName: the project name and the source
// file name, or the file name alone when the profile has no project name.
func TestDeriveTaskName(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")

	res, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	for _, task := range res.Tasks {
		want := p.ProjectName + "-" + filepath.Base(task.InputFile)
		if task.Name != want {
			t.Errorf("Name = %q, want %q", task.Name, want)
		}
	}

	// A profile with no project name falls back to the file name.
	p.ProjectName = ""
	res, err = Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	for _, task := range res.Tasks {
		if task.Name != filepath.Base(task.InputFile) {
			t.Errorf("Name = %q, want the file name %q", task.Name, filepath.Base(task.InputFile))
		}
	}
}

// TestAssembleWritesEverything is the end-to-end check: the directories, the
// generated scripts and ReducePathMap.log all appear, and the log accumulates
// across passes.
func TestAssembleWritesEverything(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")

	res, err := Assemble(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if len(res.Tasks) != 3 {
		t.Fatalf("got %d tasks, want 3", len(res.Tasks))
	}

	for _, task := range res.Tasks {
		raw, readErr := os.ReadFile(task.VpyFile)
		if readErr != nil {
			t.Fatalf("generated script %q is missing: %v", task.VpyFile, readErr)
		}
		if string(raw) != task.Script {
			t.Errorf("script %q does not match the derived text", task.VpyFile)
		}
		if !strings.Contains(string(raw), `R"`+task.InputFile+`"`) {
			t.Errorf("script %q does not reference %q", task.VpyFile, task.InputFile)
		}
		// Both parents must exist: the pipeline writes intermediate files next
		// to the working prefix and the deliverable next to the output prefix.
		if _, statErr := os.Stat(filepath.Dir(task.WorkingPathPrefix)); statErr != nil {
			t.Errorf("working directory %q was not created: %v", filepath.Dir(task.WorkingPathPrefix), statErr)
		}
		if _, statErr := os.Stat(filepath.Dir(task.OutputPathPrefix)); statErr != nil {
			t.Errorf("output directory %q was not created: %v", filepath.Dir(task.OutputPathPrefix), statErr)
		}
	}

	// ReducePathMap.log is appended to, so a second pass adds a second copy of
	// the same line rather than replacing the file.
	first, err := os.ReadFile(res.MapFile)
	if err != nil {
		t.Fatalf("ReducePathMap.log is missing: %v", err)
	}
	if len(res.ReducePathMap) != 1 {
		t.Fatalf("ReducePathMap = %+v, want one entry", res.ReducePathMap)
	}
	wantLine := upperHex8(res.ReducePathMap[0].CRC) + " " + res.ReducePathMap[0].Prefix + "\n"
	if string(first) != wantLine {
		t.Fatalf("ReducePathMap.log = %q, want %q", first, wantLine)
	}
	if _, err := Assemble(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow}); err != nil {
		t.Fatalf("second Assemble() error = %v", err)
	}
	second, err := os.ReadFile(res.MapFile)
	if err != nil {
		t.Fatalf("ReducePathMap.log is missing after the second pass: %v", err)
	}
	if string(second) != wantLine+wantLine {
		t.Errorf("ReducePathMap.log after two passes = %q, want two copies of %q", second, wantLine)
	}
}

// TestAssembleAppendsToExistingLog pins the append to a log that already has
// content, which is the case after an earlier session.
func TestAssembleAppendsToExistingLog(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	dir := filepath.Dir(profilePath)
	outDir := filepath.Join(dir, outputDirName)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	logPath := filepath.Join(outDir, reducePathMapName)
	if err := os.WriteFile(logPath, []byte("DEADBEEF old\\prefix\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	res, err := Assemble(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	want := "DEADBEEF old\\prefix\n" +
		upperHex8(res.ReducePathMap[0].CRC) + " " + res.ReducePathMap[0].Prefix + "\n"
	if string(raw) != want {
		t.Errorf("log = %q, want %q", raw, want)
	}
}

// TestAssembleWritesNoLogWithoutReduction pins that a pass which shortened
// nothing does not create the log, which is what the legacy guard did.
func TestAssembleWritesNoLogWithoutReduction(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo_720p.json")
	p.InputFiles = []string{shallowInput(t, "00000.m2ts")}

	res, err := Assemble(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if _, err := os.Stat(res.MapFile); !os.IsNotExist(err) {
		t.Errorf("ReducePathMap.log exists (err = %v), want it absent", err)
	}
}

// TestAssembleIsIdempotentForScripts pins that a second pass overwrites the
// generated script rather than failing on it.
func TestAssembleIsIdempotentForScripts(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	opts := Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow}
	first, err := Assemble(p, opts)
	if err != nil {
		t.Fatalf("first Assemble() error = %v", err)
	}
	if err := os.WriteFile(first.Tasks[0].VpyFile, []byte("stale"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	second, err := Assemble(p, opts)
	if err != nil {
		t.Fatalf("second Assemble() error = %v", err)
	}
	raw, err := os.ReadFile(second.Tasks[0].VpyFile)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(raw) == "stale" {
		t.Error("the second pass did not overwrite the stale script")
	}
}

// TestDeriveLeavesTheFilesystemAlone pins the split between Derive and
// Assemble: deriving a task must not create anything.
func TestDeriveLeavesTheFilesystemAlone(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	dir := filepath.Dir(profilePath)

	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if _, err := Derive(p, Options{ProjectFile: profilePath, ReducePath: true, Now: fixedNow}); err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(before) != len(after) {
		t.Errorf("Derive() changed the directory: %d entries before, %d after", len(before), len(after))
	}
	for _, e := range after {
		if e.IsDir() {
			t.Errorf("Derive() created the directory %q", e.Name())
		}
	}
}

// TestDeriveNeedsAProjectFile pins the error when neither the option nor the
// profile carries a path.
func TestDeriveNeedsAProjectFile(t *testing.T) {
	t.Parallel()
	p, _ := loadExample(t, "demo.json")
	p.ConfigFilePath = ""

	if _, err := Derive(p, Options{ReducePath: true}); err == nil {
		t.Fatal("Derive() = nil error, want a rejection")
	}
}

// TestDeriveNilProfile pins the nil guard.
func TestDeriveNilProfile(t *testing.T) {
	t.Parallel()
	if _, err := Derive(nil, Options{ProjectFile: filepath.Join(t.TempDir(), "p.json")}); err == nil {
		t.Fatal("Derive(nil) = nil error, want a rejection")
	}
}

// TestDeriveMissingScript pins the error when the profile names a script that
// is not there.
func TestDeriveMissingScript(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	p.InputScript = "does-not-exist.vpy"

	if _, err := Derive(p, Options{ProjectFile: profilePath}); err == nil {
		t.Fatal("Derive() = nil error, want a missing-script rejection")
	}
}

// TestDeriveNoInputTag pins the error when the script has no INPUTFILE tag,
// which is what AddTaskService.LoadVsScript rejected before the wizard ran.
func TestDeriveNoInputTag(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	dir := filepath.Dir(profilePath)
	script := filepath.Join(dir, filepath.Base(p.InputScript))
	if err := os.WriteFile(script, []byte("import vapoursynth as vs\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := Derive(p, Options{ProjectFile: profilePath}); err == nil {
		t.Fatal("Derive() = nil error, want a missing-tag rejection")
	}
}

// TestDeriveUsesProfileConfigPath pins that the profile's own path is the
// fallback when Options.ProjectFile is empty.
func TestDeriveUsesProfileConfigPath(t *testing.T) {
	t.Parallel()
	p, profilePath := loadExample(t, "demo.json")
	dir := filepath.Dir(profilePath)

	res, err := Derive(p, Options{ReducePath: true, Now: fixedNow})
	if err != nil {
		t.Fatalf("Derive() error = %v", err)
	}
	for _, task := range res.Tasks {
		if !strings.HasPrefix(task.WorkingPathPrefix, dir) {
			t.Errorf("WorkingPathPrefix = %q, want it under %q", task.WorkingPathPrefix, dir)
		}
	}
}

// TestEpisodeConfigPath pins the per-episode config lookup the wizard did: the
// first of .json/.yaml/.yml that exists next to the source.
func TestEpisodeConfigPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "00001.m2ts")
	if err := os.WriteFile(source, []byte("not a stream"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, ok := EpisodeConfigPath(source); ok {
		t.Fatal("EpisodeConfigPath() found a config that does not exist")
	}
	// The shipped 00001.m2ts.json is the real fixture for this shape.
	raw, err := os.ReadFile(filepath.Join(exampleDir, "00001.m2ts.json"))
	if err != nil {
		t.Skipf("example episode config not available: %v", err)
	}
	jsonPath := source + ".json"
	if err := os.WriteFile(jsonPath, raw, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	got, ok := EpisodeConfigPath(source)
	if !ok || got != jsonPath {
		t.Fatalf("EpisodeConfigPath() = %q, %v, want %q, true", got, ok, jsonPath)
	}

	// .json wins over .yaml, which wins over .yml.
	for _, suffix := range []string{".yaml", ".yml"} {
		if err := os.WriteFile(source+suffix, []byte("EnableReEncode: false\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	if got, _ := EpisodeConfigPath(source); got != jsonPath {
		t.Errorf("EpisodeConfigPath() = %q, want the .json to win", got)
	}
	if err := os.Remove(jsonPath); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if got, _ := EpisodeConfigPath(source); got != source+".yaml" {
		t.Errorf("EpisodeConfigPath() = %q, want the .yaml next", got)
	}
	if err := os.Remove(source + ".yaml"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if got, _ := EpisodeConfigPath(source); got != source+".yml" {
		t.Errorf("EpisodeConfigPath() = %q, want the .yml last", got)
	}
}

// TestEpisodeConfigPathRejectsDirectory pins that a directory named like a
// config file is not mistaken for one.
func TestEpisodeConfigPathRejectsDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "00001.m2ts")
	if err := os.MkdirAll(source+".json", 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if _, ok := EpisodeConfigPath(source); ok {
		t.Error("EpisodeConfigPath() accepted a directory")
	}
	if _, ok := EpisodeConfigPath(""); ok {
		t.Error("EpisodeConfigPath(\"\") accepted an empty path")
	}
}

// containsLine reports whether s contains a line whose text equals want. The
// shipped scripts use CRLF, so a plain substring search for "\nwant\n" would
// not match them.
func containsLine(s, want string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimRight(line, "\r") == want {
			return true
		}
	}
	return false
}

// driveLevel is the escaped drive level the legacy `Replace(':', '_')` produced
// for an absolute Windows path, and which becomes the "output" directory. On a
// platform whose absolute paths have no drive it is empty, and the expectations
// above degrade to the same path the legacy code would have produced there.
func driveLevel(dir string) string {
	if len(dir) < 2 || dir[1] != ':' {
		return ""
	}
	return string(dir[0]) + "_"
}
