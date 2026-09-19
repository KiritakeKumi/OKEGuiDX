// Package eac3to wraps the eac3to demuxer.
//
// Behaviour is defined by JobProcessor/Demuxer/EACDemuxer.cs and
// JobProcessor/Demuxer/TrackInfo.cs; the output shapes it recognises, the
// thresholds it applies and the file names it produces are reproduced verbatim
// here. SPEC.md in this directory is the written form of that behaviour and is
// the reference A15 (the ffmpeg demuxer) must match.
//
// Two passes, exactly as the legacy code ran them:
//
//  1. analysis — `eac3to "<source>"` lists the tracks; the listing is parsed
//     into TrackInfo values, one per track.
//  2. extraction — `eac3to "<source>" <index>:"<out>" ...` writes the tracks
//     the profile asks for. eac3to applies each track's container delay itself,
//     so the files that come out are already aligned to video t=0 and no
//     muxer-side sync offset is ever needed.
//
// This package is Windows-only in practice: it drives the closed-source eac3to
// binary. The ffmpeg implementation for other platforms lives in A15 and must
// behave identically (PLAN.md §2.1).
package eac3to

import (
	"context"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// Name is the processor name used in logs and status events.
const Name = "eac3to"

// Status strings reported while the two passes run. They are the texts
// ExecuteTaskService.ExtractSource put on the task, kept so the UI reads the
// same.
const (
	StatusAnalyze   = "轨道分析中"
	StatusExtract   = "抽取音轨中"
	StatusExtracted = "音轨已抽取"
)

// VolumeMeasurer measures the levels of an extracted audio file, the way
// FFmpegVolumeChecker did with ffmpeg's astats filter.
//
// The demuxer needs these numbers to detect silent tracks, so a run without a
// measurer can never report an audio track as empty. The ffmpeg implementation
// lives in jobproc/audio/volume; this package only defines the seam so that it
// stays testable without an ffmpeg binary.
type VolumeMeasurer interface {
	// Measure returns the RMS level and the peak level in dB. Both are
	// negative in practice; -Inf means "silence".
	Measure(ctx context.Context, file string) (mean, max float64, err error)
}

// VolumeFunc adapts a function to VolumeMeasurer.
type VolumeFunc func(ctx context.Context, file string) (mean, max float64, err error)

// Measure implements VolumeMeasurer.
func (f VolumeFunc) Measure(ctx context.Context, file string) (float64, float64, error) {
	return f(ctx, file)
}

// Options configures one demux run.
type Options struct {
	// EacPath is the absolute path to the eac3to binary. In a shipped
	// OKEGuiDX install this is eac3to-wrapper.exe, which sits next to the
	// real eac3to.exe and fixes its PGS-in-MKV bug (see SPEC.md §3).
	EacPath string
	// SourceFile is the file to demux.
	SourceFile string
	// WorkingPathPrefix mirrors TaskProfile.WorkingPathPrefix. Only its
	// directory is used, for the output files and for the eac3to log.
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
	// Priority is applied to the eac3to process.
	Priority proc.Priority
}

// Result is what one demux run produced.
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
	// Length is the container runtime in whole seconds, or 0 when the
	// listing carried none.
	Length int
	// LogPath is the full path of the eac3to log, or "" when none was
	// produced.
	LogPath string
}

// Processor demuxes one source file with eac3to.
type Processor struct {
	opts Options

	mu      sync.Mutex
	current *proc.Process
	result  *Result
}

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
	if p.opts.EacPath == "" {
		return okerr.New(okerr.KindConfig, "找不到外部工具", "未指定 eac3to 路径")
	}
	if _, err := os.Stat(p.opts.SourceFile); err != nil {
		return okerr.Wrap(err, okerr.KindNotFound, "找不到输入文件", "%s", p.opts.SourceFile)
	}

	workDir := filepath.Dir(p.opts.WorkingPathPrefix)
	logPath := filepath.Join(workDir, stem(p.opts.SourceFile)+".eac3to.log")
	hashName := fmt.Sprintf("%08X.eac3to.log", crc32.ChecksumIEEE([]byte(logPath)))

	// Pass 1: analyse. The legacy code read only stdout here; Finish drains
	// both streams, which removes the deadlock a chatty stderr would otherwise
	// cause.
	detector := NewDetector(p.opts.SourceFile, p.opts.WorkingPathPrefix)
	analyzeArgs := []string{p.opts.SourceFile, "-log=" + hashName, "-progressnumbers"}
	if err := p.runEac(ctx, analyzeArgs, workDir, hashName, func(line string) error {
		if err := detector.Feed(line); err != nil {
			return err
		}
		sink.Report(jobproc.Progress{Status: StatusAnalyze})
		return nil
	}, nil); err != nil {
		return err
	}

	tracks := detector.Tracks()
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

	// Pass 2: extract. eac3to writes its progress to stdout, the same stream
	// the analysis pass uses for the listing, so the handler goes on stdout.
	extractArgs := append([]string{p.opts.SourceFile}, plan.args...)
	extractArgs = append(extractArgs, "-log="+hashName, "-progressnumbers")
	if err := p.runEac(ctx, extractArgs, workDir, hashName, func(line string) error {
		prog, ok := ParseProgress(line)
		if !ok {
			return nil
		}
		switch {
		case prog.Completed:
			sink.Report(jobproc.Progress{Percent: 100, Status: StatusExtracted})
		case prog.Analyze:
			sink.Report(jobproc.Progress{Percent: prog.Percent, Status: StatusAnalyze})
		case prog.Percent > 0:
			sink.Report(jobproc.Progress{Percent: prog.Percent, Status: StatusExtract})
		}
		return nil
	}, nil); err != nil {
		return err
	}

	if err := p.measure(ctx, plan.tracks); err != nil {
		return err
	}

	media, err := p.buildMediaFile(plan.tracks, jobAudio, jobSub)
	if err != nil {
		return err
	}

	p.mu.Lock()
	p.result = &Result{
		MediaFile: media,
		Tracks:    tracks,
		Extracted: plan.tracks,
		Length:    detector.Length(),
		LogPath:   logPath,
	}
	p.mu.Unlock()
	return nil
}

