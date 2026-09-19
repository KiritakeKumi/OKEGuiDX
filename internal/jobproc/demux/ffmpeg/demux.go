// Package ffmpeg demuxes with ffmpeg and ffprobe, the all-platform replacement
// for the Windows-only eac3to demuxer (PLAN.md §2.1).
//
// It has the same public interface as internal/jobproc/demux/eac3to — the same
// VolumeMeasurer, Options, Result and Processor — so the pipeline can pick an
// implementation from node.Capabilities without knowing which one it got.
//
// Two passes, exactly as the eac3to demuxer runs them:
//
//  1. analysis — `ffprobe -show_streams -show_chapters -of json` enumerates the
//     tracks; the JSON is mapped onto TrackInfo values, one per track.
//  2. extraction — one ffmpeg run per track, because ffmpeg can only write one
//     output file per run for the raw muxers this needs. eac3to does the same
//     job in a single invocation; splitting it costs a re-read of the source per
//     track but keeps every output independent, which is what makes the delay
//     compensation per-track expressible at all.
//
// eac3to applies each track's container delay itself, so its outputs are
// aligned to video t=0 and the muxer never needs a sync offset. This package
// reproduces that alignment explicitly: see delay.go.
package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// Name is the processor name used in logs and status events.
const Name = "ffmpeg"

// Status strings reported while the two passes run. They are the same texts the
// eac3to demuxer puts on the task, so the UI reads identically whichever
// implementation ran.
const (
	StatusAnalyze   = "轨道分析中"
	StatusExtract   = "抽取音轨中"
	StatusExtracted = "音轨已抽取"
)

// VolumeMeasurer measures the levels of an extracted audio file, the way
// FFmpegVolumeChecker did with ffmpeg's astats filter. It is declared here
// exactly as in internal/jobproc/demux/eac3to so that
// internal/jobproc/audio/volume.Checker satisfies both.
type VolumeMeasurer interface {
	// Measure returns the RMS level and the peak level in dB. Both are
	// negative in practice; -Inf means "silence".
	Measure(ctx context.Context, file string) (mean, max float64, err error)
}

// VolumeFunc adapts a function to VolumeMeasurer.
type VolumeFunc func(ctx context.Context, file string) (mean, max float64, err error)

// Measure implements VolumeMeasurer.
func (f VolumeFunc) Measure(ctx context.Context, file string) (mean, max float64, err error) {
	return f(ctx, file)
}

// Options configures one demux run.
//
// The field set mirrors internal/jobproc/demux/eac3to.Options; the eac3to
// binary path is replaced by the two ffmpeg paths.
type Options struct {
	// FFmpeg is the absolute path to the ffmpeg binary.
	FFmpeg string
	// FFprobe is the absolute path to the ffprobe binary. It is used for the
	// analysis pass.
	FFprobe string
	// SourceFile is the file to demux.
	SourceFile string
	// WorkingPathPrefix mirrors TaskProfile.WorkingPathPrefix. Only its
	// directory is used, for the output files.
	WorkingPathPrefix string
	// AudioTracks is TaskProfile.AudioTracks, in profile order. It is copied;
	// the caller's slice is never modified.
	AudioTracks []model.AudioInfo
	// SubtitleTracks is TaskProfile.SubtitleTracks, in profile order.
	SubtitleTracks []model.Info
	// SkipAllAudioTracks drops every audio track from the source listing
	// before the count check, mirroring TaskProfile.SkipAllAudioTracks.
	SkipAllAudioTracks bool
	// SkipAllSubtitleTracks drops every subtitle track from the source
	// listing before the count check.
	SkipAllSubtitleTracks bool
	// Volume measures extracted audio tracks. Nil disables empty-track
	// detection for audio.
	Volume VolumeMeasurer
	// Priority is applied to the child processes.
	Priority proc.Priority
}

// Result is what one demux run produced. It mirrors
// internal/jobproc/demux/eac3to.Result field for field.
type Result struct {
	// MediaFile holds the extracted tracks in muxing order, with the
	// DupOrEmpty and Length fields of their Info values filled in.
	MediaFile *model.MediaFile
	// Tracks are every track the listing reported, after the skip-all
	// filters, in listing order.
	Tracks []*TrackInfo
	// Extracted are the tracks that were written, in extraction order.
	// Tracks marked DupOrEmpty are still present; their files have been moved
	// aside by MarkSkipping.
	Extracted []*TrackInfo
	// Length is the container runtime in whole seconds, or 0 when the probe
	// reported none.
	Length int
	// LogPath is always "" here: ffmpeg writes no demuxer log of its own. The
	// field exists so the two implementations stay interchangeable.
	LogPath string
}

