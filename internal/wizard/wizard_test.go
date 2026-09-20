package wizard

import (
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
)

// exampleDir holds the profiles shipped with the current release. They are the
// regression fixtures for the frozen format, and the wizard consumes them
// unchanged, so they are the fixtures here too.
const exampleDir = "../../dist/windows/examples"

// TestMain raises the log level: assembling a profile logs one line per task,
// and the wizard has no per-test assertion on that output.
func TestMain(m *testing.M) {
	log.SetLevel("ERROR")
	os.Exit(m.Run())
}

// exampleProfile copies one shipped example into a temporary directory and
// returns the copy's path plus its directory. The wizard writes next to the
// profile, so the shipped tree must never be touched.
func exampleProfile(t *testing.T, name string) (profilePath, dir string) {
	t.Helper()
	src := filepath.Join(exampleDir, name)
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("example profile not available: %v", err)
	}
	dir = t.TempDir()
	profilePath = filepath.Join(dir, name)
	if err := os.WriteFile(profilePath, raw, 0o600); err != nil {
		t.Fatalf("write profile copy: %v", err)
	}
	return profilePath, dir
}

// copyExampleVpy copies the script a profile names, so a test can assemble a
// task whose script is the real one.
func copyExampleVpy(t *testing.T, dir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(exampleDir, name))
	if err != nil {
		t.Skipf("example script not available: %v", err)
	}
	target := filepath.Join(dir, name)
	if err := os.WriteFile(target, raw, 0o600); err != nil {
		t.Fatalf("write script copy: %v", err)
	}
	return target
}

// fixedNow is the timestamp every test pins, so a generated name is exact.
var fixedNow = time.Date(2026, time.September, 20, 9, 5, 0, 0, time.Local)

