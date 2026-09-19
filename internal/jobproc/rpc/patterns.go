package rpc

import "regexp"

// reTraceback matches the terminal line of a VapourSynth traceback, verbatim
// from RpChecker.ProcessLine:
//
//	^([a-zA-Z_.]*)(Error|Exception|Exit|Interrupt|Iteration|Warning)(.*)
//
// A real traceback ends with a line such as
// `ModuleNotFoundError: No module named 'x'`.
var reTraceback = regexp.MustCompile(`^([a-zA-Z_.]*)(Error|Exception|Exit|Interrupt|Iteration|Warning)(.*)`)