// Processor demuxes one source file with ffmpeg.
type Processor struct {
	opts Options

	mu      sync.Mutex
	current *proc.Process
	result  *Result
}

// The pipeline switches on capabilities, so the method set has to stay equal to
// the eac3to demuxer's.
var (
	_ jobproc.Processor     = (*Processor)(nil)
	_ jobproc.Controllable  = (*Processor)(nil)
	_ jobproc.Prioritizable = (*Processor)(nil)
)

// New returns a Processor for the given options.
func New(opts Options) *Processor {
	if opts.Priority == 0 {
		opts.Priority = proc.DefaultPriority
	}
	return &Processor{opts: opts}
}

// Name implements jobproc.Processor.
func (p *Processor) Name() string { return Name }

// Result returns the outcome of the last successful Run, or nil.
func (p *Processor) Result() *Result {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result
}

// Close implements jobproc.Processor. It kills a run that is still in flight.
func (p *Processor) Close() error {
	if cur := p.process(); cur != nil {
		_ = cur.Kill()
		_ = cur.Close()
	}
	return nil
}

// Pause implements jobproc.Controllable.
func (p *Processor) Pause() error {
	if cur := p.process(); cur != nil {
		return cur.Pause()
	}
	return nil
}

// Resume implements jobproc.Controllable.
func (p *Processor) Resume() error {
	if cur := p.process(); cur != nil {
		return cur.Resume()
	}
	return nil
}

// SetPriority implements jobproc.Prioritizable.
func (p *Processor) SetPriority(prio jobproc.Priority) error {
	if cur := p.process(); cur != nil {
		return cur.SetPriority(proc.Priority(prio))
	}
	return nil
}

func (p *Processor) process() *proc.Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

// Run performs both passes. It implements jobproc.Processor.
func (p *Processor) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if sink == nil {
		sink = jobproc.NopSink{}
	}
	if p.opts.FFmpeg == "" {
		return okerr.New(okerr.KindConfig, "找不到外部工具", "未指定 ffmpeg 路径")
	}
	if p.opts.FFprobe == "" {
		return okerr.New(okerr.KindConfig, "找不到外部工具", "未指定 ffprobe 路径")
	}
	if _, err := os.Stat(p.opts.SourceFile); err != nil {
		return okerr.Wrap(err, okerr.KindNotFound, "找不到输入文件", "%s", p.opts.SourceFile)
	}

	// Pass 1: analyse.
	sink.Report(jobproc.Progress{Status: StatusAnalyze})
	probe, err := p.probe(ctx)
	if err != nil {
		return err
	}

	tracks, err := probe.Tracks(p.opts)
	if err != nil {
		return err
	}
	if p.opts.SkipAllAudioTracks {
		tracks = dropType(tracks, model.TrackTypeAudio)
		log.Debug("Skip all audio tracks")
	}
	if p.opts.SkipAllSubtitleTracks {
		tracks = dropType(tracks, model.TrackTypeSubtitle)
		log.Debug("Skip all subtitle tracks")
	}

	// The count check against the profile runs after the skip-all filters, as
	// it did in the legacy code.
	jobAudio, jobSub, err := p.reconcile(tracks)
	if err != nil {
		return err
	}

	plan := p.planExtraction(tracks, jobAudio, jobSub)

	// Pass 2: extract.
	if err := p.extract(ctx, sink, plan); err != nil {
		return err
	}

	if err := p.measure(ctx, plan.tracks); err != nil {
		return err
	}

	media, err := p.buildMediaFile(plan.tracks, jobAudio, jobSub)
	if err != nil {
		return err
	}

	sink.Report(jobproc.Progress{Percent: 100, Status: StatusExtracted})

	p.mu.Lock()
	p.result = &Result{
		MediaFile: media,
		Tracks:    tracks,
		Extracted: plan.tracks,
		Length:    probe.Length(),
	}
	p.mu.Unlock()
	return nil
}