func TestPathSuffix(t *testing.T) {
	t.Parallel()

	// Every expectation below was produced by running the legacy expression
	// (WizardWindow.xaml.cs:272-307) in .NET, with a project directory of
	// C:\proj. The "want" strings are the inputSuffixPath it computed.
	cases := []struct {
		name   string
		input  string
		reduce bool
		want   string
		crc    uint32
		prefix string
	}{
		{
			name:   "typical BD tree",
			input:  `D:\Main_Disc\BDMV\STREAM\00000.m2ts`,
			reduce: true,
			want:   `D_\Main_Disc\00000.m2ts`,
		},
		{
			name:   "typical BD tree without reducePath",
			input:  `D:\Main_Disc\BDMV\STREAM\00000.m2ts`,
			reduce: false,
			want:   `D_\Main_Disc\00000.m2ts`,
		},
		{
			name:   "every stripped level",
			input:  `D:\BD_VIDEO\BDBOX\BDROM\BD\BDMV\STREAM\00000.m2ts`,
			reduce: true,
			want:   `D_\00000.m2ts`,
		},
		{
			name:   "last level has a volume number",
			input:  `D:\Disc1\Vol.1\BDMV\STREAM\00000.m2ts`,
			reduce: true,
			// The middle levels are dropped: the legacy code rebuilt the path
			// from [drive, effectivePath, file] in both branches.
			want: `D_\Vol.1\00000.m2ts`,
		},
		{
			name:   "volume number with a space",
			input:  `D:\Disc1\Vol 2\BDMV\STREAM\00000.m2ts`,
			reduce: true,
			want:   `D_\Vol 2\00000.m2ts`,
		},
		{
			name:   "volume number with a dash",
			input:  `D:\Disc1\Vol-3\BDMV\STREAM\00000.m2ts`,
			reduce: true,
			want:   `D_\Vol-3\00000.m2ts`,
		},
		{
			name:   "the word Volume is not a volume number",
			input:  `D:\Disc1\Volume\BDMV\STREAM\00000.m2ts`,
			reduce: true,
			want:   `D_\64C65F4A-Volume\00000.m2ts`,
			crc:    0x64C65F4A,
			prefix: `Disc1`,
		},
		{
			name:   "two levels above the file",
			input:  `D:\a\b\00000.m2ts`,
			reduce: true,
			want:   `D_\E8B7BE43-b\00000.m2ts`,
			crc:    0xE8B7BE43,
			prefix: `a`,
		},
		{
			name:   "three levels above the file",
			input:  `D:\a\b\c\00000.m2ts`,
			reduce: true,
			want:   `D_\03E66A29-c\00000.m2ts`,
			crc:    0x03E66A29,
			prefix: `a\b`,
		},
		{
			name:   "no directory at all",
			input:  `00000.m2ts`,
			reduce: true,
			want:   `00000.m2ts`,
		},
		{
			name:   "drive root",
			input:  `D:\00000.m2ts`,
			reduce: true,
			want:   `D_\00000.m2ts`,
		},
		{
			name:   "exactly three components is not reduced",
			input:  `D:\a\00000.m2ts`,
			reduce: true,
			want:   `D_\a\00000.m2ts`,
		},
		{
			name:   "four levels above the file",
			input:  `D:\A\B\C\D\00000.m2ts`,
			reduce: true,
			want:   `D_\A8AF6D22-D\00000.m2ts`,
			crc:    0xA8AF6D22,
			prefix: `A\B\C`,
		},
		{
			name:   "lowercase drive letter is preserved",
			input:  `d:\Main_Disc\BDMV\STREAM\00001.m2ts`,
			reduce: true,
			want:   `d_\Main_Disc\00001.m2ts`,
		},
		{
			name:   "forward slashes are accepted",
			input:  `D:/Main_Disc/BDMV/STREAM/00000.m2ts`,
			reduce: true,
			want:   `D_\Main_Disc\00000.m2ts`,
		},
		{
			name:   "a space in a level survives",
			input:  `D:\Main Disc\BDMV\STREAM\00000.m2ts`,
			reduce: true,
			want:   `D_\Main Disc\00000.m2ts`,
		},
		{
			name:   "double separators collapse",
			input:  `D:\Main_Disc\\BDMV\STREAM\\00000.m2ts`,
			reduce: true,
			want:   `D_\Main_Disc\00000.m2ts`,
		},
		{
			name:   "component strip does not overlap matches",
			input:  `D:\a\BD\BD\b\c\00000.m2ts`,
			reduce: true,
			// One "BD" survives: the .NET engine resumed after the separator it
			// consumed, exactly as stripComponent does.
			want:   `D_\7CB5F810-c\00000.m2ts`,
			crc:    0x7CB5F810,
			prefix: `a\BD\b`,
		},
		{
			name:   "three equal levels lose two",
			input:  `D:\a\BD\BD\BD\b\c\00000.m2ts`,
			reduce: true,
			want:   `D_\7CB5F810-c\00000.m2ts`,
			crc:    0x7CB5F810,
			prefix: `a\BD\b`,
		},
		{
			name:   "repeated BDMV STREAM pair",
			input:  `D:\a\BDMV\STREAM\BDMV\STREAM\00000.m2ts`,
			reduce: true,
			want:   `D_\E8B7BE43-STREAM\00000.m2ts`,
			crc:    0xE8B7BE43,
			prefix: `a`,
		},
		{
			name:   "two BDMV levels lose one",
			input:  `D:\a\BDMV\BDMV\STREAM\00000.m2ts`,
			reduce: true,
			want:   `D_\E8B7BE43-BDMV\00000.m2ts`,
			crc:    0xE8B7BE43,
			prefix: `a`,
		},
		{
			name:   "two STREAM levels lose one",
			input:  `D:\a\STREAM\STREAM\b\c\00000.m2ts`,
			reduce: true,
			want:   `D_\7AF7C1F9-c\00000.m2ts`,
			crc:    0x7AF7C1F9,
			prefix: `a\STREAM\b`,
		},
		{
			name:   "strip is case sensitive",
			input:  `D:\a\bdmv\STREAM\b\c\00000.m2ts`,
			reduce: true,
			// "bdmv" is kept because the legacy pattern had no IgnoreCase.
			want:   `D_\981AAA43-c\00000.m2ts`,
			crc:    0x981AAA43,
			prefix: `a\bdmv\b`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, mapping, err := PathSuffix(tc.input, tc.reduce)
			if err != nil {
				t.Fatalf("PathSuffix(%q) error = %v", tc.input, err)
			}
			if got != tc.want {
				t.Errorf("PathSuffix(%q) = %q, want %q", tc.input, got, tc.want)
			}
			if tc.crc == 0 {
				if mapping.Prefix != "" {
					t.Errorf("PathSuffix(%q) recorded %+v, want no mapping", tc.input, mapping)
				}
				return
			}
			if mapping.CRC != tc.crc {
				t.Errorf("CRC = %08X, want %08X", mapping.CRC, tc.crc)
			}
			if mapping.Prefix != tc.prefix {
				t.Errorf("Prefix = %q, want %q", mapping.Prefix, tc.prefix)
			}
		})
	}
}

