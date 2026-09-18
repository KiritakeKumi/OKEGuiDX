// Package x265 wraps the x265 encoder.
//
// This is the reference implementation for every jobproc wrapper: it shows the
// expected shape of a package in this repository (spec struct, parser,
// table-driven tests with fixtures in testdata/). See WORKSTREAMS.md §4.3.
//
// Behaviour is defined by JobProcessor/Video/X265Encoder.cs; the output patterns
// it recognises are reproduced verbatim below, including the Asuna variant.
package x265

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// Name is the processor name used in logs and status events.
const Name = "x265"

// Progress line patterns, taken from X265Encoder.ProcessLine.
//
//   - official builds print "N frames: X fps, Y kb/s"
//   - the Asuna fork prints "N/M frames, X fps, Y kb/s"
//   - both print a summary "encoded N frames in T s (X fps), Y kb/s, Avg QP:Z"
var (
	reOfficial = regexp.MustCompile(`([0-9]+) frames: ([0-9]+(?:\.[0-9]+)?) fps, ([0-9]+(?:\.[0-9]+)?) kb/s`)
	reAsuna    = regexp.MustCompile(`([0-9]+)/[0-9]+ frames, ([0-9]+(?:\.[0-9]+)?) fps, ([0-9]+(?:\.[0-9]+)?) kb/s`)
	reSummary  = regexp.MustCompile(`encoded ([0-9]+) frames in ([0-9]+(?:\.[0-9]+)?)s \(([0-9]+(?:\.[0-9]+)?) fps\), ([0-9]+(?:\.[0-9]+)?) kb/s, Avg QP:([0-9]+(?:\.[0-9]+)?)`)
)

// Fatal output patterns. x265 prints these and keeps going, so they must be
// turned into errors immediately.
const (
	errPrefix        = "x265 [error]:"
	errUnknownOption = "unknown option"
	errFwriteFailed  = "Error: fwrite() call failed when writing frame: "
)

// Options configures an x265 run.
type Options struct {
	// VSPipe is the absolute path to vspipe.
	VSPipe string
	// Script is the .vpy path.
	Script string
	// VSPipeArgs are extra vspipe --arg values.
	VSPipeArgs []string
	// Encoder is the absolute path to x265.
	Encoder string
	// Params is the profile's EncoderParam string. It is passed through
	// unchanged; only --asm may be appended.
	Params string
	// Asm optionally forces an assembly level, mirroring the legacy avx512
	// configuration option. Ignored when Params already contains "--asm".
	Asm string
	// Output is the destination .hevc file.
	Output string
	// TotalFrames is the expected frame count.
	TotalFrames int64
	// FrameStart and FrameEnd bound a partial (re-encode) run. FrameEnd < 0
	// means "to the end".
	FrameStart int64
	FrameEnd   int64
}

// Processor encodes with x265.
type Processor struct {
	*video.Base
	opts Options
}

// New returns a Processor for the given options.
func New(opts Options) *Processor {
	p := &Processor{opts: opts}
	p.Base = video.NewBase(video.EncodeSpec{
		VSPipe:      opts.VSPipe,
		VSPipeArgs:  opts.VSPipeArgs,
		Script:      opts.Script,
		Encoder:     opts.Encoder,
		EncoderName: Name,
		EncoderArgs: buildArgs(opts),
		Output:      opts.Output,
		FrameStart:  opts.FrameStart,
		FrameEnd:    opts.FrameEnd,
		TotalFrames: opts.TotalFrames,
		StdinArg:    "--y4m",
	})
	p.SetParser(video.ParseFunc(p.parse))
	p.SetFatalLine(fatalLine)
	return p
}

// buildArgs assembles the x265 command line.
//
// The legacy code wrapped this in cmd.exe and inserted the input with "-"; here
// the input flag is supplied by the base class through EncodeSpec.StdinArg.
func buildArgs(opts Options) []string {
	var args []string
	if opts.Asm != "" && !strings.Contains(strings.ToLower(opts.Params), "--asm") {
		args = append(args, "--asm", opts.Asm)
	}
	// EncoderParam is a pre-tokenised string in every real profile; splitting on
	// spaces mirrors how cmd.exe used to pass it to the process.
	args = append(args, splitParams(opts.Params)...)
	return args
}

// splitParams splits an encoder parameter string into arguments, honouring
// double quotes so that values containing spaces survive.
func splitParams(s string) []string {
	var (
		args    []string
		cur     strings.Builder
		inQuote bool
	)
	flush := func() {
		if cur.Len() > 0 {
			args = append(args, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case r == ' ' && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return args
}

// parse extracts progress from one x265 output line.
func (p *Processor) parse(line string) (jobproc.Progress, bool, error) {
	// The final summary line reports the totals and ends the encode.
	if m := reSummary.FindStringSubmatch(line); m != nil {
		frames, _ := strconv.ParseInt(m[1], 10, 64)
		return jobproc.Progress{
			FramesDone:  frames,
			FramesTotal: p.opts.TotalFrames,
			Percent:     percent(frames, p.opts.TotalFrames),
			Speed:       m[3] + " fps",
			BitRate:     m[4] + " kb/s",
			Status:      "压制完成",
		}, true, nil
	}

	// Progress lines come in two dialects; try the newer Asuna form first so a
	// line that matches both is attributed correctly.
	if m := reAsuna.FindStringSubmatch(line); m != nil {
		return p.progress(m[1], m[2], m[3]), true, nil
	}
	if m := reOfficial.FindStringSubmatch(line); m != nil {
		return p.progress(m[1], m[2], m[3]), true, nil
	}
	return jobproc.Progress{}, false, nil
}

func (p *Processor) progress(frames, fps, kbps string) jobproc.Progress {
	done, _ := strconv.ParseInt(frames, 10, 64)
	return jobproc.Progress{
		FramesDone:  done,
		FramesTotal: p.opts.TotalFrames,
		Percent:     percent(done, p.opts.TotalFrames),
		Speed:       fps + " fps",
		BitRate:     kbps + " kb/s",
		Status:      "压制中",
	}
}

// fatalLine turns x265's own error messages into structured errors, exactly as
// X265Encoder.ProcessLine did.
func fatalLine(line string) error {
	switch {
	case strings.Contains(line, errPrefix), strings.Contains(line, errUnknownOption):
		return okerr.New(okerr.KindTool, okerr.ErrX265.Summary, "%s", line).WithOutput(line)
	case strings.Contains(line, errFwriteFailed):
		return okerr.New(okerr.KindToolCrash, okerr.ErrX265Crash.Summary,
			"写入帧失败，磁盘可能已满").WithOutput(line)
	}
	return nil
}

func percent(done, total int64) float64 {
	if total <= 0 {
		return -1
	}
	return float64(done) / float64(total) * 100
}