// probe runs ffprobe and parses its report. A failure is reported through the
// same structured shape the extraction pass uses, so the caller sees one error
// vocabulary whichever tool failed.
func (p *Processor) probe(ctx context.Context) (*Probe, error) {
	res, err := proc.Run(ctx, proc.Spec{
		Path:     p.opts.FFprobe,
		Args:     ProbeArgs(p.opts.SourceFile),
		Dir:      filepath.Dir(p.opts.WorkingPathPrefix),
		Priority: p.opts.Priority,
		Name:     Name + "-probe",
	})
	if err != nil {
		return nil, okerr.New(okerr.KindTool, okerr.ErrEac3to.Summary,
			"ffprobe 退出代码 %d", res.ExitCode).
			WithTool(Name, res.ExitCode).
			WithOutput(res.LastOutput).
			WithFile(p.opts.SourceFile)
	}
	probe, err := ParseProbe(res.Stdout)
	if err != nil {
		return nil, okerr.AsError(err).
			WithTool(Name, res.ExitCode).
			WithOutput(res.LastOutput).
			WithFile(p.opts.SourceFile)
	}
	return probe, nil
}

// dropType removes every track of the given type, preserving order.
func dropType(tracks []*TrackInfo, t model.TrackType) []*TrackInfo {
	out := make([]*TrackInfo, 0, len(tracks))
	for _, tr := range tracks {
		if tr.Type == t {
			continue
		}
		out = append(out, tr)
	}
	return out
}

// countType counts tracks of the given type.
func countType(tracks []*TrackInfo, t model.TrackType) int {
	n := 0
	for _, tr := range tracks {
		if tr.Type == t {
			n++
		}
	}
	return n
}

// reconcile performs the track-count check and prunes optional entries when the
// source matches only the required count. It returns copies: the caller's
// profile slices are never modified.
//
// This is byte-for-byte the behaviour of the eac3to demuxer's reconcile,
// because the count check is a property of the profile, not of the demuxer.
func (p *Processor) reconcile(tracks []*TrackInfo) ([]model.AudioInfo, []model.Info, error) {
	srcAudio := countType(tracks, model.TrackTypeAudio)
	srcSub := countType(tracks, model.TrackTypeSubtitle)

	jobAudio := append([]model.AudioInfo(nil), p.opts.AudioTracks...)
	jobSub := append([]model.Info(nil), p.opts.SubtitleTracks...)

	reqAudio := countOptional(jobAudio, func(a model.AudioInfo) bool { return a.Optional })
	if srcAudio != len(jobAudio) && srcAudio != reqAudio {
		return nil, nil, okerr.New(okerr.KindMismatch, okerr.ErrAudioNumMismatch.Summary,
			"当前的视频含有轨道数%d，与json中指定的数量%d必须+%d可选不符合",
			srcAudio, reqAudio, len(jobAudio)-reqAudio).
			WithFile(p.opts.SourceFile)
	}
	reqSub := countOptional(jobSub, func(s model.Info) bool { return s.Optional })
	if srcSub != len(jobSub) && srcSub != reqSub {
		return nil, nil, okerr.New(okerr.KindMismatch, okerr.ErrSubNumMismatch.Summary,
			"当前的视频含有字幕数%d，与json中指定的数量%d必须+%d可选不符合",
			srcSub, reqSub, len(jobSub)-reqSub).
			WithFile(p.opts.SourceFile)
	}
	if srcAudio != len(jobAudio) {
		jobAudio = dropOptional(jobAudio, func(a model.AudioInfo) bool { return a.Optional })
	}
	if srcSub != len(jobSub) {
		jobSub = dropOptional(jobSub, func(s model.Info) bool { return s.Optional })
	}
	return jobAudio, jobSub, nil
}

func countOptional[T any](specs []T, optional func(T) bool) int {
	n := 0
	for _, s := range specs {
		if !optional(s) {
			n++
		}
	}
	return n
}

func dropOptional[T any](specs []T, optional func(T) bool) []T {
	out := make([]T, 0, len(specs))
	for _, s := range specs {
		if !optional(s) {
			out = append(out, s)
		}
	}
	return out
}

