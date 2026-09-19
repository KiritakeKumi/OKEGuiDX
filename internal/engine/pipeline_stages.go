package engine

// This file holds the pipeline's stages. Each one names the legacy method it
// replaces in its own doc comment; the sequence that drives them is in
// runStages, in pipeline.go.

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/audio/ffmpegqaac"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/audio/qaac"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/audio/volume"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/demux/eac3to"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/demux/ffmpeg"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/mux/lsmash"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/mux/mkvepisode"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/mux/mkvmerge"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/mux/simple"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/rpc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video/iframe"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video/vspipeinfo"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// stageInspect is GetVSPipeInfo: read the script's properties, check the frame
// rate against the profile, and for a re-encode collect the old release's
// I-frame index.
func (p *Pipeline) stageInspect(ctx context.Context, st *runState, rep *reporter) error {
	rep.step(statusFetchInfo, -1)

	vspipe, err := toolchain.Require(p.opts.Caps, toolchain.ToolVSPipe)
	if err != nil {
		return err
	}
	st.vspipe = vspipe

	info := vspipeinfo.New(vspipeinfo.Options{
		VSPipe:         vspipe,
		Script:         st.p.InputScript,
		VSPipeArgs:     st.vspipeArgs(),
		ExpectedFpsNum: st.p.FpsNum,
		ExpectedFpsDen: st.p.FpsDen,
		VFR:            st.p.TimeCode,
	})
	if err := runProcessor(ctx, info, rep.sink()); err != nil {
		return err
	}
	if err := info.Validate(); err != nil {
		return err
	}
	st.vsInfo = info.Info()
	st.frames = st.vsInfo.NumFrames
	st.t.Frames = st.frames
	st.t.Status.Frames = st.frames

	log.Info("获取信息完成", "vfr", st.vsInfo.VFR, "fps", st.vsInfo.FPS,
		"fps_num", st.vsInfo.FpsNum, "fps_den", st.vsInfo.FpsDen, "frames", st.frames)

	if !st.reEncode() {
		return nil
	}
	gen := iframe.New(iframe.Options{
		VSPipe:         vspipe,
		OldFile:        st.cfg.ReEncodeOldFile,
		WorkingPath:    st.p.WorkingPathPrefix,
		NumberOfFrames: st.frames,
	})
	if err := runProcessor(ctx, gen, jobproc.NopSink{}); err != nil {
		return err
	}
	st.iFrames = gen.IFrames()
	log.Info("I帧序列", "iframes", formatIFrames(st.iFrames))
	return nil
}

// stagePrepare is DoPreparation: the timecode file, then the chapter file and
// the qpfile. The order matters because the chapter I-frame positions are
// computed either from the frame rate or from the timecode, and the timecode is
// what decides which of the two applies.
func (p *Pipeline) stagePrepare(ctx context.Context, st *runState, rep *reporter) error {
	rep.step(statusPrepare, -1)
	if err := st.prepareTimecode(); err != nil {
		return err
	}
	return p.prepareChapters(ctx, st)
}

// prepareChapters loads the chapter list, writes the OGM file the muxer reads and
// derives the qpfile from the chapter marks. It is the second half of
// DoPreparation plus GenerateQpFile.
func (p *Pipeline) prepareChapters(ctx context.Context, st *runState) error {
	if p.opts.DetectChapters {
		status, err := st.chapterService(p.opts.Caps, p.opts.Priority).UpdateChapterStatus(ctx, st.t)
		if err != nil {
			return err
		}
		st.t.Status.Chapter = status
	}

	svc := st.chapterService(p.opts.Caps, p.opts.Priority)
	info, err := svc.LoadChapter(ctx, st.t)
	if err != nil {
		return err
	}
	if info == nil {
		st.t.Status.Chapter = model.ChapterNo
		return nil
	}
	if st.t.Status.Chapter == model.ChapterMaybe || st.t.Status.Chapter == model.ChapterMKV {
		st.t.Status.Chapter = model.ChapterAdded
	}

	// Path.ChangeExtension(WorkingPathPrefix, ".txt"): the legacy code derived
	// the chapter file from the working prefix, not from the source.
	chapterPath := replaceExt(st.p.WorkingPathPrefix, ".txt")
	if err := info.WriteOGM(chapterPath); err != nil {
		return err
	}
	st.media.Chapter = &model.ChapterTrack{Track: model.Track{
		File:      model.NewFileRef(chapterPath),
		Info:      model.Info{Language: st.t.ChapterLanguage, Order: model.MaxOrder},
		TrackType: model.TrackTypeChapter,
	}}

	st.chapterFrames = chapterFrames(info, st.vsInfo, st.timecode)
	qpValue, err := st.writeQPFile(replaceExt(st.p.WorkingPathPrefix, ".qpf"))
	if err != nil {
		return err
	}
	st.qpValue = qpValue
	log.Info("准备时间码和章节文件完成", "timecode", st.timecodeFile, "qpfile", st.qpValue,
		"chapter_iframes", formatIFrames(iframe.IFrameInfo(st.chapterFrames)))
	return nil
}

