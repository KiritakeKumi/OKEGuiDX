package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// writeFile creates an empty file, failing the test when the tree is wrong.
func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// exists reports whether path is still on disk.
func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// names converts a list of absolute paths into sorted base names, which is
// what the assertions care about.
func names(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	slices.Sort(out)
	return out
}

func TestCleanerDefaultsMatchLegacy(t *testing.T) {
	t.Parallel()
	c := NewCleaner()
	if got := c.RemoveSuffixes; !slices.Equal(got, DefaultRemoveSuffixes) {
		t.Errorf("RemoveSuffixes = %v, want %v", got, DefaultRemoveSuffixes)
	}
	if got := c.RenameSuffixes; !slices.Equal(got, DefaultRenameSuffixes) {
		t.Errorf("RenameSuffixes = %v, want %v", got, DefaultRenameSuffixes)
	}
	// The default lists are the ones the C# constructor passes.
	wantRemove := []string{
		"flac", "alac", "aac", "ac3", "dts", "eac3", "sup",
		"Log.txt", "eac3to.log", "vpy", "qpf", "lwi", "rpc", "pyc",
	}
	if !slices.Equal(DefaultRemoveSuffixes, wantRemove) {
		t.Errorf("DefaultRemoveSuffixes = %v, want %v", DefaultRemoveSuffixes, wantRemove)
	}
	wantRename := []string{"hevc", "mkv", "mp4", "mka", "h264"}
	if !slices.Equal(DefaultRenameSuffixes, wantRename) {
		t.Errorf("DefaultRenameSuffixes = %v, want %v", DefaultRenameSuffixes, wantRename)
	}
}

// TestCleanerSuffixMatching covers the two traps in the legacy suffix test:
// string.EndsWith is ordinal, so the case has to match, and it is a plain
// suffix test, so a file whose *stem* ends in a suffix matches too.
func TestCleanerSuffixMatching(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		file    string
		removed bool
	}{
		{"plain suffix", "ep01.flac", true},
		{"another suffix", "ep01.eac3to.log", true},
		{"suffix with a dot inside", "ep01.Log.txt", true},
		{"uppercase suffix is not matched", "ep01.FLAC", false},
		{"mixed case suffix is not matched", "ep01.Flac", false},
		{"suffix in the stem still matches", "ep01_flac", true},
		{"suffix as a trailing dot segment", "ep01.pyc", true},
		{"unrelated suffix", "ep01.txt", false},
		{"finished output is kept", "ep01.mkv", false},
		{"vpy script is removed", "ep01.vpy", true},
		{"qpf is removed", "ep01.qpf", true},
		{"lwi is removed", "ep01.lwi", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			input := filepath.Join(dir, "ep01.m2ts")
			writeFile(t, input)
			victim := filepath.Join(dir, tt.file)
			writeFile(t, victim)

			removed, err := NewCleaner().Clean(input, nil)
			if err != nil {
				t.Fatalf("Clean() error = %v", err)
			}
			if got := exists(victim); got == tt.removed {
				t.Errorf("file %q present = %v, want removed = %v", tt.file, got, tt.removed)
			}
			if tt.removed && !slices.Contains(names(removed), tt.file) {
				t.Errorf("Clean() returned %v, want %q among them", names(removed), tt.file)
			}
		})
	}
}

// TestCleanerNeverRemovesInput is the white-list contract: Clean adds the input
// file itself before removing anything.
func TestCleanerNeverRemovesInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// The input is itself named with a removable suffix, which is exactly the
	// case the white list exists for.
	input := filepath.Join(dir, "ep01.flac")
	writeFile(t, input)

	removed, err := NewCleaner().Clean(input, nil)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if !exists(input) {
		t.Fatal("Clean() deleted its own input file")
	}
	if len(removed) != 0 {
		t.Errorf("Clean() removed %v, want nothing", names(removed))
	}
}

// TestCleanerHonoursWhiteList checks that the caller's list protects extra
// files, which is how the wizard keeps the generated .vpy and the .lwi index.
func TestCleanerHonoursWhiteList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	script := filepath.Join(dir, "ep01.vpy")
	writeFile(t, script)
	index := filepath.Join(dir, "ep01.lwi")
	writeFile(t, index)
	scratch := filepath.Join(dir, "ep01.flac")
	writeFile(t, scratch)

	removed, err := NewCleaner().Clean(input, []string{script, index})
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if !exists(script) || !exists(index) {
		t.Error("Clean() deleted a white-listed file")
	}
	if exists(scratch) {
		t.Error("Clean() kept a non white-listed intermediate")
	}
	if want := []string{"ep01.flac"}; !slices.Equal(names(removed), want) {
		t.Errorf("Clean() removed %v, want %v", names(removed), want)
	}
}

