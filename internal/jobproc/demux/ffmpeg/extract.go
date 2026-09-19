package ffmpeg

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// Extraction.
//
// eac3to takes one command line that names every output ("3:out.flac 4:out.sup
// ..."). ffmpeg has no equivalent: each of its raw muxers writes exactly one
// stream to one file, so the plan becomes one invocation per track. That split
// is what makes per-track delay compensation expressible at all, and it costs a
// re-read of the source per track — the same price eac3to pays, since it also
// walks the source once per track.
//
// A track that needs a positive delay and can be stream-copied needs three
// invocations (body, silent pad, join), because a raw stream copy cannot take a
// filter and a raw muxer has nowhere to store an offset.

// extraction is one ffmpeg invocation per track, plus the tracks it writes.
type extraction struct {
	jobs []extractJob
	// tracks are the tracks the plan writes, in extraction order.
	tracks []*TrackInfo
	// duration is the source runtime in seconds, for progress reporting.
	duration float64
}

// extractJob is one output file's worth of work.
type extractJob struct {
	track *TrackInfo
	// steps are the ffmpeg invocations, in order. The last one writes the
	// track's output file.
	steps []extractStep
	// temps are intermediate files to remove once the job is done.
	temps []string
}

// extractStep is one ffmpeg invocation.
type extractStep struct {
	args []string
	// list is a concat-demuxer file list to write before the step runs. It is
	// nil for every step that does not join.
	list *concatList
}

// concatList is the concat demuxer's input list.
type concatList struct {
	path    string
	entries []string
}

// extract runs the plan, reporting progress across the whole plan.
func (p *Processor) extract(ctx context.Context, sink jobproc.ProgressSink, plan *extraction) error {
	total := len(plan.jobs)
	if total == 0 {
		return nil
	}
	span := 100 / float64(total)
	for i, job := range plan.jobs {
		base := float64(i) * span
		if msg := delayStatus(job.track); msg != "" {
			log.Debug(msg, "track", job.track.Index, "file", job.track.OutFileName())
		}
		if msg := subtitleWarning(job.track); msg != "" {
			log.Warn(msg, "track", job.track.Index, "file", job.track.OutFileName())
		}
		if err := p.runJob(ctx, sink, job, base, span, plan.duration); err != nil {
			removeAll(job.temps)
			return err
		}
		removeAll(job.temps)
	}
	return nil
}

// runJob executes one track's steps.
func (p *Processor) runJob(ctx context.Context, sink jobproc.ProgressSink, job extractJob, base, span, duration float64) error {
	for i, step := range job.steps {
		if step.list != nil {
			if err := writeConcatList(step.list.path, step.list.entries); err != nil {
				return err
			}
		}
		// Only the step that reads the whole source carries meaningful
		// progress; the pad is generated instantly and the join is a copy.
		onOut := p.progressHandler(sink, base, span, duration, i == 0)
		if err := p.runStep(ctx, step, onOut); err != nil {
			return err
		}
	}
	sink.Report(jobproc.Progress{Percent: base + span, Status: StatusExtract})
	return nil
}

// runStep starts one ffmpeg invocation and drains it.
func (p *Processor) runStep(ctx context.Context, step extractStep, onOut proc.LineFunc) error {
	cur, finishErr := p.run(ctx, Name, step.args, onOut, nil)
	if cur == nil {
		return finishErr
	}
	defer p.closeChild(cur)

	if finishErr != nil || cur.ExitCode() != 0 {
		return p.toolError(ctx, cur, finishErr)
	}
	return nil
}

// progressHandler turns ffmpeg's -progress stream into jobproc progress.
//
// The stream is key=value lines, one block per update, terminated by
// "progress=continue" or "progress=end". out_time_us is the position in the
// output, which for a demux is the position in the source.
func (p *Processor) progressHandler(sink jobproc.ProgressSink, base, span, duration float64, enabled bool) proc.LineFunc {
	if !enabled {
		return nil
	}
	return func(line string) error {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil
		}
		switch strings.TrimSpace(key) {
		case "out_time_us":
			// An unreadable or absent total is not a failure: the progress
			// stream is best-effort, and the run's outcome does not depend on
			// it. parseProgressValue keeps the "cannot read" case out of an
			// error variable, which is what it is.
			us, ok := parseProgressValue(value)
			if !ok || duration <= 0 {
				return nil
			}
			frac := clamp01(us / 1e6 / duration)
			sink.Report(jobproc.Progress{
				Percent: base + span*frac,
				Status:  StatusExtract,
			})
		case "progress":
			if strings.TrimSpace(value) == "end" {
				sink.Report(jobproc.Progress{Percent: base + span, Status: StatusExtract})
			}
		}
		return nil
	}
}

