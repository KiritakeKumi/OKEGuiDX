// Package rpc runs the re-encode PSNR check.
//
// Behaviour is defined by JobProcessor/RpChecker/RpChecker.cs. The legacy code
// never invoked RPChecker.exe: it generated a `_rpc.vpy` from RpcTemplate.vpy
// and ran vspipe on it, parsing the `RPCOUT:` lines the template's callback
// prints (INVENTORY.md §5). This package does the same, minus the shell.
//
// Two deliberate deviations, both required to make the result useful:
//
//   - The legacy code passed `.` as the output file, which on Windows means
//     "write nothing". vspipe's stdout is redirected to the null device by the
//     process layer instead, which behaves identically on every platform.
//   - A non-zero vspipe exit becomes a structured error. The legacy code
//     ignored it and reported a "pass" whenever the run happened to end with
//     an "Output ..." line, so a crashed script could still be recorded as
//     passed.
package rpc

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// Name is the processor name used in logs and status events.
const Name = "rpc"

// PSNR thresholds, verbatim from RpChecker. `psnr_threashold` keeps the
// original typo so a search for either spelling finds this code.
const (
	// PSNRThreshold is the Y-plane minimum.
	PSNRThreshold = 30.0
	// PSNRUVThreshold is the U/V-plane minimum. It is also the placeholder
	// value a Y-only run stores for the two chroma planes.
	PSNRUVThreshold = 40.0
)

// RPCOUTPrefix is the marker the template's callback prints. RpChecker took
// line.Substring(8) after a Contains check, so the colon is part of the
// marker.
const RPCOUTPrefix = "RPCOUT:"

// Options configures one RPC check.
type Options struct {
	// VSPipe is the absolute path to vspipe.
	VSPipe string
	// Template is the RpcTemplate.vpy to fill in. Empty means
	// DefaultTemplatePath.
	Template string
	// SourceScript is the task's InputScript; it replaces OKE:SOURCE_SCRIPT.
	SourceScript string
	// RippedFile is the encoded video to compare against; it replaces
	// OKE:VIDEO_FILE and names the generated script.
	RippedFile string
	// VSPipeArgs are the profile's vspipe arguments, rendered into the script
	// as setattr clauses.
	VSPipeArgs []string
	// TotalFrames is the frame count of the encoded file, used for progress.
	TotalFrames int64
	// Output is the .rpc result path. When empty, the legacy default applies:
	// the failed-result path (OutputPathPrefix + ".rpc") on failure and the
	// source's extension replaced with `.rpc` on success.
	Output string
	// FailedOutput is OutputPathPrefix + ".rpc", used when the check fails.
	FailedOutput string
	// SourceFile is the task's source file, used to fill the "该文件X" part of
	// the legacy error message.
	SourceFile string
	// Priority is applied to the child process. The zero value selects
	// proc.DefaultPriority.
	Priority proc.Priority
}

// Processor runs one RPC check.
type Processor struct {
	opts Options

	mu       sync.Mutex
	proc     *proc.Process
	sink     jobproc.ProgressSink
	prog     jobproc.Progress
	status   model.RPCStatus
	frames   int64
	script   string
	result   Result
	frameErr error
	started  bool
}

// New returns a Processor for the given options.
func New(opts Options) *Processor {
	if opts.Template == "" {
		opts.Template = DefaultTemplatePath
	}
	if opts.Priority == 0 {
		opts.Priority = proc.DefaultPriority
	}
	return &Processor{opts: opts, status: model.RPCWaiting}
}

// Name implements jobproc.Processor.
func (p *Processor) Name() string { return Name }

// Status returns the RPC state. It is only meaningful after Run.
func (p *Processor) Status() model.RPCStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// Result returns the decoded samples and the path they were written to. It is
// only meaningful after Run has returned successfully.
func (p *Processor) Result() (Result, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	res := p.result
	res.Samples = append([]Sample(nil), p.result.Samples...)
	return res, p.outputPath()
}

// Progress returns the latest progress snapshot.
func (p *Processor) Progress() jobproc.Progress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.prog
}