// planExtraction reproduces the argument-building loop of the eac3to demuxer's
// Extract: every extractable track consumes one entry of the profile's audio or
// subtitle list, in listing order, *including* tracks whose MuxOption is Skip.
// A Lossy audio request downgrades every codec except EAC3 to FLAC, which also
// decides the output extension.
//
// Where eac3to takes one command line for all tracks, this builds one ffmpeg
// invocation per track; the *selection* of tracks is identical.
func (p *Processor) planExtraction(tracks []*TrackInfo, jobAudio []model.AudioInfo, jobSub []model.Info) *extraction {
	plan := &extraction{}
	audioID, subID := 0, 0
	for _, track := range tracks {
		if !track.extract() {
			continue
		}
		switch track.Type {
		case model.TrackTypeAudio:
			if audioID >= len(jobAudio) {
				return plan
			}
			spec := jobAudio[audioID]
			audioID++
			if spec.Mux == model.MuxOptionSkip {
				continue
			}
			if spec.Lossy && track.Codec != CodecEAC3 {
				track.Codec = CodecFLAC
			}
		case model.TrackTypeSubtitle:
			if subID >= len(jobSub) {
				return plan
			}
			spec := jobSub[subID]
			subID++
			if spec.Mux == model.MuxOptionSkip {
				continue
			}
		default:
			continue
		}
		plan.tracks = append(plan.tracks, track)
		steps := p.buildExtractArgs(track)
		if len(steps) == 0 {
			// outputFor rejected the codec, which extract() already filtered
			// out. Reaching here means the two disagree, and silently dropping
			// the track would leave the muxer with fewer files than it was
			// promised.
			log.Warn("无法为该轨道构造抽取命令，已跳过", "track", track.Index, "codec", track.Codec.String())
			plan.tracks = plan.tracks[:len(plan.tracks)-1]
			continue
		}
		plan.jobs = append(plan.jobs, extractJob{
			track: track,
			steps: steps,
			temps: tempFiles(track.OutFileName()),
		})
	}
	plan.duration = float64(probeLength(tracks))
	return plan
}

// probeLength picks the container runtime out of the track list, for progress
// reporting only.
func probeLength(tracks []*TrackInfo) int {
	for _, t := range tracks {
		if t.Length > 0 {
			return t.Length
		}
	}
	return 0
}

// tempFiles lists the byproducts a job may leave behind.
func tempFiles(out string) []string {
	return []string{
		tempPath(out, ".body"),
		tempPath(out, ".pad"),
		tempPath(out, ".concat"),
	}
}

// tempPath returns a byproduct path next to the target, so it lands on the same
// volume as the output and never crosses a mount point.
func tempPath(target, suffix string) string {
	return target + suffix
}

// measure stats every extracted file and runs the volume checker on audio.
func (p *Processor) measure(ctx context.Context, extracted []*TrackInfo) error {
	for _, track := range extracted {
		info, err := os.Stat(track.OutFileName())
		if err != nil {
			return okerr.Wrap(err, okerr.KindIO, okerr.ErrUnknown.Summary,
				"文件输出失败: %s", track.OutFileName())
		}
		if info.Size() == 0 {
			return okerr.New(okerr.KindIO, okerr.ErrUnknown.Summary,
				"文件输出失败: %s 是空文件", track.OutFileName())
		}
		track.FileSize = info.Size()
		if track.Type != model.TrackTypeAudio || p.opts.Volume == nil {
			continue
		}
		mean, max, err := p.opts.Volume.Measure(ctx, track.OutFileName())
		if err != nil {
			return okerr.Wrap(err, okerr.KindTool, okerr.ErrUnknown.Summary,
				"音量检测失败: %s", track.OutFileName())
		}
		track.MeanVolume = mean
		track.MaxVolume = max
	}
	return nil
}

