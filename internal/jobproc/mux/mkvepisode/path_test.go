package mkvepisode

import "testing"

// The expected values below were produced by running the same inputs through
// .NET 8 (Path.GetDirectoryName, FileInfo.Name, Path.Combine), which is what
// the legacy pipeline used. The one intentional difference is documented in
// path.go: .NET normalises "/" to "\" on Windows, this port keeps the
// separator the input used so the engine works on Linux too.

func TestDirNameMatchesDotNet(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want string
	}{
		// The values on the left are the .NET results verbatim, except that
		// "/" is kept where .NET printed "\" (noted per case).
		{``, ``},
		{`D:\out\00001.m2ts`, `D:\out`},
		{`D:\out\ep01.work`, `D:\out`},
		{`D:\out\`, `D:\out`},
		{`D:\out`, `D:\`},
		{`D:\`, ``}, // a bare root has no directory name
		{`D:`, ``},
		{`D:\a\b\c`, `D:\a\b`},
		{`D:\a\b\c\`, `D:\a\b\c`},
		{`D:\a\\`, `D:\a`}, // repeated separators are trimmed
		{`ep01`, ``},
		{`out/`, `out`}, // .NET: "out"
		{`out/ep01`, `out`},
		{`a/b`, `a`},
		{`a//b`, `a`},
		{`a/`, `a`},
		{`/`, ``},      // a bare root
		{`/out`, `/`},  // .NET: "\"
		{`/a/b`, `/a`}, // .NET: "\a"
		{`\a`, `\`},
		{`\a\b`, `\a`},
		{`\\`, ``},
		{`\\server`, ``},
		{`\\server\`, ``},
		{`\\server\share`, ``},
		{`\\server\share\`, `\\server\share`},
		{`\\server\share\dir`, `\\server\share`},
		{`\\server\share\dir\file.mkv`, `\\server\share\dir`},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if got := dirName(tc.path); got != tc.want {
				t.Errorf("dirName(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestBaseNameMatchesDotNet(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want string
	}{
		{`D:\BDMV\STREAM\00001.m2ts`, `00001.m2ts`},
		{`00001.m2ts`, `00001.m2ts`},
		{`D:/in/ep01.m2ts`, `ep01.m2ts`},
		{`D:\out\`, ``}, // a path ending in a separator has no name
		{`D:\out`, `out`},
		{`ep01`, `ep01`},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			if got := baseName(tc.path); got != tc.want {
				t.Errorf("baseName(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestJoinPathMatchesDotNet(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		dir  string
		file string
		want string
	}{
		{"windows separator", `D:\out`, "00001.m2ts.mkv", `D:\out\00001.m2ts.mkv`},
		{"empty directory", ``, "00001.m2ts.mkv", "00001.m2ts.mkv"},
		{"empty name", `D:\out`, ``, `D:\out`},
		{"both empty", ``, ``, ``},
		{"forward separator is kept", "out", "00001.m2ts.mkv", "out/00001.m2ts.mkv"},
		{"trailing separator", `D:\out\`, "f.mkv", `D:\out\f.mkv`},
		{"rooted name replaces the directory", `D:\out`, `C:\dest\f.mkv`, `C:\dest\f.mkv`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := joinPath(tc.dir, tc.file); got != tc.want {
				t.Errorf("joinPath(%q, %q) = %q, want %q", tc.dir, tc.file, got, tc.want)
			}
		})
	}
}