// Run implements jobproc.Processor.
func (p *Processor) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if sink == nil {
		sink = jobproc.NopSink{}
	}
	p.mu.Lock()
	p.sink = sink
	p.mu.Unlock()

	script, err := WriteScript(p.opts.Template, p.opts.SourceScript, p.opts.RippedFile, p.opts.VSPipeArgs)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.script = script
	p.started = true
	p.mu.Unlock()

	// The legacy command line was `"<script>" .`, where `.` tells vspipe to
	// consume every frame without writing any of them.
	process, err := proc.Start(proc.Spec{
		Path:     p.opts.VSPipe,
		Args:     []string{script, "."},
		Priority: p.opts.Priority,
		Name:     "vspipe",
	})
	if err != nil {
		return err
	}
	_ = process.Stdin().Close()
	p.mu.Lock()
	p.proc = process
	p.mu.Unlock()

	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = process.Kill()
		case <-stopWatch:
		}
	}()

	// The template prints RPCOUT lines to stderr and vspipe writes its own
	// diagnostics there too, so only stderr is parsed; stdout is drained and
	// discarded because `.` makes it empty anyway.
	handler := p.lineHandler()
	runErr := process.FinishStderr(handler)
	if drainErr := drain(process.Stdout()); drainErr != nil {
		log.Debug("丢弃vspipe输出失败", "err", drainErr)
	}

	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "vspipe 已被终止")
	}
	if lineErr := p.takeFrameError(); lineErr != nil {
		return lineErr
	}
	if runErr != nil {
		return okerr.Wrap(runErr, okerr.KindTool, okerr.ErrRPC.Summary, "vspipe 异常退出").
			WithTool("vspipe", process.ExitCode()).
			WithOutput(process.RecentOutput()).
			WithFile(p.opts.SourceFile)
	}

	// A run that never reached the "Output " line still counts as passed, for
	// the same reason the legacy code did so: no frame was flagged.
	p.mu.Lock()
	if p.status == model.RPCWaiting {
		p.status = model.RPCPassed
	}
	p.mu.Unlock()

	return p.writeResult()
}

// Close implements jobproc.Processor.
func (p *Processor) Close() error {
	p.mu.Lock()
	child := p.proc
	p.mu.Unlock()
	if child != nil {
		return child.Close()
	}
	return nil
}

// Pause implements jobproc.Controllable.
func (p *Processor) Pause() error {
	p.mu.Lock()
	child := p.proc
	p.mu.Unlock()
	if child != nil {
		return child.Pause()
	}
	return nil
}

// Resume implements jobproc.Controllable.
func (p *Processor) Resume() error {
	p.mu.Lock()
	child := p.proc
	p.mu.Unlock()
	if child != nil {
		return child.Resume()
	}
	return nil
}

// SetPriority implements jobproc.Prioritizable.
func (p *Processor) SetPriority(priority jobproc.Priority) error {
	p.mu.Lock()
	child := p.proc
	p.mu.Unlock()
	if child == nil {
		return nil
	}
	return child.SetPriority(proc.Priority(priority))
}

// ScriptPath returns the path of the script the last Run generated.
func (p *Processor) ScriptPath() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.script
}

// outputPath applies the legacy naming.
//
// RpcJob derived the initial path from the source script
// (Path.ChangeExtension(sourceFile, "rpc")), and RpChecker.waitForFinish
// switched to the failed-result path unless the check had passed before
// inserting the status into the extension: `00001.rpc` becomes
// `00001-通过.rpc` or `00001-未通过.rpc`.
func (p *Processor) outputPath() string {
	status := p.status
	out := p.opts.Output
	if status != model.RPCPassed && p.opts.FailedOutput != "" {
		out = p.opts.FailedOutput
	}
	if out == "" {
		out = replaceExt(p.opts.SourceScript, ".rpc")
	}
	return strings.ReplaceAll(out, ".rpc", "-"+status.String()+".rpc")
}

// writeResult serializes the collected samples next to the encoded file.
//
// The choice of layout mirrors RpChecker.waitForFinish: a Y-only run (any
// two-field sample) writes RpcResult, everything else writes RpcResult3.
func (p *Processor) writeResult() error {
	p.mu.Lock()
	res := p.result
	res.Samples = append([]Sample(nil), p.result.Samples...)
	path := p.outputPath()
	p.mu.Unlock()

	// The legacy code never assigned FileNamePair on the YUV path, so it was
	// serialized as nulls; only the Y-only path carried the pair.
	yuv := res.YUV || len(res.Samples) == 0
	if yuv {
		res.FileNamePair = nil
	} else {
		res.FileNamePair = &FileNamePair{Src: p.opts.SourceScript, Opt: p.opts.RippedFile}
	}

	data, err := EncodeResult(&res, yuv)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入RPC结果", "%s: %v", path, err)
	}
	log.Info("RPC检查结束", "status", p.Status().String(), "samples", len(res.Samples), "file", path)
	return nil
}

// takeFrameError returns and clears the first error recorded by the parser.
func (p *Processor) takeFrameError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.frameErr
	p.frameErr = nil
	return err
}