// stagePlanReEncode is CheckReEncodeSlice plus the layout half of
// GenerateReEncodeJob: align every requested slice to the old release's
// I-frames, merge what became contiguous, and lay out the parts.
func (p *Pipeline) stagePlanReEncode(st *runState) error {
	if !st.reEncode() {
		return nil
	}
	slices, err := checkReEncodeSlices(st.cfg.ReEncodeSliceArray, st.iFrames, st.inputPath())
	if err != nil {
		return err
	}
	st.cfg.ReEncodeSliceArray = slices
	if err := st.planParts(); err != nil {
		return err
	}
	st.t.SliceParts = st.partRanges()
	return nil
}

// stageDemux is ExtractSource, GenerateAudioJob and AddSubtitle: extract the
// source tracks, decide what each one is worth, and queue the audio jobs.
func (p *Pipeline) stageDemux(ctx context.Context, st *runState, rep *reporter) error {
	demuxer, err := p.newDemuxer(st)
	if err != nil {
		return err
	}
	if err := runProcessor(ctx, demuxer, rep.sink()); err != nil {
		return err
	}
	res, err := demuxer.result()
	if err != nil {
		return err
	}

	log.Debug("demux 完成", "length", res.length, "log", res.logPath,
		"audio", len(res.media.AudioTracks), "subtitle", len(res.media.SubtitleTracks))
	st.srcAudio = res.media.AudioTracks
	st.srcSubs = res.media.SubtitleTracks
	st.enqueueAudio()
	st.addSubtitles()
	return nil
}

// demuxResult is the shape both demuxer implementations produce. The two
// packages declare structurally identical Options and Result types — the compat
// test in internal/jobproc/demux/ffmpeg pins that — but they are distinct Go
// types, so the pipeline normalises them once here rather than switching on the
// implementation everywhere.
type demuxResult struct {
	media *model.MediaFile
	// length is the container runtime in whole seconds.
	length int
	// logPath is the demuxer's own log, when it wrote one.
	logPath string
}

// demuxer is the pipeline-side view of a demuxer: a processor plus the result it
// produced.
type demuxer interface {
	jobproc.Processor
	result() (demuxResult, error)
}

// eacDemuxer adapts the eac3to demuxer.
type eacDemuxer struct{ *eac3to.Processor }

func (d eacDemuxer) result() (demuxResult, error) {
	res := d.Result()
	if res == nil || res.MediaFile == nil {
		return demuxResult{}, errNoDemuxOutput()
	}
	return demuxResult{media: res.MediaFile, length: res.Length, logPath: res.LogPath}, nil
}

// ffmpegDemuxer adapts the ffmpeg demuxer.
type ffmpegDemuxer struct{ *ffmpeg.Processor }

func (d ffmpegDemuxer) result() (demuxResult, error) {
	res := d.Result()
	if res == nil || res.MediaFile == nil {
		return demuxResult{}, errNoDemuxOutput()
	}
	return demuxResult{media: res.MediaFile, length: res.Length, logPath: res.LogPath}, nil
}

// errNoDemuxOutput is what a demuxer that exited successfully without producing a
// track list reports. It is a pipeline bug rather than an operator problem, so it
// carries the legacy "eac3to出错" summary and the detail explains itself.
func errNoDemuxOutput() error {
	return okerr.New(okerr.KindTool, okerr.ErrEac3to.Summary, "demux 没有输出任何轨道")
}

