package video

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// helpTimeout bounds the `vspipe --help` probe. The help text is printed
// before any script is loaded, so anything slower than this is not a vspipe.
const helpTimeout = 10 * time.Second

// y4mArgs is the vspipe argument vector that asks for y4m output. VapourSynth
// replaced `--y4m` with `-c/--container <y4m|wav|w64>` and later removed the
// old spelling ("Unknown argument: --y4m" on R73), while R42 only knows the old
// one. The two are told apart by asking vspipe itself, not by comparing a
// version number: the help text is what the binary will actually accept.
func y4mArgs(ctx context.Context, vspipe string) []string {
	if vspipeHasContainerFlag(ctx, vspipe) {
		return []string{"-c", "y4m"}
	}
	return []string{"--y4m"}
}

// containerFlag caches the probe per path, since a worker runs many encodes
// against the same vspipe and the answer cannot change under it.
var containerFlag sync.Map // map[string]bool

func vspipeHasContainerFlag(ctx context.Context, vspipe string) bool {
	if v, ok := containerFlag.Load(vspipe); ok {
		return v.(bool)
	}
	ctx, cancel := context.WithTimeout(ctx, helpTimeout)
	defer cancel()
	res, err := proc.Run(ctx, proc.Spec{Path: vspipe, Args: []string{"--help"}, Name: "vspipe"})
	if res == nil {
		// vspipe could not even be started. Say nothing about it and let the
		// real run report that, rather than caching a guess.
		return false
	}
	// Old and new builds disagree on the exit code of --help, and on whether
	// it goes to stdout or stderr, so the exit status is not consulted.
	_ = err
	has := strings.Contains(res.Stdout, "--container") || strings.Contains(res.Stderr, "--container")
	containerFlag.Store(vspipe, has)
	return has
}