// lineHandler builds the per-stream parser, mirroring RpChecker.ProcessLine.
func (p *Processor) lineHandler() proc.LineFunc {
	var (
		inVSError bool
		errorMsg  strings.Builder
	)
	return func(line string) error {
		switch {
		case strings.Contains(line, "Python exception: "):
			// The legacy code threw at the first traceback line that looked
			// like an exception summary and recorded the accumulated text.
			inVSError = true
			errorMsg.Reset()
			return nil

		case inVSError:
			if reTraceback.MatchString(line) {
				errorMsg.WriteString("\n")
				errorMsg.WriteString(line)
				p.fail(okerr.New(okerr.KindTool, okerr.ErrRPC.Summary, "%s",
					strings.TrimSpace(errorMsg.String())).
					WithOutput(strings.TrimSpace(errorMsg.String())).
					WithFile(p.opts.SourceFile))
				return nil
			}
			if line != "" {
				errorMsg.WriteString("\n")
				errorMsg.WriteString(line)
			}
			return nil

		case strings.Contains(line, RPCOUTPrefix):
			return p.parseRPCOUT(line)

		case strings.HasPrefix(line, "Output "):
			// vspipe prints this once every frame has been written. The legacy
			// code treated it as the end of a successful run.
			p.mu.Lock()
			if p.status == model.RPCWaiting {
				p.status = model.RPCPassed
			}
			p.mu.Unlock()
			return nil
		}
		return nil
	}
}

// parseRPCOUT handles one measurement line.
//
// A malformed line is logged and skipped rather than failing the run: the
// legacy code indexed strNumbers[1] unconditionally and crashed on such input,
// and dropping one unparseable sample cannot invalidate the measurements that
// did parse.
//
//nolint:nilerr // the swallow is deliberate; see the comment above
func (p *Processor) parseRPCOUT(line string) error {
	idx := strings.Index(line, RPCOUTPrefix)
	fields := strings.Fields(line[idx+len(RPCOUTPrefix):])
	if len(fields) < 2 {
		log.Debug("无法解析RPCOUT行", "line", line)
		return nil
	}
	frameNo, err := strconv.Atoi(fields[0])
	if err != nil {
		log.Debug("无法解析RPCOUT帧号", "line", line)
		return nil
	}
	psnr, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		log.Debug("无法解析RPCOUT数值", "line", line)
		return nil
	}

	sample := Sample{Index: frameNo, Value: psnr, ValueU: PSNRUVThreshold, ValueV: PSNRUVThreshold}
	yuv := len(fields) > 3
	if yuv {
		u, errU := strconv.ParseFloat(fields[2], 64)
		v, errV := strconv.ParseFloat(fields[3], 64)
		if errU != nil || errV != nil {
			log.Debug("无法解析RPCOUT色度数值", "line", line)
			return nil
		}
		sample.ValueU, sample.ValueV = u, v
	}

	p.mu.Lock()
	p.frames++
	p.result.Samples = append(p.result.Samples, sample)
	if yuv {
		p.result.YUV = true
	}
	// The legacy code compared a Y-only run's chroma planes against the
	// threshold placeholder, which always passes; the fields are kept at the
	// placeholder here so the comparison stays identical.
	if psnr < PSNRThreshold || sample.ValueU < PSNRUVThreshold || sample.ValueV < PSNRUVThreshold {
		p.status = model.RPCFailed
	}
	percent := -1.0
	if p.opts.TotalFrames > 0 {
		percent = 100.0 * float64(p.frames) / float64(p.opts.TotalFrames)
	}
	p.prog = jobproc.Progress{
		Percent:     percent,
		FramesDone:  p.frames,
		FramesTotal: p.opts.TotalFrames,
		Status:      "RPC中",
	}
	snapshot := p.prog
	sink := p.sink
	p.mu.Unlock()

	if sink != nil {
		sink.Report(snapshot)
	}
	return nil
}

// fail records the first parse failure. The legacy code threw out of the line
// handler, which the process layer turns into a read error; recording it and
// letting the run finish keeps the child from being killed mid-write.
func (p *Processor) fail(err error) {
	p.mu.Lock()
	if p.frameErr == nil {
		p.frameErr = err
	}
	p.mu.Unlock()
}

// drain consumes a stream that carries no data this processor needs.
func drain(r io.Reader) error {
	_, err := io.Copy(io.Discard, r)
	return err
}

// replaceExt swaps a path's extension, mirroring Path.ChangeExtension.
func replaceExt(path, ext string) string {
	if i := strings.LastIndexByte(path, '.'); i > strings.LastIndexAny(path, `/\`) {
		return path[:i] + ext
	}
	return path + ext
}

// Ensure the processor satisfies the frozen interfaces.
var (
	_ jobproc.Processor     = (*Processor)(nil)
	_ jobproc.Controllable  = (*Processor)(nil)
	_ jobproc.Prioritizable = (*Processor)(nil)
)
