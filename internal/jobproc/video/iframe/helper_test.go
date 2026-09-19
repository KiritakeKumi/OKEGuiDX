package iframe

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Run drives a real child process, so the tests need a fake vspipe. Rather than
// shipping a helper binary or a shell wrapper, the test binary re-executes
// itself: TestMain intercepts the invocation before the testing flags are
// parsed and plays the tool. This keeps the production Run code path completely
// unmodified, including the exact argv it builds.

const (
	// helperEnvVar marks a re-executed test binary as the fake vspipe.
	helperEnvVar = "OKEGUIDX_IFRAME_HELPER"
	// helperFixtureEnvVar names the fixture the fake vspipe replays.
	helperFixtureEnvVar = "OKEGUIDX_IFRAME_FIXTURE"
	// helperRoleEnvVar selects the behaviour of the fake vspipe.
	helperRoleEnvVar = "OKEGUIDX_IFRAME_ROLE"

	roleFixture = "fixture"
	roleNoList  = "no-list"
	roleEmpty   = "empty"
	roleFail    = "fail"
	// helperRecordEnvVar names the file receiving the recorded argv.
	helperRecordEnvVar = "OKEGUIDX_IFRAME_RECORD"
)

// TestMain doubles as the entry point of the fake vspipe.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnvVar) == "1" {
		os.Exit(fakeVSPipe())
	}
	os.Exit(m.Run())
}

// fakeVSPipe imitates `vspipe --info <script> -` for the current test. It sees
// the same os.Args the production code passed to proc.Start.
func fakeVSPipe() int {
	if record := os.Getenv(helperRecordEnvVar); record != "" {
		if err := os.WriteFile(record, []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
			return 6
		}
	}

	switch os.Getenv(helperRoleEnvVar) {
	case roleFixture:
		return replayFixture(os.Getenv(helperFixtureEnvVar))
	case roleNoList:
		// The real vspipe prints the properties on stdout, nothing on stderr.
		fmt.Fprint(os.Stdout, "Output Index: 0\nType: Video\nFrames: 100\nBits: 8\n")
		return 0
	case roleEmpty:
		return 0
	case roleFail:
		fmt.Fprint(os.Stderr, "some diagnostic\n")
		return 1
	default:
		return 2
	}
}

// replayFixture copies a fixture onto the two streams the way a real run
// distributes them: the vspipe properties go to stdout, and the generated
// script's own print (the IFrameList line) goes to stderr.
func replayFixture(name string) int {
	f, err := os.Open(name)
	if err != nil {
		return 4
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, IFrameMarker) {
			fmt.Fprintln(os.Stderr, line)
			continue
		}
		fmt.Fprintln(os.Stdout, line)
	}
	if err := sc.Err(); err != nil {
		return 5
	}
	return 0
}