// TestPathSuffixCRCIsStandardIEEE pins the CRC32 the legacy code used: the
// standard IEEE polynomial, seeded with 0xFFFFFFFF and finalised with the same
// XOR, which is exactly hash/crc32.ChecksumIEEE. The two prefixes are
// independent of the derived path so the assertion states the algorithm, not a
// value copied out of the implementation under test.
func TestPathSuffixCRCIsStandardIEEE(t *testing.T) {
	t.Parallel()
	cases := []struct {
		prefix string
		input  string
	}{
		{prefix: `123456789\x`, input: `D:\123456789\x\y\f.m2ts`},
		{prefix: `a`, input: `D:\a\b\f.m2ts`},
		{prefix: `a\x`, input: `D:\a\x\y\f.m2ts`},
	}
	for _, tc := range cases {
		_, mapping, err := PathSuffix(tc.input, true)
		if err != nil {
			t.Fatalf("PathSuffix(%q) error = %v", tc.input, err)
		}
		if mapping.Prefix != tc.prefix {
			t.Fatalf("PathSuffix(%q).Prefix = %q, want %q", tc.input, mapping.Prefix, tc.prefix)
		}
		if want := crc32.ChecksumIEEE([]byte(tc.prefix)); mapping.CRC != want {
			t.Errorf("PathSuffix(%q).CRC = %08X, want %08X", tc.input, mapping.CRC, want)
		}
	}
}

// TestPathSuffixCRCKnownValues pins the exact digits the legacy ReducePathMap.log
// contained, so a refactor cannot quietly change which directory a finished
// release was written to. The values came from running the legacy expression in
// .NET; see the report for the probe.
func TestPathSuffixCRCKnownValues(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input string
		want  uint32
	}{
		{`D:\a\b\00000.m2ts`, 0xE8B7BE43},                  // prefix "a"
		{`D:\a\b\c\00000.m2ts`, 0x03E66A29},                // prefix "a\b"
		{`D:\Disc1\Volume\BDMV\STREAM\x.m2ts`, 0x64C65F4A}, // prefix "Disc1"
		{`D:\A\B\C\D\00000.m2ts`, 0xA8AF6D22},              // prefix "A\B\C"
	}
	for _, tc := range cases {
		_, mapping, err := PathSuffix(tc.input, true)
		if err != nil {
			t.Fatalf("PathSuffix(%q) error = %v", tc.input, err)
		}
		if mapping.CRC != tc.want {
			t.Errorf("PathSuffix(%q).CRC = %08X, want %08X", tc.input, mapping.CRC, tc.want)
		}
	}
}

func TestPathSuffixRejectsEmptyInput(t *testing.T) {
	t.Parallel()
	for _, input := range []string{"", "   ", "\t"} {
		if _, _, err := PathSuffix(input, true); err == nil {
			t.Errorf("PathSuffix(%q) = nil error, want a rejection", input)
		}
	}
}

