// Package svtav1 wraps the SVT-AV1 encoder (SvtAv1EncApp).
//
// Behaviour is defined by JobProcessor/Video/SVTAV1Encoder.cs. Three things set
// SVT-AV1 apart from the x264/x265 wrappers:
//
//   - It reads raw video from stdin with "-i -" and writes the bitstream with
//     "-b <file>", while the base class appends "-o <file>". In SVT-AV1 "-o" is
//     the *reconstructed* YUV path, not the bitstream, so the recon write is
//     aimed at the null device. See ReconOutput for why that is the only shape
//     that both satisfies the base class and produces the right file.
//
//   - Its progress records are carriage-return terminated, and "-i -" also
//     means the encoder cannot know the frame count up front. The base class
//     scans lines with bufio.Scanner, which splits on "\n" only, so an entire
//     run of progress records arrives as one scanned line. The parser therefore
//     searches within a line instead of assuming one record per line.
//
//   - Newer builds colour their progress output unconditionally, even when
//     stderr is redirected. The C# patterns predate that, so escape sequences
//     are stripped before matching; without this the modern dialect would not
//     be recognised at all.
package svtav1

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// Name is the processor name used in logs and status events.
const Name = "svtav1"

// ReconOutput is the value passed to the app's "-o" flag.
//
// The base class always appends "-o <Output>" after the encoder's own
// arguments, so the flag cannot be dropped. In SVT-AV1 "-o" is the
// *reconstructed* YUV path (OUTPUT_RECON_TOKEN in Source/App/app_config.c)
// while the bitstream goes to "-b". Pointing "-o" at the profile's real output
// path is not an option: the app cannot open a file that "-b" is already
// writing and aborts with "Error: Invalid parameter '-o' with value '...'".
// The null device keeps the flag harmless, and the bitstream is byte-identical
// to a run without it (verified against SvtAv1EncApp v4.2.0).
const ReconOutput = "NUL"

// Progress patterns.
//
// reLegacy is SVTAV1Encoder.ProcessLine's regOfficial, reproduced verbatim
// (including the "fp[sm]" unit, which older builds use to report frames per
// minute for slow encodes). It covers the v1.x-v2.x dialect:
//
//	Encoding frame   42 4795.06 kbps 3.91 fps
//
// reModern covers the v3.x+ dialect, which v4.2.0 - the version
// OKEGuiDX-tools' versions.lock pins - prints:
//
//	Encoding: 42/324 Frames @ 3.91 fps | 4795.06 kb/s | Size: ...
//
// The "N" and "N/M" forms are the same pattern; the app drops the total when it
// cannot know it, which is exactly the case when reading a pipe. The speed and
// bitrate part is optional so that a truncated record still yields a frame
// count.
var (
	reLegacy = regexp.MustCompile(`(?i)Encoding frame *([0-9]+) *([0-9]+(?:\.[0-9]+)?) *kbps *([0-9]+(?:\.[0-9]+)?) *(fp[sm])`)

	reModern = regexp.MustCompile(`(?i)Encoding: *([0-9]+)(?:/[0-9]+)? *Frames? *@ *(?:([0-9]+(?:\.[0-9]+)?) *fps *\| *([0-9]+(?:\.[0-9]+)?) *kb/s)?`)

	// reDone is the C# "all_done_encoding *([0-9]+) frames" pattern. SVT-AV1
	// only prints that line when built with -DLOG_ENC_DONE=1, which the tools
	// recipe does not enable, so it is a fallback rather than the usual signal.
	reDone = regexp.MustCompile(`(?i)all_done_encoding *([0-9]+) frames`)

	// reSummary is the C# "[\t ]*([0-9]+)[\t ]" used on the summary table's
	// first row.
	reSummary = regexp.MustCompile(`[\t ]*([0-9]+)[\t ]`)

	// reANSI strips the SGR sequences the app emits even when stderr is not a
	// terminal. Colour is what keeps the C# patterns from matching modern
	// builds, so it has to go before any of the above are tried.
	reANSI = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
)

// Fatal output patterns, taken from SVTAV1Encoder.ProcessLine.
const (
	errSvtPrefix  = "Svt[error]: "
	errSVTPrefix  = "[SVT-Error]: "
	errGeneric    = "Error: "
	errFwriteFail = "Error: fwrite() call failed when writing frame: "
)

// Options configures an SVT-AV1 run.
type Options struct {
	// VSPipe is the absolute path to vspipe.
	VSPipe string
	// Script is the .vpy path.
	Script string
	// VSPipeArgs are extra vspipe --arg values.
	VSPipeArgs []string
	// Encoder is the absolute path to SvtAv1EncApp.
	Encoder string
	// Params is the profile's EncoderParam string. It is passed through
	// unchanged, mirroring how cmd.exe used to hand it to the process.
	Params string
	// Output is the destination .ivf file.
	Output string
	// TotalFrames is the expected frame count, used for the percentage and for
	// the truncated-encode check. SVT-AV1 cannot report it itself when reading
	// from a pipe.
	TotalFrames int64
	// FrameStart and FrameEnd bound a partial (re-encode) run. FrameEnd < 0
	// means "to the end".
	FrameStart int64
	FrameEnd   int64
}

// Processor encodes with SVT-AV1.
type Processor struct {
	*video.Base
	opts Options
	// expectTotalFrames mirrors SVTAV1Encoder's field of the same name: the
	// summary table's first row is only read when its header preceded it.
	expectTotalFrames bool
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
		Output:      ReconOutput,
		FrameStart:  opts.FrameStart,
		FrameEnd:    opts.FrameEnd,
		TotalFrames: opts.TotalFrames,
		StdinArg:    "-i",
	})
	p.SetParser(video.ParseFunc(p.parse))
	p.SetFatalLine(fatalLine)
	return p
}

