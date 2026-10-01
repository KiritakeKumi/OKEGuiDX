package video

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// fakeHelpEnv makes the test binary impersonate vspipe: when it is set, the
// binary prints that text for --help and exits instead of running tests.
const fakeHelpEnv = "OKEGUIDX_FAKE_VSPIPE_HELP"

// fakeRoleEnv makes the test binary play both ends of the pipeline, told apart
// by argv: --help is the capability probe, "-o" is the encoder, anything else
// is vspipe, which fails the way R73 does on an argument it does not know.
const fakeRoleEnv = "OKEGUIDX_FAKE_PIPELINE"

func TestMain(m *testing.M) {
	if _, ok := os.LookupEnv(fakeRoleEnv); ok {
		os.Exit(fakePipelineRole(os.Args[1:]))
	}
	if help, ok := os.LookupEnv(fakeHelpEnv); ok {
		fmt.Fprint(os.Stderr, help)
		os.Exit(1) // old and new vspipe disagree on --help's exit code; neither may matter
	}
	os.Exit(m.Run())
}

// Help texts are the relevant lines of real vspipe output: R42 only knows
// --y4m, R73 only knows --container.
const (
	helpR42 = "VSPipe usage:\n  vspipe.exe [options] <script> <outfile>\n" +
		"Available options:\n  -y, --y4m   Add YUV4MPEG headers to output\n"
	helpR73 = "VSPipe R73 usage:\n  vspipe [options] <script> <outfile>\n" +
		"Available options:\n  -c, --container <y4m/wav/w64>    Add headers for the specified format\n"
)

func TestY4MArgsFollowsWhatVSPipeAccepts(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		help string
		want []string
	}{
		{"R42 spelling", helpR42, []string{"--y4m"}},
		{"R73 spelling", helpR73, []string{"-c", "y4m"}},
		{"unrecognisable help keeps the legacy flag", "", []string{"--y4m"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(fakeHelpEnv, tc.help)
			containerFlag.Delete(self) // the answer is cached per path
			got := y4mArgs(context.Background(), self)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("y4mArgs = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestY4MArgsUnstartableVSPipeFallsBackAndIsNotCached(t *testing.T) {
	missing := t.TempDir() + string(os.PathSeparator) + "no-such-vspipe"
	got := y4mArgs(context.Background(), missing)
	if !reflect.DeepEqual(got, []string{"--y4m"}) {
		t.Errorf("y4mArgs = %v, want the legacy flag", got)
	}
	if _, cached := containerFlag.Load(missing); cached {
		t.Error("a vspipe that could not be started must not be cached as 'old'")
	}
}

func fakePipelineRole(args []string) int {
	for _, a := range args {
		switch a {
		case "--help":
			fmt.Fprint(os.Stdout, helpR73)
			return 0
		case "-o":
			_, _ = io.Copy(io.Discard, os.Stdin)
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, "Unknown argument: --bogus")
	return 1
}

// A vspipe that dies must leave its own words in the error. The pipeline used
// to report only "vspipe 异常退出", so the cause was invisible to the user.
func TestRunKeepsVSPipeStderrWhenItFails(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fakeRoleEnv, "1")
	containerFlag.Delete(self)

	b := NewBase(EncodeSpec{
		VSPipe:      self,
		Script:      "blank.vpy",
		Encoder:     self,
		EncoderName: "fake",
		Output:      "out.hevc",
		StdinArg:    "-",
		FrameEnd:    -1,
	})
	err = b.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("Run() = nil, want the vspipe failure")
	}
	e := okerr.AsError(err)
	if !strings.Contains(e.Output, "Unknown argument: --bogus") {
		t.Errorf("Output = %q, want vspipe's stderr", e.Output)
	}
	if e.Tool != "vspipe" || e.ExitCode != 1 {
		t.Errorf("Tool/ExitCode = %q/%d, want vspipe/1", e.Tool, e.ExitCode)
	}
}