// TestCleanerDoesNotMutateWhiteList pins down the one deliberate divergence
// from the legacy code: Cleaner.Clean appended to the caller's List<string>.
func TestCleanerDoesNotMutateWhiteList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)

	list := []string{"keep-me"}
	if _, err := NewCleaner().Clean(input, list); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if want := []string{"keep-me"}; !slices.Equal(list, want) {
		t.Errorf("white list became %v, want %v", list, want)
	}
}

// TestCleanerSearchesRecursively documents the AllDirectories hazard: a file
// below the input's directory is removed as well, including one in an output
// directory that happens to live under the working directory.
func TestCleanerSearchesRecursively(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	top := filepath.Join(dir, "ep01.flac")
	writeFile(t, top)
	nested := filepath.Join(dir, "output", "ep01.aac")
	writeFile(t, nested)
	deeper := filepath.Join(dir, "output", "old", "ep01.sup")
	writeFile(t, deeper)
	// A sibling that does not share the stem must survive.
	sibling := filepath.Join(dir, "output", "ep02.flac")
	writeFile(t, sibling)

	removed, err := NewCleaner().Clean(input, nil)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	for _, gone := range []string{top, nested, deeper} {
		if exists(gone) {
			t.Errorf("Clean() kept %s, want it removed", gone)
		}
	}
	if !exists(sibling) {
		t.Error("Clean() removed a file with a different stem")
	}
	want := []string{"ep01.aac", "ep01.flac", "ep01.sup"}
	if got := names(removed); !slices.Equal(got, want) {
		t.Errorf("Clean() removed %v, want %v", got, want)
	}
}

// TestCleanerTopDirectoryOnlyForRename is the counterpart: Rename never looks
// below the input's directory.
func TestCleanerTopDirectoryOnlyForRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	top := filepath.Join(dir, "ep01.mkv")
	writeFile(t, top)
	nested := filepath.Join(dir, "old", "ep01.mkv")
	writeFile(t, nested)

	now := time.Date(2026, time.September, 19, 21, 32, 0, 0, time.Local)
	c := NewCleaner()
	renamed, err := c.renameAt(input, nil, now)
	if err != nil {
		t.Fatalf("renameAt() error = %v", err)
	}
	if !exists(nested) {
		t.Error("Rename() touched a file in a subdirectory")
	}
	if exists(top) {
		t.Error("Rename() left the original in place")
	}
	want := filepath.Join(dir, "ep01_b_192132.mkv")
	if !slices.Equal(renamed, []string{want}) {
		t.Errorf("renameAt() = %v, want [%s]", renamed, want)
	}
	if !exists(want) {
		t.Errorf("Rename() did not create %s", want)
	}
}

func TestCleanerRenameUsesLegacyTimestampFormat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		when time.Time
		want string
	}{
		{"day and time", time.Date(2026, time.September, 5, 9, 7, 0, 0, time.Local), "ep01_b_050907.mkv"},
		{"midnight is 00", time.Date(2026, time.January, 1, 0, 0, 0, 0, time.Local), "ep01_b_010000.mkv"},
		{"single digit day is zero padded", time.Date(2026, time.December, 9, 23, 59, 0, 0, time.Local), "ep01_b_092359.mkv"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			input := filepath.Join(dir, "ep01.m2ts")
			writeFile(t, input)
			writeFile(t, filepath.Join(dir, "ep01.mkv"))

			renamed, err := NewCleaner().renameAt(input, nil, tt.when)
			if err != nil {
				t.Fatalf("renameAt() error = %v", err)
			}
			if want := []string{filepath.Join(dir, tt.want)}; !slices.Equal(renamed, want) {
				t.Errorf("renameAt() = %v, want %v", renamed, want)
			}
		})
	}
}

// TestCleanerRenameKeepsTheMatchedStem mirrors the legacy loop, which
// recomputes the stem from the matched file rather than from the input.
func TestCleanerRenameKeepsTheMatchedStem(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	writeFile(t, filepath.Join(dir, "ep01.v2.mkv"))

	now := time.Date(2026, time.September, 19, 21, 32, 0, 0, time.Local)
	renamed, err := NewCleaner().renameAt(input, nil, now)
	if err != nil {
		t.Fatalf("renameAt() error = %v", err)
	}
	want := filepath.Join(dir, "ep01.v2_b_192132.mkv")
	if !slices.Equal(renamed, []string{want}) {
		t.Errorf("renameAt() = %v, want [%s]", renamed, want)
	}
}

