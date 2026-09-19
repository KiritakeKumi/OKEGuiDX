package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// Exit codes. A script that drives okegui has to tell the two ways a run can
// fail apart: a broken profile means "fix the input, do not retry", while a
// failed encode means "look at the log".
//
//	0    success
//	1    internal failure: the command could not be carried out at all
//	2    the engine ran and at least one task failed
//	3    user error: bad command line, unusable profile, missing tool
//	130  interrupted by SIGINT/SIGTERM
const (
	exitOK          = 0
	exitFailure     = 1
	exitTaskFailed  = 2
	exitUsage       = 3
	exitInterrupted = 130
)

// errHelp is returned when -h/--help was requested. The caller decides which
// stream the usage text goes to, so it is a value rather than a print.
var errHelp = errors.New("help requested")

// exitError carries a process exit code together with the message the operator
// sees.
type exitError struct {
	code int
	err  error
}

// Error implements error.
func (e *exitError) Error() string { return e.err.Error() }

// Unwrap exposes the cause to errors.Is / errors.As.
func (e *exitError) Unwrap() error { return e.err }

// fail builds an exitError with a formatted message.
func fail(code int, format string, args ...any) *exitError {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

// wrapExit attaches an exit code to an existing error.
func wrapExit(code int, err error) *exitError {
	return &exitError{code: code, err: err}
}

// exitCodeOf maps an error to a process exit code. Errors that already carry a
// code win; otherwise the okerr kind decides, because the kinds were chosen to
// separate "the operator must change something" from "the engine broke".
func exitCodeOf(err error) int {
	if err == nil {
		return exitOK
	}
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var ve *profile.ValidationError
	if errors.As(err, &ve) {
		return exitUsage
	}
	var oe *okerr.Error
	if errors.As(err, &oe) {
		switch oe.Kind {
		case okerr.KindConfig, okerr.KindNotFound, okerr.KindUnsupported, okerr.KindMismatch:
			return exitUsage
		}
	}
	return exitFailure
}

// printError writes the operator-facing form of a failure to w. The legacy code
// showed these strings in a MessageBox; stderr is the headless equivalent.
func printError(w io.Writer, err error) {
	var ve *profile.ValidationError
	if errors.As(err, &ve) {
		fmt.Fprintf(w, "okegui: %s\n", ve.Summary)
		if ve.Detail != "" {
			fmt.Fprintf(w, "        %s\n", ve.Detail)
		}
		return
	}
	var oe *okerr.Error
	if errors.As(err, &oe) {
		// okerr.Render is the operator-facing template. When it does not carry
		// the summary — the templates that only interpolate a detail — the
		// summary is printed first so the failure is still identifiable.
		rendered := okerr.Render(oe)
		if oe.Summary != "" && !strings.Contains(rendered, oe.Summary) {
			fmt.Fprintf(w, "okegui: %s\n", oe.Summary)
			fmt.Fprintf(w, "        %s\n", rendered)
			return
		}
		fmt.Fprintf(w, "okegui: %s\n", rendered)
		return
	}
	fmt.Fprintf(w, "okegui: %v\n", err)
}