// parseProgressValue reads one numeric field of ffmpeg's progress stream.
func parseProgressValue(v string) (float64, bool) {
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// clamp01 bounds a fraction to 0..1.
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// buildExtractArgs assembles the invocations for one track.
//
// The shapes are:
//
//	copied, no delay    -i src -map 0:N -c copy -f FMT out
//	copied, delay > 0   body + silent pad + concat, all stream-copied
//	copied, delay < 0   -i src -map 0:N -c copy -ss D -f FMT out
//	decoded             -i src -map 0:N -c:a ENC -af <delay filter> -f FMT out
//
// Subtitle tracks are always copied: their delay, if any, is carried by the
// container rather than by the samples.
func (p *Processor) buildExtractArgs(track *TrackInfo) []extractStep {
	out := track.OutFileName()
	muxer, encoder, ok := outputFor(*track)
	if !ok {
		return nil
	}
	switch {
	case isSubtitleCodec(track.Codec):
		return []extractStep{{args: p.subtitleArgs(track, muxer, out)}}
	case track.copyable():
		if steps, ok := p.copySteps(track, muxer, out); ok {
			return steps
		}
		// No copy form for this delay: the decoded path can always express
		// one.
		return []extractStep{{args: p.decodeArgs(track, muxer, encoder, out)}}
	default:
		return []extractStep{{args: p.decodeArgs(track, muxer, encoder, out)}}
	}
}

// inputArgs is the shared input and stream selection every shape starts with.
func (p *Processor) inputArgs(track *TrackInfo) []string {
	return []string{
		"-y", "-nostdin", "-v", "warning",
		"-i", p.opts.SourceFile,
		"-map", "0:" + strconv.Itoa(track.stream),
	}
}

// withOutput appends the progress reporting ffmpeg writes to stdout, then the
// destination path.
//
// The order matters: ffmpeg parses its arguments positionally, so an option
// after the output file is read as a second output rather than as an option of
// the first. The destination must therefore stay last.
func withOutput(args []string, out string) []string {
	return append(args, "-progress", "pipe:1", "-nostats", out)
}

// copySteps builds the stream-copy shapes, or ok=false when the delay cannot be
// expressed by copying.
func (p *Processor) copySteps(track *TrackInfo, muxer, out string) ([]extractStep, bool) {
	switch {
	case track.delay == 0:
		args := p.inputArgs(track)
		args = append(args, "-c", "copy", "-f", muxer)
		return []extractStep{{args: withOutput(args, out)}}, true

	case track.delay < 0:
		// A raw stream copy cannot take -af, so the head is dropped with an
		// output-side -ss. Output-side is the only reliable spelling here:
		// measured against real streams, an input-side -ss before -i cut the
		// wrong amount (it followed the video's start rather than the
		// audio's). ffmpeg drops whole packets, which snaps the cut to a
		// compressed frame boundary: 32 ms for AC3, 10.7 ms for DTS. eac3to's
		// own frame surgery had exactly the same granularity.
		args := p.inputArgs(track)
		args = append(args,
			"-c", "copy",
			"-ss", formatSeconds(float64(-track.delay)/1000),
			"-f", muxer,
		)
		return []extractStep{{args: withOutput(args, out)}}, true

	default:
		// A positive delay needs real silence in front of the body. The pad
		// must use the same encoder parameters as the body or the join
		// produces garbage; padFor refuses when they are not known, and the
		// caller falls back to the decoded path.
		spec, ok := padFor(track, track.delay)
		if !ok {
			return nil, false
		}
		body := tempPath(out, ".body")
		pad := tempPath(out, ".pad")
		list := tempPath(out, ".concat")

		bodyArgs := p.inputArgs(track)
		bodyArgs = append(bodyArgs, "-c", "copy", "-f", muxer)
		bodyArgs = withOutput(bodyArgs, body)

		joinArgs := withOutput(concatArgs(spec, list), out)

		return []extractStep{
			{args: bodyArgs},
			{args: withOutput(padArgs(spec), pad)},
			{
				args: joinArgs,
				list: &concatList{path: list, entries: []string{pad, body}},
			},
		}, true
	}
}

// subtitleArgs builds the subtitle shape: a plain stream copy.
//
// ffmpeg's raw subtitle muxers subtract the container's start offset, which for
// every representable source shape is video t=0, so no shift is applied. See
// the note at the top of delay.go for the measurement behind that.
func (p *Processor) subtitleArgs(track *TrackInfo, muxer, out string) []string {
	args := p.inputArgs(track)
	args = append(args, "-c:s", "copy", "-f", muxer)
	return withOutput(args, out)
}

// decodeArgs builds the decoded shape: the delay is materialised by an audio
// filter, which is exact to the sample in both directions.
func (p *Processor) decodeArgs(track *TrackInfo, muxer, encoder, out string) []string {
	args := p.inputArgs(track)
	if encoder == "" {
		encoder = "flac"
	}
	args = append(args, "-c:a", encoder)
	if filter := audioFilter(track.delay, track.sampleRate); filter != "" {
		args = append(args, "-af", filter)
	}
	args = append(args, "-f", muxer)
	return withOutput(args, out)
}

// writeConcatList writes the concat demuxer's file list.
//
// Paths are written with forward slashes: the demuxer treats each entry as a URL,
// and a Windows backslash is not a separator there. The single-quote escaping is
// ffmpeg's own: a quote inside a path is written as '\” (close, escaped quote,
// reopen). Without it a path containing an apostrophe truncates the list and the
// join silently reads the wrong file.
func writeConcatList(path string, entries []string) error {
	var b strings.Builder
	for _, e := range entries {
		b.WriteString("file '")
		b.WriteString(strings.ReplaceAll(filepath.ToSlash(e), "'", `'\''`))
		b.WriteString("'\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return okerr.Wrap(err, okerr.KindIO, okerr.ErrUnknown.Summary,
			"无法写入 concat 列表: %s", path)
	}
	return nil
}

// removeAll deletes intermediate files, ignoring failures: they are byproducts
// and a stale one only costs disk space.
func removeAll(paths []string) {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			log.Debug("无法删除中间文件", "file", p, "err", err)
		}
	}
}
