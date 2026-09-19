// Package iframe collects the I-frame index of an old release for re-encoding.
//
// Behaviour is defined by JobProcessor/Video/IFrameInfoGenerator.cs: a small
// .vpy script loads the old file through LSMASH with framelist=True, prints the
// frame's _IFrameList property to stderr, and `vspipe --info` runs it. The
// resulting index drives the re-encode slicer, so the slicer's alignment
// helpers (FindNearestLeft, FindNearestRight, FindInRangeIndex) live here too.
package iframe

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// Name is the processor name used in logs and status events.
const Name = "iframe"

// ScriptSuffix is appended to the working path to name the generated script,
// mirroring the legacy `WorkingPath + "_iframe.vpy"`.
const ScriptSuffix = "_iframe.vpy"

// The generated script and the parser share two markers: the frame property
// LSMASH fills in, and the prefix of the line the script prints it on.
const (
	IFrameProperty = "_IFrameList"
	IFrameMarker   = "IFrameList"
)

// Output patterns, taken verbatim from IFrameInfoGenerator.ProcessLine. The
// original compiled them once per line; they are hoisted here.
//
// The list pattern accepts an empty list, which the original's `+` did not.
// LSMASH reports `[]` when a stream has no random access point at all, and the
// original would have crashed indexing split[1] instead of falling back to the
// 0..NumberOfFrames bracket it adds anyway.
var (
	reIFrame    = regexp.MustCompile(`IFrameList: \[([0-9, ]*)\]`)
	reFrames    = regexp.MustCompile(`Frames: ([0-9]+)`)
	reTraceback = regexp.MustCompile(`^[a-zA-Z_.]*(Error|Exception|Exit|Interrupt|Iteration|Warning)(.*)`)
)

// scriptTemplate is the .vpy the processor runs: byte for byte what the legacy
// PrepareScript wrote. The source path is wrapped in an R"..." literal so that
// Windows backslashes survive; nothing else is escaped, exactly as before.
const scriptTemplate = "import sys\n" +
	"from vapoursynth import core\n" +
	"a=R\"%s\"\n" +
	"src = core.lsmas.LWLibavSource(a, cache=0, framelist=True)\n" +
	"src.text.FrameProps(\"" + IFrameProperty + "\").set_output(0)\n" +
	"print(f\"" + IFrameMarker + ": {src.get_frame(0).props." + IFrameProperty + "}\", file=sys.stderr)\n"

// ScriptContent renders the .vpy that extracts the I-frame list of oldFile.
func ScriptContent(oldFile string) string {
	return fmt.Sprintf(scriptTemplate, oldFile)
}

// ScriptPath returns where the generated script is written for a working path.
// The legacy code concatenated the two strings, so the caller's path is used
// as-is and is expected to be a prefix such as `...\ep01`.
func ScriptPath(workingPath string) string {
	return workingPath + ScriptSuffix
}

// Options configures one I-frame extraction run.
type Options struct {
	// VSPipe is the absolute path to vspipe.
	VSPipe string
	// OldFile is the old release to inspect. It mirrors VideoInfoJob's
	// ReEncodeOldFile and is baked into the generated script.
	OldFile string
	// WorkingPath is the prefix the script is named after; the legacy job set
	// it to the profile's WorkingPathPrefix.
	WorkingPath string
	// NumberOfFrames is the frame count of the script that will be encoded.
	// It brackets the I-frame list and is the upper bound the old release must
	// not exceed.
	NumberOfFrames int64
}

// Processor extracts the I-frame index of an old release.
type Processor struct {
	opts Options

	mu      sync.Mutex
	iframes IFrameInfo
	gotList bool
}

// New returns a Processor for the given options.
func New(opts Options) *Processor { return &Processor{opts: opts} }

// Name implements jobproc.Processor.
func (p *Processor) Name() string { return Name }

// IFrames returns the collected I-frame index. It is only meaningful after Run
// has returned successfully.
func (p *Processor) IFrames() IFrameInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append(IFrameInfo(nil), p.iframes...)
}