// TestCleanerRenameDeletesWhenTheTargetExists reproduces the legacy failure
// path: .NET's File.Move refuses an existing destination, logs, and deletes
// the source.
//
// The match list is computed once, before any rename, exactly as the legacy
// loop does. An existing `ep01_b_<stamp>.mkv` therefore appears in that list
// in its own right (it matches `ep01*.*` and ends in a rename suffix), and the
// loop then backs *it* up on a later iteration. The net effect is recorded
// here so it is not mistaken for a port bug.
func TestCleanerRenameDeletesWhenTheTargetExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	victim := filepath.Join(dir, "ep01.mkv")
	writeFile(t, victim)

	now := time.Date(2026, time.September, 19, 21, 32, 0, 0, time.Local)
	// Occupy the destination the first iteration would use.
	blocker := filepath.Join(dir, "ep01_b_192132.mkv")
	writeFile(t, blocker)

	renamed, err := NewCleaner().renameAt(input, nil, now)
	if err != nil {
		t.Fatalf("renameAt() error = %v", err)
	}
	if exists(victim) {
		t.Error("renameAt() left the file it could not back up")
	}
	// The blocker is a match too, so it is backed up in turn.
	second := filepath.Join(dir, "ep01_b_192132_b_192132.mkv")
	if !slices.Equal(renamed, []string{second}) {
		t.Errorf("renameAt() = %v, want [%s]", renamed, second)
	}
	if !exists(second) {
		t.Errorf("renameAt() did not back up the pre-existing backup")
	}
}

// TestCleanerRenameWhiteListsTheExistingBackup shows the same collision with
// the pre-existing backup protected by the white list: the blocker is excluded
// from the match list, so the first iteration finds its destination occupied,
// deletes the source, and the backup survives untouched.
func TestCleanerRenameWhiteListsTheExistingBackup(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	victim := filepath.Join(dir, "ep01.mkv")
	writeFile(t, victim)

	now := time.Date(2026, time.September, 19, 21, 32, 0, 0, time.Local)
	blocker := filepath.Join(dir, "ep01_b_192132.mkv")
	writeFile(t, blocker)

	renamed, err := NewCleaner().renameAt(input, []string{blocker}, now)
	if err != nil {
		t.Fatalf("renameAt() error = %v", err)
	}
	if len(renamed) != 0 {
		t.Errorf("renameAt() = %v, want no renamed file", renamed)
	}
	if !exists(blocker) {
		t.Error("renameAt() touched the white-listed backup")
	}
	if exists(victim) {
		t.Error("renameAt() kept the file it could not back up")
	}
}

func TestCleanerRenameHonoursWhiteList(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	keep := filepath.Join(dir, "ep01.mkv")
	writeFile(t, keep)
	writeFile(t, filepath.Join(dir, "ep01.mp4"))

	now := time.Date(2026, time.September, 19, 21, 32, 0, 0, time.Local)
	renamed, err := NewCleaner().renameAt(input, []string{keep}, now)
	if err != nil {
		t.Fatalf("renameAt() error = %v", err)
	}
	if !exists(keep) {
		t.Error("renameAt() touched a white-listed file")
	}
	want := []string{filepath.Join(dir, "ep01_b_192132.mp4")}
	if !slices.Equal(renamed, want) {
		t.Errorf("renameAt() = %v, want %v", renamed, want)
	}
}

func TestCleanerMissingDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope", "ep01.m2ts")

	_, err := NewCleaner().Clean(missing, nil)
	if err == nil {
		t.Fatal("Clean() = nil error, want a not-found error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindNotFound {
		t.Errorf("Kind = %q, want %q", got, okerr.KindNotFound)
	}
}

func TestCleanerMissingDirectoryRename(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope", "ep01.m2ts")

	if _, err := NewCleaner().Rename(missing, nil); err == nil {
		t.Fatal("Rename() = nil error, want a not-found error")
	}
}

// TestCleanerNoMatchesIsNotAnError: a directory holding only the input is the
// common case on a first run.
func TestCleanerNoMatchesIsNotAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	writeFile(t, filepath.Join(dir, "ep01.mkv"))

	removed, err := NewCleaner().Clean(input, nil)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if len(removed) != 0 {
		t.Errorf("Clean() removed %v, want nothing", names(removed))
	}
	if !exists(input) {
		t.Error("Clean() removed the input")
	}
}

// TestCleanerCustomSuffixes covers NewCleanerWith, which exists so the
// behaviour is testable without the global defaults.
func TestCleanerCustomSuffixes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	writeFile(t, filepath.Join(dir, "ep01.log"))
	writeFile(t, filepath.Join(dir, "ep01.flac"))

	c := NewCleanerWith([]string{"log"}, nil)
	removed, err := c.Clean(input, nil)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if want := []string{"ep01.log"}; !slices.Equal(names(removed), want) {
		t.Errorf("Clean() removed %v, want %v", names(removed), want)
	}
	if !exists(filepath.Join(dir, "ep01.flac")) {
		t.Error("Clean() used the default suffix list instead of the custom one")
	}
}