// newDemuxer selects the demuxer implementation from the node's capabilities.
//
// This is the one place in the pipeline where a platform difference is decided,
// and it is decided by a feature bit rather than by GOOS: eac3to is a closed
// Windows x86 binary, so a node that advertises it uses it and every other node
// uses ffmpeg.
func (p *Pipeline) newDemuxer(st *runState) (demuxer, error) {
	caps := p.opts.Caps
	if caps.HasFeature(node.FeatureEac3to) {
		path, err := toolchain.Require(caps, toolchain.ToolEac3to)
		if err != nil {
			return nil, err
		}
		return eacDemuxer{eac3to.New(eac3to.Options{
			EacPath:               path,
			SourceFile:            st.inputPath(),
			WorkingPathPrefix:     st.p.WorkingPathPrefix,
			AudioTracks:           st.audioSpecs(),
			SubtitleTracks:        st.subtitleSpecs(),
			SkipAllAudioTracks:    st.p.SkipAllAudioTracks,
			SkipAllSubtitleTracks: st.p.SkipAllSubtitleTracks,
			Volume:                p.volumeChecker(),
			Priority:              p.opts.Priority,
		})}, nil
	}

	ffmpegPath, err := toolchain.Require(caps, toolchain.ToolFFmpeg)
	if err != nil {
		return nil, err
	}
	probePath, err := toolchain.Require(caps, toolchain.ToolFFprobe)
	if err != nil {
		return nil, err
	}
	return ffmpegDemuxer{ffmpeg.New(ffmpeg.Options{
		FFmpeg:                ffmpegPath,
		FFprobe:               probePath,
		SourceFile:            st.inputPath(),
		WorkingPathPrefix:     st.p.WorkingPathPrefix,
		AudioTracks:           st.audioSpecs(),
		SubtitleTracks:        st.subtitleSpecs(),
		SkipAllAudioTracks:    st.p.SkipAllAudioTracks,
		SkipAllSubtitleTracks: st.p.SkipAllSubtitleTracks,
		Volume:                p.volumeChecker(),
		Priority:              p.opts.Priority,
	})}, nil
}

// audioSpecs converts the profile's audio track list into the model form the
// demuxer takes.
func (st *runState) audioSpecs() []model.AudioInfo {
	out := make([]model.AudioInfo, 0, len(st.p.AudioTracks))
	for _, spec := range st.p.AudioTracks {
		out = append(out, profile.AudioSpecToModel(spec))
	}
	return out
}

// subtitleSpecs converts the profile's subtitle track list into the model form
// the demuxer takes.
func (st *runState) subtitleSpecs() []model.Info {
	out := make([]model.Info, 0, len(st.p.SubtitleTracks))
	for _, spec := range st.p.SubtitleTracks {
		out = append(out, profile.TrackSpecToModel(spec))
	}
	return out
}

// volumeChecker wires the ffmpeg level measurement into the demuxer. Without it
// the demuxer cannot tell a silent track from an audible one and the pipeline
// would mux silence into the release. A missing ffmpeg is not fatal: the demuxer
// then reports "no measurement", exactly like the legacy code with an
// unconfigured checker.
func (p *Pipeline) volumeChecker() eac3to.VolumeMeasurer {
	path, err := toolchain.Require(p.opts.Caps, toolchain.ToolFFmpeg)
	if err != nil {
		log.Warn("没有 ffmpeg，无法检测空音轨", "err", err)
		return nil
	}
	return volume.NewChecker(path, p.opts.Priority)
}

// stageAudio runs every queued audio job and routes the results. Mirrors the
// AudioJob branch of DoAllJobs.
func (p *Pipeline) stageAudio(ctx context.Context, st *runState, rep *reporter) error {
	for i := range st.audioJobs {
		if err := p.runAudioJob(ctx, st, &st.audioJobs[i], rep); err != nil {
			return err
		}
	}
	st.routeAudioFiles()
	return nil
}