// Run implements jobproc.Processor.
//
// The sink is ignored: the step is a one-shot probe that produces no progress
// worth reporting, and the legacy processor reported none either.
func (p *Processor) Run(ctx context.Context, _ jobproc.ProgressSink) error {
	script := ScriptPath(p.opts.WorkingPath)
	if err := os.WriteFile(script, []byte(ScriptContent(p.opts.OldFile)), 0o600); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入vpy脚本", "%s: %v", script, err)
	}

	// The legacy command line was `--info "<script>"`, with no output file.
	// vspipe R42 rejects that outright ("No output file specified"), so the
	// explicit `-` that the vspipeinfo processor also passes is used instead;
	// --info exits before any frame would be written to it.
	process, err := proc.Start(proc.Spec{
		Path:     p.opts.VSPipe,
		Args:     []string{"--info", script, "-"},
		Priority: proc.DefaultPriority,
		Name:     "vspipe",
	})
	if err != nil {
		return err
	}
	_ = process.Stdin().Close()

	// Cancellation has to kill the child; Finish blocks on its pipes.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Kill()
		case <-stopWatch:
		}
	}()

	// Both streams carry what the parser needs: vspipe writes the script's
	// properties to stdout, while the script's own print goes to stderr. Each
	// stream gets its own handler because the traceback state is per stream;
	// sharing one would let a stdout traceback absorb stderr lines.
	runErr := process.Finish(p.lineHandler(), p.lineHandler())

	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "vspipe 已被终止")
	}
	if runErr != nil {
		if e := okerr.AsError(runErr); e.Kind != okerr.KindUnknown {
			return e
		}
		return okerr.Wrap(runErr, okerr.KindTool, okerr.ErrVpy.Summary, "vspipe 异常退出").
			WithTool("vspipe", process.ExitCode()).
			WithOutput(process.RecentOutput())
	}

	p.mu.Lock()
	gotList := p.gotList
	count := len(p.iframes)
	p.mu.Unlock()
	if !gotList {
		return okerr.New(okerr.KindTool, okerr.ErrVpy.Summary,
			"vspipe 没有输出 I 帧列表，旧版成品可能无法解码")
	}
	log.Debug("获取I帧列表完成", "count", count, "file", p.opts.OldFile)
	return nil
}

// Close implements jobproc.Processor.
func (p *Processor) Close() error { return nil }

// lineHandler builds a per-stream parser.
//
// The state machine mirrors the original ProcessLine: a line containing
// "Python exception: " starts collecting a traceback, and the traceback ends at
// the first line that looks like an exception summary. The collected text
// becomes the error detail.
func (p *Processor) lineHandler() proc.LineFunc {
	var (
		inError bool
		buf     strings.Builder
	)
	return func(line string) error {
		switch {
		case strings.Contains(line, "Python exception: "):
			inError = true
			buf.Reset()
			return nil

		case inError:
			if reTraceback.MatchString(line) {
				buf.WriteString("\n")
				buf.WriteString(line)
				detail := strings.TrimSpace(buf.String())
				return okerr.New(okerr.KindTool, okerr.ErrVpy.Summary, "%s", detail).
					WithOutput(detail)
			}
			if line != "" {
				buf.WriteString("\n")
				buf.WriteString(line)
			}
			return nil

		// "Frames" is tested before "IFrameList", as in the original.
		case strings.Contains(line, "Frames"):
			return p.checkFrames(line)

		case strings.Contains(line, IFrameMarker):
			nums, ok := parseIFrameList(line)
			if !ok {
				// The original indexed split[1] unconditionally and crashed on
				// a line that merely mentions the marker; skipping is safer and
				// cannot lose a well-formed list.
				log.Debug("无法解析I帧列表行", "line", line)
				return nil
			}
			p.addIFrames(nums)
			return nil
		}
		return nil
	}
}

// checkFrames applies the frame count guard: the old release must not be longer
// than the script that is being encoded, and a shorter one is unusual enough to
// warn about.
func (p *Processor) checkFrames(line string) error {
	m := reFrames.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	f, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		// The regex guarantees a digit run, so this can only be an overflow.
		// The legacy TryParse left 0 in that case, and 0 is then compared
		// against the script's frame count like any other value.
		f = 0
	}

	log.Debug("旧版压制成品帧数", "frames", f)
	if f > p.opts.NumberOfFrames {
		return okerr.New(okerr.KindMismatch, okerr.ErrReEncodeFrames.Summary,
			"脚本输出帧数为%d，但旧版压制成品帧数为%d", p.opts.NumberOfFrames, f)
	}
	if f != p.opts.NumberOfFrames {
		log.Warn("旧版压制成品帧数与压制脚本输出帧数不匹配，这不是ReEncode的标准用法，除非你非常清楚自己在做什么",
			"old", f, "script", p.opts.NumberOfFrames)
	}
	return nil
}

// addIFrames appends a parsed list and applies the original bracket: frame 0
// and the script's frame count are always part of the index.
func (p *Processor) addIFrames(nums []int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, w := range nums {
		if w >= 0 {
			p.iframes = append(p.iframes, w)
		}
	}
	if !p.iframes.Contains(0) {
		p.iframes = append(IFrameInfo{0}, p.iframes...)
	}
	if !p.iframes.Contains(p.opts.NumberOfFrames) {
		p.iframes = append(p.iframes, p.opts.NumberOfFrames)
	}
	p.gotList = true
}

// parseIFrameList extracts the numbers from an "IFrameList: [...]" line. The
// second result is false when the line is not a well-formed list line.
func parseIFrameList(line string) ([]int64, bool) {
	m := reIFrame.FindStringSubmatch(line)
	if m == nil {
		return nil, false
	}
	fields := strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' })
	nums := make([]int64, 0, len(fields))
	for _, s := range fields {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			// Mirrors long.TryParse leaving -1, which the original skipped.
			continue
		}
		nums = append(nums, v)
	}
	return nums, true
}

// Ensure the processor satisfies the frozen interface.
var _ jobproc.Processor = (*Processor)(nil)