// runEac starts eac3to with the given arguments and drains its output, then
// moves the hash-named log into place. onOut and onErr may be nil.
func (p *Processor) runEac(ctx context.Context, args []string, dir, hashName string, onOut, onErr proc.LineFunc) error {
	cur, err := proc.Start(proc.Spec{
		Path:     p.opts.EacPath,
		Args:     args,
		Dir:      dir,
		Priority: p.opts.Priority,
		Name:     Name,
	})
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.current = cur
	p.mu.Unlock()

	// eac3to needs no stdin; closing it keeps the child from waiting on a
	// console that is not there.
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
	code := cur.ExitCode()
	recent := cur.RecentOutput()
	_ = cur.Close()

	p.mu.Lock()
	p.current = nil
	p.mu.Unlock()

	// A handler error (an unrecognised track codec, say) is already a
	// structured error; pass it through unchanged.
	if e := okerr.AsError(finishErr); e != nil && e.Kind != okerr.KindUnknown {
		return finishErr
	}
	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", Name)
	}
	if finishErr != nil {
		return okerr.Wrap(finishErr, okerr.KindTool, okerr.ErrEac3to.Summary,
			"eac3to 异常退出").WithTool(Name, code).WithFile(p.opts.SourceFile)
	}
	if code != 0 {
		// The legacy code threw Constants.eac3toErrorSmr with the exit code in
		// the exception data, which ExceptionParser rendered through
		// Constants.eac3toErrorMsg.
		return okerr.New(okerr.KindTool, okerr.ErrEac3to.Summary,
			"eac3to 退出代码 %d", code).
			WithTool(Name, code).
			WithOutput(recent).
			WithFile(p.opts.SourceFile)
	}

	p.moveLog(dir, hashName)
	return nil
}

// moveLog renames the hash-named log eac3to wrote into its final name, the way
// StartEac did after WaitForExit. A missing hash file is not an error: eac3to
// before 3.49 and -nolog configurations write none.
func (p *Processor) moveLog(dir, hashName string) {
	if hashName == "" {
		return
	}
	final := filepath.Join(dir, stem(p.opts.SourceFile)+".eac3to.log")
	src := filepath.Join(dir, hashName)
	if _, err := os.Stat(src); err != nil {
		return
	}
	if _, err := os.Stat(final); err == nil {
		if rmErr := os.Remove(final); rmErr != nil {
			log.Warn("无法删除旧的 eac3to 日志", "file", final, "err", rmErr)
			return
		}
	}
	if err := os.Rename(src, final); err != nil {
		log.Warn("无法重命名 eac3to 日志", "from", src, "to", final, "err", err)
	}
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

// extraction is the second pass's argument list plus the tracks it writes.
type extraction struct {
	args   []string
	tracks []*TrackInfo
}

// planExtraction reproduces the argument-building loop of Extract.
//
// Every extractable track consumes one entry of the profile's audio or
// subtitle list, in listing order, *including* tracks whose MuxOption is Skip:
// the legacy code advanced the index before testing for Skip, so the profile
// lists are positional. A Lossy audio request downgrades every codec except
// EAC3 to FLAC, which also decides the output extension.
//
// Skipped tracks never reach the result, so the extracted tracks still line up
// one-to-one with the remaining profile entries when the MediaFile is built.
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
		// "N:" and the path are separate arguments on purpose: eac3to has its
		// own command-line splitter and reads "N:path" differently from
		// "N:" "path" when the path contains spaces. The legacy code passed the
		// combined form to the wrapper, which split it for the same reason.
		plan.args = append(plan.args, strconv.Itoa(track.Index)+":", track.OutFileName())
		plan.tracks = append(plan.tracks, track)
	}
	return plan
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
// assembly at the end of Extract.
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
			// The typed wrapper carries the codec metadata; AddTrack alone
			// would replace it with defaults.
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
			// ChapterService, not from eac3to. eac3to's own .txt is renamed to
			// the source's base name, and only when nothing sits there already.
			target := changeExt(p.opts.SourceFile, ".txt")
			if _, err := os.Stat(target); err == nil {
				log.Info("检测到单独准备的章节，不予添加")
				continue
			}
			if err := os.Rename(track.OutFileName(), target); err != nil {
				log.Warn("无法重命名章节文件", "from", track.OutFileName(), "to", target, "err", err)
			}
		default:
			log.Warn("不认识的轨道", "file", track.OutFileName(), "type", track.Type.String())
		}
	}
	log.Debug("extract audio number", "audio", audioID, "sub", subID)
	return media, nil
}