// runAudioJob transcodes one extracted track and routes the result.
//
// The legacy code chose the encoder from the extracted format and the requested
// codec (ExecuteTaskService.cs:562-585):
//
//	FLAC -> AAC              QAACEncoder
//	!FLAC && AAC && Lossy    FFmpegPipeQAACEncoder
//	format != codec          Constants.audioFormatMistachSmr
//
// AudioInfo.Lossy is never assigned anywhere in the legacy code and the profile
// has no field for it, so the second branch is unreachable; it is ported anyway
// so that a future profile field cannot silently change the shape of the code.
// The third branch is the "you asked for AAC but the source is AC3" case, which
// the demuxer's Lossy downgrade normally prevents by extracting every non-EAC3
// lossy request as FLAC.
func (p *Pipeline) runAudioJob(ctx context.Context, st *runState, job *audioJob, rep *reporter) error {
	srcFmt := strings.ToUpper(strings.TrimPrefix(filepath.Ext(job.input), "."))
	want := job.info.OutputCodec

	switch {
	case srcFmt == "FLAC" && want == "AAC":
		enc, err := qaac.New(p.opts.Caps, qaac.OptionsFromInfo(job.info, job.input, job.output))
		if err != nil {
			return err
		}
		_ = enc.SetPriority(jobproc.Priority(p.opts.Priority))
		rep.step(statusAudioEncode, 0)
		if err := runProcessor(ctx, enc, rep.sink()); err != nil {
			return err
		}
	case srcFmt != "FLAC" && want == "AAC" && job.info.Lossy:
		qaacPath, err := toolchain.Require(p.opts.Caps, toolchain.ToolQAAC)
		if err != nil {
			return err
		}
		ffmpegPath, err := toolchain.Require(p.opts.Caps, toolchain.ToolFFmpeg)
		if err != nil {
			return err
		}
		enc := ffmpegqaac.New(ffmpegqaac.Options{
			Caps:     p.opts.Caps,
			FFmpeg:   ffmpegPath,
			QAAC:     qaacPath,
			Input:    job.input,
			Output:   job.output,
			Info:     job.info,
			Priority: p.opts.Priority,
		})
		rep.step(statusAudioEncode, 0)
		if err := runProcessor(ctx, enc, rep.sink()); err != nil {
			return err
		}
	case srcFmt != want:
		return okerr.New(okerr.KindMismatch, okerr.ErrAudioFormatMismatch.Summary,
			"无法将%s格式的音轨转为%s格式", srcFmt, want).WithFile(st.inputPath())
	}

	job.file = model.NewFileRef(job.output)
	job.done = true
	return nil
}

// stageMKA muxes the tracks that were routed to the external audio file.
// Mirrors the `task.MkaOutFile.Tracks.Count > 0` block of WorkerDoWork.
func (p *Pipeline) stageMKA(ctx context.Context, st *runState, rep *reporter) error {
	if len(st.mka.AudioTracks)+len(st.mka.SubtitleTracks) == 0 {
		return nil
	}
	mkvmergePath, err := toolchain.Require(p.opts.Caps, toolchain.ToolMkvmerge)
	if err != nil {
		return err
	}
	mux := mkvepisode.New(mkvepisode.Options{
		Mkvmerge:  mkvmergePath,
		Output:    mkvepisode.MKAOutputPath(st.p.OutputPathPrefix),
		Media:     st.mka,
		Container: mkvmerge.ContainerMKA,
		Roots:     p.opts.Caps.Volumes,
		Priority:  p.opts.Priority,
	})
	rep.step(statusMuxMKA, 0)
	return runProcessor(ctx, mux, rep.sink())
}

// stageVideo encodes the whole video. Mirrors the non-re-encode branch of
// WorkerDoWork plus GenerateVideoJob.
func (p *Pipeline) stageVideo(ctx context.Context, st *runState, rep *reporter) error {
	part := st.wholeVideoPart()
	if err := p.encodePart(ctx, st, part, rep, 1); err != nil {
		return err
	}
	st.media.Video = st.videoTrack(part.output)
	return nil
}

// stageReEncodeVideo encodes every part of a re-encode and muxes each one into
// its own container. Mirrors GenerateReEncodeJob's job loop and the VideoJob and
// SingleVideo branches of DoAllJobs.
func (p *Pipeline) stageReEncodeVideo(ctx context.Context, st *runState, rep *reporter) error {
	total := len(st.parts)
	for i := range st.parts {
		part := &st.parts[i]
		if part.reEncode {
			if err := p.encodePart(ctx, st, *part, rep, total); err != nil {
				return err
			}
		}
		if err := p.muxPart(ctx, st, part, rep, total); err != nil {
			return err
		}
	}
	return nil
}

// encodePart runs one video encode. A re-encode part is a partial run bounded by
// the part's frame range; the whole-file case passes an open range, which the
// encoder base class turns into "no -s/-e".
func (p *Pipeline) encodePart(ctx context.Context, st *runState, part part, rep *reporter, total int) error {
	encoderPath, err := toolchain.Require(p.opts.Caps, st.encoderTool())
	if err != nil {
		return err
	}
	spec := encodeSpec{
		vspipe:     st.vspipe,
		script:     st.p.InputScript,
		vspipeArgs: st.vspipeArgs(),
		encoder:    encoderPath,
		params:     st.encoderParams(part, numaFrom(ctx)),
		output:     part.output,
		// A partial encode reports only the frames inside its range, so the
		// expected count is the range's length. GenerateVideoJob:404 did the
		// same, and both the progress percentage and the truncated-encode
		// check depend on it.
		totalFrames: part.frameCount(st.frames),
		frameStart:  part.frames.Begin,
		frameEnd:    part.frames.End,
		asm:         p.opts.Asm,
	}

	rep.step(partStatus(part, total, statusVideoEncode), 0)
	return runProcessor(ctx, st.newEncoder(spec), rep.sink())
}

