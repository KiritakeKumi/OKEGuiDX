package ffmpeg

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
)

// TestMain turns the test binary into the fake ffmpeg and ffprobe when the
// demuxer spawns it, and otherwise runs the suite normally.
//
// The demuxer builds its own argument lists, so the child cannot be told to run
// only TestHelperProcess through -test.run: Go's flag parser rejects ffmpeg's
// arguments outright. The child is instead identified by the marker the test
// writes into the environment, whose value is the report file to print. Without
// this guard the child would run the whole suite, and TestRunEndToEnd inside it
// would spawn further children without bound.
func TestMain(m *testing.M) {
	// The fail-at case takes precedence: it simulates a tool exiting with a
	// status before it produces any output.
	if failAt := os.Getenv(helperEnvFailAt); failAt != "" {
		os.Exit(atoi(failAt))
	}
	if report := os.Getenv(helperEnvReport); report != "" {
		runFakeTool(report)
		return
	}
	// The demuxer logs every child-process line at trace level, which drowns
	// the test output; the package under test does not need it.
	log.SetLevel("ERROR")
	os.Exit(m.Run())
}

// runFakeTool is the child process body. It serves both roles:
//
//   - asked for a probe (the argument list contains -show_streams), it prints
//     the configured report;
//   - otherwise it is an extraction run: it records the argument list and
//     writes the file named by the last argument.
//
// It never returns.
func runFakeTool(reportPath string) {
	args := os.Args[1:]
	logArgs(args)

	if isProbeRun(args) {
		b, err := os.ReadFile(reportPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "helper: read report:", err)
			os.Exit(9)
		}
		os.Stdout.Write(b)
		os.Exit(0)
	}

	if code := os.Getenv(helperEnvExtractExit); code != "" {
		fmt.Fprintln(os.Stderr, "helper: extraction refused")
		os.Exit(atoi(code))
	}

	if err := writeOutputs(args); err != nil {
		fmt.Fprintln(os.Stderr, "helper: write:", err)
		os.Exit(9)
	}
	emitProgress(args)
	os.Exit(0)
}

// logArgs appends the argument list to the log the test asked for.
func logArgs(args []string) {
	path := os.Getenv(helperEnvArgsLog)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper: open args log:", err)
		os.Exit(9)
	}
	defer f.Close()
	if _, err := fmt.Fprintln(f, strings.Join(args, " ")); err != nil {
		fmt.Fprintln(os.Stderr, "helper: write args log:", err)
		os.Exit(9)
	}
}

// isProbeRun reports whether the arguments are the ffprobe shape.
func isProbeRun(args []string) bool {
	for _, a := range args {
		if a == "-show_streams" {
			return true
		}
	}
	return false
}

// writeOutputs creates the file the run targets. The demuxer always passes the
// destination last, and the fake content is sized from the configured map keyed
// by the file's base name.
func writeOutputs(args []string) error {
	if len(args) == 0 {
		return nil
	}
	target := args[len(args)-1]
	if strings.HasPrefix(target, "-") {
		// A run with no output file (the probe is handled above, so this is an
		// argument the fake does not understand).
		return nil
	}
	sizes := map[string]int{}
	if raw := os.Getenv(helperEnvSizes); raw != "" {
		if err := json.Unmarshal([]byte(raw), &sizes); err != nil {
			return fmt.Errorf("bad sizes: %w", err)
		}
	}
	n := sizes[baseName(target)]
	if n == 0 {
		n = 1024
	}
	return os.WriteFile(target, make([]byte, n), 0o600)
}

// emitProgress prints a plausible -progress stream so the parser is exercised.
func emitProgress(args []string) {
	for _, a := range args {
		if a == "-progress" {
			fmt.Fprintln(os.Stdout, "out_time_us=1000000")
			fmt.Fprintln(os.Stdout, "progress=continue")
			fmt.Fprintln(os.Stdout, "out_time_us=6000000")
			fmt.Fprintln(os.Stdout, "progress=end")
			return
		}
	}
}

func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