func TestOutputPath(t *testing.T) {
	t.Parallel()
	// The expectations are the .NET Regex.Replace output for the same inputs.
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "escaped drive becomes the output directory",
			in:   `C:\proj\D_\Main_Disc\00000.m2ts`,
			want: `C:\proj\output\Main_Disc\00000.m2ts`,
		},
		{
			name: "a ._ level becomes the output directory",
			in:   `C:\proj\._\Main_Disc\00000.m2ts`,
			want: `C:\proj\output\Main_Disc\00000.m2ts`,
		},
		{
			name: "any single-character underscore level matches",
			in:   `C:\proj\D_\a_\b\00000.m2ts`,
			want: `C:\proj\output\a_\b\00000.m2ts`,
		},
		{
			name: "two ._ levels: the second is skipped by the scan",
			in:   `C:\proj\D_\work\._\._\00000.m2ts`,
			// After matching `\D_\` and `\._\`, the scan resumes on "._\..." and
			// the leading separator the pattern needs is gone.
			want: `C:\proj\output\work\output\._\00000.m2ts`,
		},
		{
			name: "three ._ levels lose two",
			in:   `C:\proj\D_\work\._\._\._\00000.m2ts`,
			want: `C:\proj\output\work\output\._\output\00000.m2ts`,
		},
		{
			name: "a bare underscore is not a match",
			in:   `C:\proj\a\_\b\00000.m2ts`,
			want: `C:\proj\a\_\b\00000.m2ts`,
		},
		{
			name: "no match leaves the path alone",
			in:   `C:\proj\Main_Disc\00000.m2ts`,
			want: `C:\proj\Main_Disc\00000.m2ts`,
		},
		{
			name: "the first level is not a match",
			in:   `D_\Main_Disc\00000.m2ts`,
			want: `D_\Main_Disc\00000.m2ts`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := outputPath(normalizeSeparators(tc.in))
			if got != normalizeSeparators(tc.want) {
				t.Errorf("outputPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestStripComponent(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		comp string
		want string
	}{
		{"simple", `D_\a\BDMV\b\c`, "BDMV", `D_\a\b\c`},
		{"every occurrence in one pass", `D_\BD\BD\BD\x`, "BD", `D_\BD\x`},
		{"one of two consecutive", `D_\a\BD\BD\b`, "BD", `D_\a\BD\b`},
		{"no separator before a drive", `D_\BD\BD\x`, "BD", `D_\BD\x`},
		{"case sensitive", `D_\a\bdmv\b`, "BDMV", `D_\a\bdmv\b`},
		{"missing level", `D_\a\b`, "STREAM", `D_\a\b`},
		{"level name is a prefix of another", `D_\a\BDROM\b`, "BD", `D_\a\BDROM\b`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := stripComponent(normalizeSeparators(tc.in), tc.comp)
			if got != normalizeSeparators(tc.want) {
				t.Errorf("stripComponent(%q, %q) = %q, want %q", tc.in, tc.comp, got, tc.want)
			}
		})
	}
}