// muxPart muxes one part of a re-encode. A re-encoded part gets its own
// single-video container; a part copied from the old release is cut out of it
// with mkvmerge's --split.
//
// The legacy code passed SliceInfo(0, -1) for an encoded part, so mkvmerge's
// --split was never applied there; that quirk is reproduced rather than "fixed"
// (DECISIONS-NEEDED.md B2, "保留（无害）").
func (p *Pipeline) muxPart(ctx context.Context, st *runState, part *part, rep *reporter, total int) error {
	mkvmergePath, err := toolchain.Require(p.opts.Caps, toolchain.ToolMkvmerge)
	if err != nil {
		return err
	}
	opts := simple.SingleVideoOptions{
		Options: simple.Options{
			Mkvmerge:   mkvmergePath,
			Output:     st.partMuxPath(*part),
			SourceFile: st.inputPath(),
			Priority:   p.opts.Priority,
		},
		Input: part.source(st.cfg.ReEncodeOldFile),
	}
	if !part.reEncode {
		opts.Partial = true
		opts.FrameRange = part.frames
	}
	rep.step(partStatus(*part, total, statusPartMux), 0)
	if err := runProcessor(ctx, simple.NewSingleVideo(opts), rep.sink()); err != nil {
		return err
	}
	part.muxed = opts.Output
	part.file = model.NewFileRef(opts.Output)
	return nil
}

// stageAppend joins the parts into one video. Mirrors GenerateMuxJob(info) and
// the AppendVideo branch of DoAllJobs.
func (p *Pipeline) stageAppend(ctx context.Context, st *runState, rep *reporter) error {
	mkvmergePath, err := toolchain.Require(p.opts.Caps, toolchain.ToolMkvmerge)
	if err != nil {
		return err
	}
	opts := st.appendOptions(mkvmergePath, p.opts.Priority)
	rep.step(statusVideoAppend, 0)
	if err := runProcessor(ctx, simple.NewAppendVideo(opts), rep.sink()); err != nil {
		return err
	}
	st.appended = opts.Output
	st.media.Video = st.videoTrack(opts.Output)
	return nil
}

// stageMux writes the final container. Mirrors GenerateMuxJob(mediaOutFile) and
// the NewMkvEpisode and NewMp4Episode branches of DoAllJobs.
//
// The container is the profile's: MKV goes through mkvmerge, MP4 through l-smash.
// The legacy code switched on the MuxJob's MuxType, which was in turn chosen
// from the container string; MKA never reaches this stage because the earlier
// stage already handled it.
func (p *Pipeline) stageMux(ctx context.Context, st *runState, rep *reporter) error {
	output := st.finalOutputPath()
	rep.step(statusFinalMux, 0)

	if st.p.ContainerFormat == string(profile.ContainerMP4) {
		muxerPath, err := toolchain.Require(p.opts.Caps, toolchain.ToolLSmash)
		if err != nil {
			return err
		}
		mux := lsmash.NewEpisode(muxerPath, output, st.media,
			st.media.TotalFileSize(st.fileSizeOf), p.opts.Caps.Volumes)
		_ = mux.SetPriority(jobproc.Priority(p.opts.Priority))
		if err := runProcessor(ctx, mux, rep.sink()); err != nil {
			return err
		}
		return st.recordDeliverable(output)
	}

	mkvmergePath, err := toolchain.Require(p.opts.Caps, toolchain.ToolMkvmerge)
	if err != nil {
		return err
	}
	mux := mkvepisode.New(mkvepisode.Options{
		Mkvmerge:  mkvmergePath,
		Output:    output,
		Media:     st.media,
		Container: mkvmerge.ContainerMKV,
		Roots:     p.opts.Caps.Volumes,
		Priority:  p.opts.Priority,
	})
	if err := runProcessor(ctx, mux, rep.sink()); err != nil {
		return err
	}
	return st.recordDeliverable(output)
}

