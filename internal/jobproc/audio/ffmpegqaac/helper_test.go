package ffmpegqaac

import (
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// The smoke test needs two real child processes. Rather than shipping helper
// binaries, the tests re-execute the test binary itself with a sentinel
// environment variable, the same pattern internal/proc uses.

const (
	helperEnvVar = "OKEGUIDX_FFMPEGQAAC_HELPER"
	// helperPayload is what the fake ffmpeg writes to stdout; the fake qaac
	// copies its stdin into the -o file, so the smoke test can prove the pipe
	// carried the bytes across the two processes.
	helperPayload = "FAKE-WAV-PAYLOAD"
)

// helperSpec builds a Spec that re-executes the test binary as the helper, the
// same shape internal/proc uses. The role selects which tool to imitate.
func helperSpec(t *testing.T, role string, toolArgs ...string) proc.Spec {
	t.Helper()
	args := append([]string{"-test.run=TestHelperProcess", "--", role}, toolArgs...)
	return proc.Spec{
		Path: helperPath(t),
		Args: args,
		Env:  append(os.Environ(), helperEnvVar+"=1"),
		Name: role,
	}
}

// helperPath returns the executable to re-execute: this test binary.
func helperPath(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	return exe
}

// TestHelperProcess is not a real test. It acts as the child process for the
// smoke test.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnvVar) != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	switch args[0] {
	case "ffmpeg":
		// The producer: the payload goes to stdout (which the pipeline wires
		// into qaac), progress goes to stderr (which the processor parses).
		if _, err := io.WriteString(os.Stdout, helperPayload); err != nil {
			os.Exit(3)
		}
		fmt.Fprint(os.Stderr, "\r0:05.000 (10.0x)   ")
		os.Exit(0)
	case "qaac":
		// The consumer: everything arriving on stdin is the ffmpeg payload and
		// must land in the -o file.
		record := ""
		for i, a := range args {
			if a == "-o" && i+1 < len(args) {
				record = args[i+1]
			}
		}
		if record == "" {
			os.Exit(4)
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(5)
		}
		if err := os.WriteFile(record, data, 0o600); err != nil {
			os.Exit(6)
		}
		fmt.Fprint(os.Stderr, "\r[100.0%] 0:10.000/0:10.000 (10.0x), ETA 0:00.000  ")
		fmt.Fprintln(os.Stderr, "Optimizing...done")
		os.Exit(0)
	}
	os.Exit(2)
}