// buildArgs assembles the SVT-AV1 arguments, in the order the C# built them:
// the progress flag, then the profile parameters, then the bitstream path.
// The base class appends "-i", its value and "-o" after these.
//
//   - "--progress 2" is what makes the app print per-frame progress at all;
//     the profile's EncoderParam cannot be relied on to carry it.
//   - "-b <Output>" is the bitstream.
func buildArgs(opts Options) []string {
	params := splitParams(opts.Params)
	args := make([]string, 0, 4+len(params))
	args = append(args, "--progress", "2")
	args = append(args, params...)
	args = append(args, "-b", opts.Output)
	return args
}

// splitParams splits an encoder parameter string into arguments, honouring
// double quotes so that values containing spaces survive. It mirrors the x265
// wrapper, where the same routine stands in for cmd.exe tokenisation.
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

// parse extracts progress from one line of encoder output.
//
// A scanned line may carry many progress records, because the app separates
// them with "\r" and the scanner only splits on "\n". Every record in the line
// is consumed and the last one wins, which is the newest frame count.
func (p *Processor) parse(line string) (jobproc.Progress, bool, error) {
	line = stripANSI(line)

	// A finished encode. The reported count is authoritative: it is what
	// EncodeFinish used to compare against the expected frame count, and what
	// the base class' truncated-encode check reads.
	if m := reDone.FindStringSubmatch(line); m != nil {
		return p.finish(m[1]), true, nil
	}

	// The summary table's header arms the next line, exactly as the C# did.
	if strings.HasPrefix(line, "Total Frames\t") {
		p.expectTotalFrames = true
		return jobproc.Progress{}, false, nil
	}
	if p.expectTotalFrames {
		p.expectTotalFrames = false
		// The C# split on "[\t ]*([0-9]+)[\t ]" and took the first group.
		if m := reSummary.FindStringSubmatch(line); m != nil {
			return p.finish(m[1]), true, nil
		}
	}

	if m := lastSubmatch(reModern, line); m != nil {
		return p.progress(m[1], m[2], m[3]), true, nil
	}

	if m := lastSubmatch(reLegacy, line); m != nil {
		return p.progress(m[1], speedInFPS(m[3], m[4]), m[2]), true, nil
	}
	return jobproc.Progress{}, false, nil
}

// lastSubmatch returns the *final* match of re in line, or nil. A scanned line
// can hold a whole run of progress records, and only the last one describes the
// current state; taking the first would freeze the reported frame count at the
// first record of every line.
func lastSubmatch(re *regexp.Regexp, line string) []string {
	all := re.FindAllStringSubmatch(line, -1)
	if len(all) == 0 {
		return nil
	}
	return all[len(all)-1]
}

// progress builds a mid-encode update. Empty speed or bitrate leave the
// previous value in place, which is what the base class' merge expects.
func (p *Processor) progress(frames, fps, kbps string) jobproc.Progress {
	done, _ := strconv.ParseInt(frames, 10, 64)
	prog := jobproc.Progress{
		FramesDone:  done,
		FramesTotal: p.opts.TotalFrames,
		Percent:     percent(done, p.opts.TotalFrames),
		Status:      "压制中",
	}
	if fps != "" {
		prog.Speed = fps + " fps"
	}
	if kbps != "" {
		prog.BitRate = kbps + " kb/s"
	}
	return prog
}

// finish builds the terminal update for a completed encode.
func (p *Processor) finish(frames string) jobproc.Progress {
	done, _ := strconv.ParseInt(frames, 10, 64)
	return jobproc.Progress{
		FramesDone:  done,
		FramesTotal: p.opts.TotalFrames,
		Percent:     100,
		Status:      "压制完成",
	}
}

// speedInFPS converts the C# speed/unit pair into a formatted fps string.
// SetSpeed divided by 60 when the unit was "fpm", because slow encodes report
// frames per minute; the reported figure is always in fps.
func speedInFPS(value, unit string) string {
	fps, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return ""
	}
	if strings.EqualFold(unit, "fpm") {
		fps /= 60
	}
	return strconv.FormatFloat(fps, 'f', 2, 64)
}

// fatalLine turns SVT-AV1's own error messages into structured errors, exactly
// as SVTAV1Encoder.ProcessLine did.
//
// The C# tested for "Error: " before the fwrite pattern, so the fwrite case was
// unreachable there and a disk-full failure was reported as ErrSVTAV1 rather
// than ErrSVTAV1Crash. The fwrite test is kept ahead of the generic one here so
// that the crash summary is actually reachable.
func fatalLine(line string) error {
	line = stripANSI(line)
	switch {
	case strings.Contains(line, errFwriteFail):
		return okerr.New(okerr.KindToolCrash, okerr.ErrSVTAV1Crash.Summary,
			"写入帧失败，磁盘可能已满").WithOutput(line)
	case strings.Contains(line, errSvtPrefix),
		strings.Contains(line, errSVTPrefix),
		strings.Contains(line, errGeneric):
		return okerr.New(okerr.KindTool, okerr.ErrSVTAV1.Summary, "%s", line).WithOutput(line)
	}
	return nil
}

func stripANSI(s string) string {
	if !strings.Contains(s, "\x1b") {
		return s
	}
	return reANSI.ReplaceAllString(s, "")
}

func percent(done, total int64) float64 {
	if total <= 0 {
		return -1
	}
	return float64(done) / float64(total) * 100
}
