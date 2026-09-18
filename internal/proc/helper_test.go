package proc

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

// The proc package needs real child processes to test against. Rather than
// shipping a helper binary, the tests re-execute the test binary itself with a
// sentinel environment variable; this is the standard Go pattern for testing
// process handling and keeps the repository free of build artefacts.

const (
	helperEnvVar = "OKEGUIDX_PROC_HELPER"
	helperCmd    = "emit"
	helperSleep  = "sleep"
	helperSink   = "sink"
	helperExit   = "exit"
)

// TestHelperProcess is not a real test. It acts as the child process for the
// tests in this package.
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
	case helperCmd:
		fmt.Println("hello stdout")
		fmt.Fprintln(os.Stderr, "hello stderr")
		os.Exit(7)
	case helperSleep:
		// Sleep long enough that the test can pause, resume and kill it.
		select {}
	case helperSink:
		// Echo everything read from stdin back out, so the test can assert
		// that a pipe really carried data across two processes.
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				os.Stdout.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		os.Exit(0)
	case helperExit:
		code := 0
		if len(args) > 1 {
			_, _ = fmt.Sscanf(args[1], "%d", &code)
		}
		os.Exit(code)
	}
	os.Exit(2)
}

// helperSpec builds a Spec that re-executes the test binary as the helper.
func helperSpec(name string, helperArgs ...string) Spec {
	args := append([]string{"-test.run=TestHelperProcess", "--"}, helperArgs...)
	return Spec{
		Path: os.Args[0],
		Args: args,
		Env:  append(os.Environ(), helperEnvVar+"=1"),
		Name: name,
	}
}

// wrapForTest mirrors what okerr.Wrap does, used to check that wrapping keeps
// errors.Is working.
func wrapForTest(err error) error {
	return fmt.Errorf("wrapped: %w", err)
}

var _ = errors.Is