func TestSplitComponents(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{`D_\a\b\f.m2ts`, []string{"D_", "a", "b", "f.m2ts"}},
		{`D_/a/b/f.m2ts`, []string{"D_", "a", "b", "f.m2ts"}},
		{`D_\\a\\b\\f.m2ts`, []string{"D_", "a", "b", "f.m2ts"}},
		{`f.m2ts`, []string{"f.m2ts"}},
		{``, nil},
	}
	for _, tc := range cases {
		got := splitComponents(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("splitComponents(%q) = %v, want %v", tc.in, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitComponents(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

func TestUpperHex8(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   uint32
		want string
	}{
		{0, "00000000"},
		{1, "00000001"},
		{0xCBF43926, "CBF43926"},
		{0xFFFFFFFF, "FFFFFFFF"},
		{0x64C65F4A, "64C65F4A"},
	}
	for _, tc := range cases {
		if got := upperHex8(tc.in); got != tc.want {
			t.Errorf("upperHex8(%#x) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripDriveColon(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{`D:\a\b`, `D_\a\b`},
		{`d:\a\b`, `d_\a\b`},
		{`D:/a/b`, `D_/a/b`},
		{`/mnt/a/b`, `/mnt/a/b`},
		{`a:b`, `a_b`},
		{`a\b:c`, `a\b:c`},
		{`\\server\share\x`, `\\server\share\x`},
		{``, ``},
		{`D:`, `D_`},
		{`:`, `:`},
	}
	for _, tc := range cases {
		if got := stripDriveColon(normalizeSeparators(tc.in)); got != normalizeSeparators(tc.want) {
			t.Errorf("stripDriveColon(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestIsRooted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{`C:\a`, true},
		{`C:/a`, true},
		{`c:a`, true},
		{`a\b`, false},
		{`a:/b`, true},
		{``, false},
	}
	for _, tc := range cases {
		if got := isRooted(tc.in); got != tc.want {
			t.Errorf("isRooted(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCombineLikeDotNet(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		dir  string
		sfx  string
		want string
	}{
		{"relative suffix joins", `C:\proj`, `D_\a\f.m2ts`, filepath.Join(`C:\proj`, `D_\a\f.m2ts`)},
		{"rooted suffix wins", `C:\proj`, `\\server\share\f.m2ts`, `\\server\share\f.m2ts`},
		{"drive suffix wins", `C:\proj`, `D_\a\f.m2ts`, filepath.Join(`C:\proj`, `D_\a\f.m2ts`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := combineLikeDotNet(normalizeSeparators(tc.dir), normalizeSeparators(tc.sfx)); got != normalizeSeparators(tc.want) {
				t.Errorf("combineLikeDotNet(%q, %q) = %q, want %q", tc.dir, tc.sfx, got, tc.want)
			}
		})
	}
}

func TestVpyName(t *testing.T) {
	t.Parallel()
	// The legacy `newPath + "-" + time.ToString("MMddHHmm") + ".vpy"` keeps the
	// source extension, which is why the generated file is not "00000.vpy".
	got := vpyName(filepath.Join(`C:\proj`, `D_\Main_Disc`, "00000.m2ts"), "09200905")
	want := filepath.Join(`C:\proj`, `D_\Main_Disc`, "00000.m2ts-09200905.vpy")
	if got != want {
		t.Errorf("vpyName() = %q, want %q", got, want)
	}
}

func TestMakeDirRejectsEmpty(t *testing.T) {
	t.Parallel()
	if err := makeDir(""); err == nil {
		t.Fatal("makeDir(\"\") = nil, want an error")
	}
}

func TestTimestamp(t *testing.T) {
	t.Parallel()
	// The legacy `ToString("MMddHHmm")` has no separators and pads each field to
	// two digits, so the minute of a single-digit hour still has four digits.
	cases := []struct {
		when time.Time
		want string
	}{
		{time.Date(2026, time.September, 20, 9, 5, 0, 0, time.UTC), "09200905"},
		{time.Date(2026, time.January, 2, 0, 0, 0, 0, time.UTC), "01020000"},
		{time.Date(2026, time.December, 31, 23, 59, 0, 0, time.UTC), "12312359"},
	}
	for _, tc := range cases {
		if got := timestamp(tc.when); got != tc.want {
			t.Errorf("timestamp(%s) = %q, want %q", tc.when, got, tc.want)
		}
	}
	// A Go layout would have emitted the letters literally, which is the reason
	// timestamp exists.
	if fixedNow.Format("MMddHHmm") != "MMddHHmm" {
		t.Errorf("the Go layout no longer reproduces the .NET format string, so timestamp may be unnecessary")
	}
}

func TestNormalizeSeparatorsRoundTrips(t *testing.T) {
	t.Parallel()
	sep := string(filepath.Separator)
	for _, in := range []string{`a/b`, `a\b`, `a/b\c`, `/a`, `\a`} {
		got := normalizeSeparators(in)
		if strings.ContainsAny(got, "/\\") && strings.Count(got, sep) == 0 {
			t.Errorf("normalizeSeparators(%q) = %q, want %q separators", in, got, sep)
		}
	}
}
