// Package x264 wraps the x264 encoder.
//
// Behaviour is defined by JobProcessor/Video/X264Encoder.cs. The pipeline is
// shared with the other encoders (see internal/jobproc/video); this package
// only supplies x264's argument shape and its output dialects.
//
// Two details differ from x265 and are easy to get wrong:
//
//   - the input flag is "--demuxer y4m" rather than "--y4m";
//   - the progress line uses a colon ("N frames: X fps, Y kb/s") while the
//     completion line uses a comma ("encoded N frames, X fps, Y kb/s").
package x264

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// Name is the processor name used in logs and status events.
const Name = "x264"

// Output patterns, taken from X264Encoder.ProcessLine.
//
//   - the completion line is matched first, because it also contains the word
//     "encoded" that the original used as a cheap pre-filter;
//   - the progress line is matched case-insensitively.
var (
	reSummary  = regexp.MustCompile(`encoded ([0-9]+) frames, ([0-9]+\.[0-9]+) fps, ([0-9]+\.[0-9]+) kb/s`)
	reProgress = regexp.MustCompile(`(?i)([0-9]+) frames: ([0-9]+\.[0-9]+) fps, ([0-9]+\.[0-9]+) kb/s`)
)

// Fatal output patterns.
const (
	errPrefix       = "x264 [error]:"
	errUnknownOpt   = "unknown option"
	errFwriteFailed = "Error: fwrite() call failed when writing frame: "
)

// Options configures an x264 run.
type Options struct {
	// VSPipe is the absolute path to vspipe.
	VSPipe string
	// Script is the .vpy path.
	Script string
	// VSPipeArgs are extra vspipe --arg values.
	VSPipeArgs []string
	// Encoder is the absolute path to x264.
	Encoder string
	// Params is the profile's EncoderParam string, passed through unchanged
	// except for an optional --asm prefix.
	Params string
	// Asm optionally forces an assembly level, mirroring the legacy avx512
	// configuration option. Ignored when Params already names an --asm value.
	Asm string
	// Output is the destination .h264/.264 file.
	Output string
	// TotalFrames is the expected frame count.
	TotalFrames int64
	// FrameStart and FrameEnd bound a partial (re-encode) run. FrameEnd < 0
	// means "to the end".
	FrameStart int64
	FrameEnd   int64
}

// Processor encodes with x264.
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
		EncoderArgs: BuildArgs(opts),
		Output:      opts.Output,
		FrameStart:  opts.FrameStart,
		FrameEnd:    opts.FrameEnd,
		TotalFrames: opts.TotalFrames,
		// x264 names the demuxer and then reads from stdin; that is three
		// tokens, which is why InputArgs exists.
		InputArgs: []string{"--demuxer", "y4m", "-"},
	})
	p.SetParser(video.ParseFunc(p.parse))
	p.SetFatalLine(FatalLine)
	return p
}

// BuildArgs assembles the x264 command line.
//
// It is exported so the argument shape can be tested without starting a
// process, and so callers that need to display the command can do so.
func BuildArgs(opts Options) []string {
	var args []string
	if opts.Asm != "" && !strings.Contains(strings.ToLower(opts.Params), "--asm") {
		args = append(args, "--asm", opts.Asm)
	}
	args = append(args, splitParams(opts.Params)...)
	// The stdin marker and the output path are appended by the base class.
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

// parse extracts progress from one x264 output line.
func (p *Processor) parse(line string) (jobproc.Progress, bool, error) {
	// The completion line comes first: it also matches the progress pattern's
	// frame-count shape, and treating it as progress would leave the frame
	// count short of the total.
	if m := reSummary.FindStringSubmatch(line); m != nil {
		frames, _ := strconv.ParseInt(m[1], 10, 64)
		return jobproc.Progress{
			FramesDone:  frames,
			FramesTotal: p.opts.TotalFrames,
			Percent:     percent(frames, p.opts.TotalFrames),
			Speed:       m[2] + " fps",
			BitRate:     m[3] + " kb/s",
			Status:      "压制完成",
		}, true, nil
	}

	if m := reProgress.FindStringSubmatch(line); m != nil {
		frames, _ := strconv.ParseInt(m[1], 10, 64)
		return jobproc.Progress{
			FramesDone:  frames,
			FramesTotal: p.opts.TotalFrames,
			Percent:     percent(frames, p.opts.TotalFrames),
			Speed:       m[2] + " fps",
			BitRate:     m[3] + " kb/s",
			Status:      "压制中",
		}, true, nil
	}
	return jobproc.Progress{}, false, nil
}

// FatalLine turns x264's own error messages into structured errors, exactly as
// X264Encoder.ProcessLine did. It is exported so the mapping can be tested
// directly.
func FatalLine(line string) error {
	switch {
	case strings.Contains(line, errPrefix), strings.Contains(line, errUnknownOpt):
		return okerr.New(okerr.KindTool, okerr.ErrX264.Summary, "%s", line).WithOutput(line)
	case strings.Contains(line, errFwriteFailed):
		return okerr.New(okerr.KindToolCrash, okerr.ErrX264Crash.Summary,
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