// TestCleanerSubstringTrap covers the case the prompt calls out: `ep01*.*`
// finds `ep01x.txt` and `ep01jpn.txt` because the pattern is not anchored to a
// separator, while `ep02` is untouched.
func TestCleanerSubstringTrap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	for _, n := range []string{"ep01x.txt", "ep01jpn.flac", "ep01_.pyc", "ep02.flac", "ep1.flac"} {
		writeFile(t, filepath.Join(dir, n))
	}

	removed, err := NewCleaner().Clean(input, nil)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	want := []string{"ep01_.pyc", "ep01jpn.flac"} // ep01x.txt is not a remove suffix
	if got := names(removed); !slices.Equal(got, want) {
		t.Errorf("Clean() removed %v, want %v", got, want)
	}
	for _, keep := range []string{"ep02.flac", "ep1.flac", "ep01x.txt"} {
		if !exists(filepath.Join(dir, keep)) {
			t.Errorf("Clean() removed %s, want it kept", keep)
		}
	}
}

// TestCleanerLogFileSuffixes checks the two multi-dot suffixes, which is where
// a naive extension comparison would diverge.
//
// `ep01.log.txt` ends with neither "Log.txt" nor "eac3to.log", so the suffix
// test leaves it alone. On Windows a file named `ep01.log.txt` is not even
// distinct from `ep01.Log.txt` (the filesystem is case-insensitive), which is
// why the test uses `ep01.other.txt` as the kept counterpart.
func TestCleanerLogFileSuffixes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	for _, n := range []string{"ep01.Log.txt", "ep01.eac3to.log", "ep01.log", "ep01.other.txt"} {
		writeFile(t, filepath.Join(dir, n))
	}

	removed, err := NewCleaner().Clean(input, nil)
	if err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	want := []string{"ep01.Log.txt", "ep01.eac3to.log"}
	if got := names(removed); !slices.Equal(got, want) {
		t.Errorf("Clean() removed %v, want %v", got, want)
	}
	for _, keep := range []string{"ep01.log", "ep01.other.txt"} {
		if !exists(filepath.Join(dir, keep)) {
			t.Errorf("Clean() removed %s, want it kept", keep)
		}
	}
}

// TestCleanerDirectoriesAreNotRemoved guards the WalkDir callback: a directory
// whose name matches must not be handed to os.Remove.
func TestCleanerDirectoriesAreNotRemoved(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	sub := filepath.Join(dir, "ep01.flac")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(sub, "inner.txt"))

	if _, err := NewCleaner().Clean(input, nil); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if !exists(sub) {
		t.Error("Clean() removed a directory")
	}
	if !exists(filepath.Join(sub, "inner.txt")) {
		t.Error("Clean() removed a file that does not match")
	}
}

func TestTrimExt(t *testing.T) {
	t.Parallel()
	// The expectations are the ones .NET's Path.GetFileNameWithoutExtension
	// produces, which is what the legacy code calls.
	tests := []struct {
		in, want string
	}{
		{"ep01.m2ts", "ep01"},
		{`dir/ep01.m2ts`, "ep01"},
		{"ep01", "ep01"},
		{"ep01.tar.gz", "ep01.tar"},
		// A leading dot is an extension to .NET, so the stem is empty.
		{".hidden", ""},
		{".hidden.txt", ".hidden"},
		{"ep01.", "ep01"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := trimExt(tt.in); got != tt.want {
				t.Errorf("trimExt(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHasAnySuffix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		suffixes []string
		want     bool
	}{
		{"ep01.flac", []string{"flac"}, true},
		{"ep01.FLAC", []string{"flac"}, false},
		{"ep01.flac", []string{"FLAC"}, false},
		{"ep01_flac", []string{"flac"}, true},
		{"ep01.Log.txt", []string{"Log.txt"}, true},
		{"ep01.log.txt", []string{"Log.txt"}, false},
		{"ep01.txt", []string{"flac", "eac3to.log"}, false},
		{"ep01.eac3to.log", []string{"flac", "eac3to.log"}, true},
		{"ep01.flac", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/"+strings.Join(tt.suffixes, ","), func(t *testing.T) {
			t.Parallel()
			if got := hasAnySuffix(tt.name, tt.suffixes); got != tt.want {
				t.Errorf("hasAnySuffix(%q, %v) = %v, want %v", tt.name, tt.suffixes, got, tt.want)
			}
		})
	}
}
