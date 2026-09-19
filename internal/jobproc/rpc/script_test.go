package rpc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The template fixture is the real RpcTemplate.vpy the release workflow
// downloads from AmusementClub/vapoursynth-script.

func TestScriptContentReplacesPlaceholders(t *testing.T) {
	t.Parallel()

	template := string(readFixture(t, "RpcTemplate.vpy"))

	cases := []struct {
		name         string
		sourceScript string
		videoFile    string
		args         []string
		wantContains []string
		wantAbsent   []string
	}{
		{
			name:         "no vspipe arguments",
			sourceScript: `D:\work\source.vpy`,
			videoFile:    `D:\work\00001.mkv`,
			wantContains: []string{
				`sourceScript = r"D:\work\source.vpy"`,
				`videoFile = r"D:\work\00001.mkv"`,
				"loader.exec_module(mod)",
			},
			wantAbsent: []string{placeholderSource, placeholderVideo},
		},
		{
			name:         "arguments become setattr clauses",
			sourceScript: `D:\work\source.vpy`,
			videoFile:    `D:\work\00001.mkv`,
			args:         []string{"deint=yes", "message=fluffy kittens"},
			wantContains: []string{
				"setattr(mod, 'deint', b'yes')\n",
				"setattr(mod, 'message', b'fluffy kittens')\n",
			},
			wantAbsent: []string{placeholderArgs},
		},
		{
			name:         "empty argument list leaves no clauses",
			sourceScript: "a.vpy",
			videoFile:    "b.mkv",
			wantAbsent:   []string{placeholderSource, placeholderVideo, placeholderArgs},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ScriptContent(template, tc.sourceScript, tc.videoFile, tc.args)
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("script does not contain %q\n%s", want, got)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(got, absent) {
					t.Errorf("script still contains placeholder %q", absent)
				}
			}
		})
	}
}

// TestScriptContentKeepsTemplateBody guards against a replacement eating the
// rest of the template: only the three markers may change.
func TestScriptContentKeepsTemplateBody(t *testing.T) {
	t.Parallel()

	template := string(readFixture(t, "RpcTemplate.vpy"))
	got := ScriptContent(template, "src.vpy", "rip.mkv", nil)

	if !strings.Contains(got, `print("RPCOUT:", n, ' '.join([ '%f' % p.props.PlanePSNR for p in f ]), flush=True, file=sys.stderr)`) {
		t.Error("the RPCOUT callback line was altered")
	}
	if !strings.Contains(got, "cmp.set_output()") {
		t.Error("the template tail was altered")
	}
}

// TestScriptContentReplacementOrder pins the legacy replacement order: the
// source script is substituted first, so a source path that itself contains
// "OKE:VIDEO_FILE" is rewritten by the following pass. That is exactly what
// RpChecker.GetRpcScript did, and it is preserved rather than "fixed": the
// substituted text is a path, and a real path never contains that marker.
func TestScriptContentReplacementOrder(t *testing.T) {
	t.Parallel()

	// The template is minimal on purpose: it isolates the replacement order
	// from everything else in RpcTemplate.vpy.
	const template = "src=r\"OKE:SOURCE_SCRIPT\"\nvid=r\"OKE:VIDEO_FILE\"\nOKE:VSPIPE_ARGS\nend"
	got := ScriptContent(template, `D:\OKE:VIDEO_FILE\x.vpy`, `D:\rip.mkv`, nil)

	want := "src=r\"D:\\D:\\rip.mkv\\x.vpy\"\nvid=r\"D:\\rip.mkv\"\n\nend"
	if got != want {
		t.Fatalf("ScriptContent() = %q\nwant %q", got, want)
	}
}

func TestArgsClauses(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"empty", nil, ""},
		{"single", []string{"deint=yes"}, "setattr(mod, 'deint', b'yes')\n"},
		{
			name: "value containing equals signs",
			args: []string{"filter=a=b"},
			want: "setattr(mod, 'filter', b'a=b')\n",
		},
		{
			name: "backslashes are escaped",
			args: []string{`path=C:\work`},
			want: "setattr(mod, 'path', b'C:\\\\work')\n",
		},
		{
			name: "malformed argument is skipped",
			args: []string{"novalue"},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ArgsClauses(tc.args); got != tc.want {
				t.Errorf("ArgsClauses(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// TestScriptPath mirrors RpChecker's Path.ChangeExtension replacement: only
// the final extension is dropped, and "_rpc.vpy" is appended.
func TestScriptPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{`D:\work\00001.mkv`, `D:\work\00001_rpc.vpy`},
		{`/work/00001.mkv`, `/work/00001_rpc.vpy`},
		{`D:\work\00001`, `D:\work\00001_rpc.vpy`},
		{`D:\work.d\00001.mkv`, `D:\work.d\00001_rpc.vpy`},
		{`D:\我的 作品\第01話.mkv`, `D:\我的 作品\第01話_rpc.vpy`},
	}

	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := ScriptPath(tc.in); got != tc.want {
				t.Errorf("ScriptPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestWriteScript(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	templatePath := filepath.Join(dir, "RpcTemplate.vpy")
	template := string(readFixture(t, "RpcTemplate.vpy"))
	if err := os.WriteFile(templatePath, []byte(template), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}
	ripped := filepath.Join(dir, "00001.mkv")

	got, err := WriteScript(templatePath, `D:\src\source.vpy`, ripped, []string{"deint=yes"})
	if err != nil {
		t.Fatalf("WriteScript() error = %v", err)
	}
	if want := filepath.Join(dir, "00001_rpc.vpy"); got != want {
		t.Fatalf("WriteScript() = %q, want %q", got, want)
	}
	data, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("read generated script: %v", err)
	}
	content := string(data)
	for _, want := range []string{
		`sourceScript = r"D:\src\source.vpy"`,
		`videoFile = r"` + ripped + `"`,
		"setattr(mod, 'deint', b'yes')",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("generated script does not contain %q", want)
		}
	}
}

func TestWriteScriptMissingTemplate(t *testing.T) {
	t.Parallel()

	_, err := WriteScript(filepath.Join(t.TempDir(), "missing.vpy"), "a.vpy", "b.mkv", nil)
	if err == nil {
		t.Fatal("WriteScript() = nil, want an error")
	}
	if !strings.Contains(err.Error(), "找不到RPC模板") {
		t.Errorf("error = %v, want it to name the missing template", err)
	}
}