// buildMediaFile reproduces the empty/duplicate sweep and the MediaFile
// assembly at the end of the eac3to demuxer's Extract.
func (p *Processor) buildMediaFile(extracted []*TrackInfo, jobAudio []model.AudioInfo, jobSub []model.Info) (*model.MediaFile, error) {
	removed := make(map[int]bool)
	for i, track := range extracted {
		if removed[track.Index] {
			continue
		}
		if track.IsEmpty() {
			log.Warn(track.OutFileName() + "被检测为空轨道。")
			removed[track.Index] = true
			track.MarkSkipping()
			continue
		}
		for _, other := range extracted[i+1:] {
			if !track.IsDuplicate(other) {
				continue
			}
			log.Warn(track.OutFileName() + "被检测为与" + other.OutFileName() + "重复。")
			removed[other.Index] = true
			other.MarkSkipping()
		}
	}

	media := model.NewMediaFile()
	audioID, subID := 0, 0
	for _, track := range extracted {
		ref := model.NewFileRef(track.OutFileName())
		switch track.Type {
		case model.TrackTypeAudio:
			if audioID >= len(jobAudio) {
				return nil, okerr.New(okerr.KindMismatch, okerr.ErrAudioNumMismatch.Summary,
					"音轨数不一致：封装第 %d 条音轨时配置已用尽", audioID+1)
			}
			spec := jobAudio[audioID]
			audioID++
			spec.SetDupOrEmpty(track.DupOrEmpty)
			spec.Length = track.Length
			if err := media.AddAudioTrack(&model.AudioTrack{
				Track: model.Track{
					File:      ref,
					Info:      spec.Info,
					TrackType: model.TrackTypeAudio,
				},
				Audio: spec,
			}); err != nil {
				return nil, err
			}
		case model.TrackTypeSubtitle:
			if subID >= len(jobSub) {
				return nil, okerr.New(okerr.KindMismatch, okerr.ErrSubNumMismatch.Summary,
					"字幕数不一致：封装第 %d 条字幕时配置已用尽", subID+1)
			}
			spec := jobSub[subID]
			subID++
			spec.SetDupOrEmpty(track.DupOrEmpty)
			if err := media.AddSubtitleTrack(&model.SubtitleTrack{
				Track: model.Track{
					File:      ref,
					Info:      spec,
					TrackType: model.TrackTypeSubtitle,
				},
			}); err != nil {
				return nil, err
			}
		case model.TrackTypeChapter:
			// The chapter track that reaches the container comes from
			// ChapterService, not from the demuxer. ffmpeg writes a text
			// chapter file only when asked to; the probe pass never asks.
			log.Debug("章节轨道不由 demuxer 输出", "file", track.OutFileName())
		default:
			log.Warn("不认识的轨道", "file", track.OutFileName(), "type", track.Type.String())
		}
	}
	log.Debug("extract audio number", "audio", audioID, "sub", subID)
	return media, nil
}

// run starts one child process, registers it for pause/resume/priority, waits
// for the context and drains both streams.
func (p *Processor) run(ctx context.Context, name string, args []string, onOut, onErr proc.LineFunc) (*proc.Process, error) {
	cur, err := proc.Start(proc.Spec{
		Path:     p.opts.FFmpeg,
		Args:     args,
		Dir:      filepath.Dir(p.opts.WorkingPathPrefix),
		Priority: p.opts.Priority,
		Name:     name,
	})
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.current = cur
	p.mu.Unlock()

	_ = cur.Stdin().Close()

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = cur.Kill()
		case <-stop:
		}
	}()

	finishErr := cur.Finish(onOut, onErr)

	p.mu.Lock()
	p.current = nil
	p.mu.Unlock()
	return cur, finishErr
}

// toolError turns a failed child into a structured error, mirroring the shape
// the eac3to demuxer produces for its binary.
//
// Both branches carry okerr.ErrEac3to.Summary, exactly as the eac3to demuxer
// does, so okerr.Render produces the operator-facing template the pipeline
// already knows regardless of which implementation ran.
func (p *Processor) toolError(ctx context.Context, cur *proc.Process, finishErr error) error {
	code := cur.ExitCode()
	recent := cur.RecentOutput()
	if e := okerr.AsError(finishErr); e != nil && e.Kind != okerr.KindUnknown {
		return finishErr
	}
	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", Name)
	}
	if finishErr != nil {
		return okerr.Wrap(finishErr, okerr.KindTool, okerr.ErrEac3to.Summary,
			"ffmpeg 异常退出").
			WithTool(Name, code).
			WithOutput(recent).
			WithFile(p.opts.SourceFile)
	}
	return okerr.New(okerr.KindTool, okerr.ErrEac3to.Summary,
		"ffmpeg 退出代码 %d", code).
		WithTool(Name, code).
		WithOutput(recent).
		WithFile(p.opts.SourceFile)
}

// Close releases a child that failed to be registered.
func (p *Processor) closeChild(cur *proc.Process) {
	if cur == nil {
		return
	}
	_ = cur.Close()
}

// String implements fmt.Stringer for diagnostics.
func (p *Processor) String() string {
	return fmt.Sprintf("%s(%s)", Name, p.opts.SourceFile)
}