// stageMergeOld merges the appended video with the old release's remaining
// tracks. Mirrors GenerateMuxJob(reEncodeOldFile) and the MergeOldRemux branch of
// DoAllJobs: the old file's video is dropped, its audio, subtitle and chapter
// tracks survive.
func (p *Pipeline) stageMergeOld(ctx context.Context, st *runState, rep *reporter) error {
	mkvmergePath, err := toolchain.Require(p.opts.Caps, toolchain.ToolMkvmerge)
	if err != nil {
		return err
	}
	output := st.finalOutputPath()
	rep.step(statusFinalMux, 0)
	mux := simple.NewMergeOld(simple.MergeOldOptions{
		Options: simple.Options{
			Mkvmerge:   mkvmergePath,
			Output:     output,
			SourceFile: st.inputPath(),
			Priority:   p.opts.Priority,
		},
		Input:        st.appended,
		OldFile:      st.cfg.ReEncodeOldFile,
		TimecodeFile: st.timecodeFile,
	})
	if err := runProcessor(ctx, mux, rep.sink()); err != nil {
		return err
	}
	return st.recordDeliverable(output)
}

// stageRPC runs the PSNR check. Mirrors DoRPCheck.
//
// The check compares the encoded video against the script, so a task that
// produced no video track cannot run it. The legacy code dereferenced
// MediaOutFile.VideoTrack unconditionally and would have surfaced a
// NullReferenceException as the catch-all "未知错误"; reporting the missing
// track is strictly clearer.
func (p *Pipeline) stageRPC(ctx context.Context, st *runState, rep *reporter) error {
	if !st.p.Rpc {
		st.t.Status.RPC = model.RPCSkipped
		return nil
	}
	video := st.videoPath()
	if video == "" {
		return okerr.New(okerr.KindConfig, "RPC任务不完整",
			"任务没有可检查的视频轨，无法执行 RPC").WithFile(st.inputPath())
	}
	vspipe, err := toolchain.Require(p.opts.Caps, toolchain.ToolVSPipe)
	if err != nil {
		return err
	}
	checker := rpc.New(rpc.Options{
		VSPipe:       vspipe,
		Template:     p.rpcTemplate(),
		SourceScript: st.p.InputScript,
		RippedFile:   video,
		VSPipeArgs:   st.vspipeArgs(),
		TotalFrames:  st.frames,
		Output:       replaceExt(st.p.InputScript, ".rpc"),
		FailedOutput: st.p.OutputPathPrefix + ".rpc",
		SourceFile:   st.inputPath(),
		Priority:     p.opts.Priority,
	})
	rep.step(statusRPC, 0)
	if err := runProcessor(ctx, checker, rep.sink()); err != nil {
		return err
	}
	st.t.Status.RPC = checker.Status()
	_, out := checker.Result()
	st.t.Status.RPCOutput = out
	return nil
}

// stageCleanup removes the intermediate files of the source. The legacy pipeline
// never ran the cleaner at task end, so this is opt-in (see
// PipelineOptions.CleanAfterTask); the white list protects everything the task
// itself produced.
func (p *Pipeline) stageCleanup(st *runState) {
	if !p.opts.CleanAfterTask {
		return
	}
	input := st.inputPath()
	if err := ensureFileExists(input); err != nil {
		return
	}
	removed, err := NewCleaner().Clean(input, st.whiteList())
	if err != nil {
		log.Warn("清理中间文件失败", "err", err)
		return
	}
	log.Debug("清理中间文件完成", "count", len(removed))
}

// rpcTemplate resolves RpcTemplate.vpy. The legacy code hardcoded
// `.\tools\rpc\RpcTemplate.vpy`, a path relative to the process working
// directory; RPCTemplate overrides it, and the rpc processor reports an absent
// file when it tries to read it.
func (p *Pipeline) rpcTemplate() string {
	if p.opts.RPCTemplate != "" {
		return p.opts.RPCTemplate
	}
	if info, ok := p.opts.Caps.Tool(toolchain.ToolRPCChecker); ok && info.Path != "" {
		dir := filepath.Dir(info.Path)
		for _, name := range []string{"RpcTemplate.vpy", "rpctemplate.vpy"} {
			candidate := filepath.Join(dir, name)
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	// Fall back to the working directory, which is where a portable install
	// puts it and where the legacy relative path resolved from.
	return rpc.DefaultTemplatePath
}
