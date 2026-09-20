// Package engine runs tasks. This file is the task pipeline's driver: the Go port
// of Worker/ExecuteTaskService.cs, which the legacy code expressed as one 842-line
// WorkerDoWork plus a dozen private helpers.
//
// The port is deliberately not one function. The legacy method mixed four kinds
// of decision in one control flow:
//
//   - what the profile says (re-encode or not, VFR or not, which container);
//   - what the source turned out to be (frame count, frame rate, tracks);
//   - which external tool runs next, in which order;
//   - what the operator sees.
//
// Each of those is a separate stage here (see pipeline_stages.go), and the stages
// communicate through an explicit runState (see pipeline_state.go) rather than
// through the task object. The value of the split is that every stage has one
// entry point, one contract and one failure mode, so the sequence can be read off
// runStages and a stage can be exercised on its own.
//
// Stage order, from ExecuteTaskService.WorkerDoWork:
//
//	loadProfile   read the profile and episode config the queue only stored a path to
//	inspect       vspipe --info, the frame-rate check, and (re-encode) the I-frame index
//	prepare       timecode, chapters, qpfile
//	planReEncode  align the re-encode slices to I-frames and lay out the parts
//	demux         extract audio and subtitle tracks, detect empty and duplicate ones
//	audio         transcode every extracted audio track
//	mka           mux the tracks that belong in the external audio file
//	video         encode the whole file, or every part of a re-encode
//	append        join the re-encode parts into one video
//	mux           the final container (or the merge with the old release)
//	rpc           the re-encode PSNR check
//	cleanup       delete the intermediate files, when the caller asked for it
//
// Every stage reports progress through a jobproc.ProgressSink, so the pipeline
// never parses tool output and never starts a process itself. PIPELINE.md next to
// this file is the written form of the stage contracts.
package engine

import (
	"context"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// Status strings the pipeline writes. They are the values the legacy code put on
// TaskStatus.CurrentStatus, kept verbatim because the UI and the regression
// fixtures compare against them.
const (
	statusFetchInfo   = "获取信息中"
	statusPrepare     = "准备中"
	statusAudioEncode = "音频转码中"
	statusVideoEncode = "压制中"
	statusPartMux     = "封装中"
	statusVideoAppend = "视频拼接中"
	statusFinalMux    = "最终封装中"
	statusMuxMKA      = "封装MKA中"
	statusRPC         = "RPC中"
	statusFinished    = "完成"
)

// PipelineOptions configures one task run. Everything the pipeline needs from
// the outside world is here, so the pipeline itself reads no global state.
type PipelineOptions struct {
	// Caps describes this node. It decides which demuxer runs, whether AAC is
	// possible at all, and which volume roots a FileRef resolves against.
	Caps node.Capabilities

	// LoadProfile resolves the profile and episode config of a task whose
	// model.Task fields are still the generic values a JSON round trip left
	// behind. The queue is the only thing that knows the config path
	// (TaskManager.ConfigPath), so the caller closes over its own queue:
	//
	//	opts.LoadProfile = func(t *model.Task) (*profile.Profile, *profile.EpisodeConfig, error) {
	//		path, ok := tm.ConfigPath(t.ID)
	//		if !ok {
	//			return nil, nil, okerr.New(okerr.KindNotFound, "找不到任务", "任务 %s 不在队列中", t.ID)
	//		}
	//		return loadProfileFromDisk(path)
	//	}
	//
	// It is only called when the task does not already carry typed values, and
	// its absence is reported rather than silently skipped.
	LoadProfile func(t *model.Task) (*profile.Profile, *profile.EpisodeConfig, error)

	// UpdateTask, when set, is how the pipeline publishes the state that
	// model.StatusEvent cannot carry: the chapter status and the RPC result.
	// The worker pool hands the pipeline a copy of the queued task, so a
	// mutation of the task alone would never reach the queue.
	UpdateTask func(id model.TaskID, fn func(*model.Task)) error

	// Numa decides the x265 --pools value. Nil means this machine's nodes.
	Numa *platform.Numa
	// Priority is applied to every child process. The zero value selects
	// proc.DefaultPriority.
	Priority proc.Priority
	// Asm optionally forces the encoder's assembly level, mirroring the legacy
	// avx512 configuration option.
	Asm string

	// RPCTemplate is the RpcTemplate.vpy the PSNR check is rendered from. The
	// legacy code hardcoded `.\tools\rpc\RpcTemplate.vpy`, a path relative to
	// the process working directory; a daemon needs it explicit, so an empty
	// value falls back to that relative path.
	RPCTemplate string

	// DetectChapters makes the pipeline run the chapter source detection the
	// legacy UI performed when a task was created (ChapterService.
	// UpdateChapterStatus). The pipeline itself never did this, so it is off by
	// default: a headless front end that creates tasks without a wizard should
	// turn it on, or call chapter.Service.UpdateChapterStatus itself.
	DetectChapters bool

	// CleanAfterTask removes the intermediate files once the task finished.
	// The legacy pipeline never did this at task end (Utils/Cleaner.cs has
	// exactly one caller, the UI's "clear finished tasks" action), so it is off
	// by default and exists for an unattended daemon.
	CleanAfterTask bool
}

// Pipeline runs one task. It is the RunFunc the Executor and the worker pool
// drive. It holds no per-task state, so one value serves any number of tasks.
type Pipeline struct {
	opts PipelineOptions
}

// NewPipeline returns a pipeline for the given options.
func NewPipeline(opts PipelineOptions) *Pipeline {
	if opts.Priority == 0 {
		opts.Priority = proc.DefaultPriority
	}
	if opts.Numa == nil {
		opts.Numa = platform.NewNumaWithCount(opts.Caps.NUMANodes)
	}
	if opts.LoadProfile == nil {
		opts.LoadProfile = missingProfile
	}
	return &Pipeline{opts: opts}
}

// RunFunc adapts the pipeline to engine.RunFunc.
func (p *Pipeline) RunFunc() RunFunc { return p.Run }

// The pipeline must satisfy the frozen Executor boundary's work function, which
// is what lets LocalExecutor and the worker pool drive it without knowing what a
// pipeline is.
var _ RunFunc = NewPipeline(PipelineOptions{}).Run

// Run executes one task. It reports progress on events and returns nil only when
// the task finished; every failure is a *okerr.Error so the caller can render
// the legacy operator message with okerr.Render.
//
// The context is the only cancellation channel. The legacy code polled
// BackgroundWorker.CancellationPending between phases; a context check at the
// same points is the equivalent, and each processor additionally kills its own
// child processes when the context ends.
func (p *Pipeline) Run(ctx context.Context, t *model.Task, events chan<- model.StatusEvent) error {
	if t == nil {
		return okerr.New(okerr.KindConfig, "任务为空", "无法执行空任务")
	}
	rep := newReporter(t.ID, events)

	st, err := p.loadProfile(t, rep)
	if err == nil {
		err = p.runStages(ctx, st, rep)
	}
	// The failure is reported by the returned error, and the executor turns it
	// into the task's terminal event. Emitting a "完成" event here would claim a
	// success the run did not have, and the worker pool treats the terminal
	// event as the single writer of the task's final state.
	if err != nil {
		return err
	}

	st.commit()
	rep.step(statusFinished, 100)
	log.Info("任务完成")
	return nil
}

// runStages is the stage sequence. It mirrors WorkerDoWork line for line; the
// comments mark where the legacy control flow is not linear.
func (p *Pipeline) runStages(ctx context.Context, st *runState, rep *reporter) error {
	if err := p.stageInspect(ctx, st, rep); err != nil {
		return err
	}
	if err := p.stagePrepare(ctx, st, rep); err != nil {
		return err
	}
	if err := p.stagePlanReEncode(st); err != nil {
		return err
	}

	// The legacy condition was `!IsReEncode || (IsReEncode && ReExtractSource)`:
	// a re-encode that reuses the old release's tracks skips demuxing entirely
	// and takes those tracks from the old file at mux time instead.
	if !st.reEncode() || st.reExtractSource() {
		if err := p.stageDemux(ctx, st, rep); err != nil {
			return err
		}
		if err := p.stageAudio(ctx, st, rep); err != nil {
			return err
		}
		if err := p.stageMKA(ctx, st, rep); err != nil {
			return err
		}
	}

	// "检查是否被OKE主线程取消" (ExecuteTaskService.cs:139).
	if err := ctx.Err(); err != nil {
		return canceled(err, st.t)
	}

	if err := p.stageVideoFlow(ctx, st, rep); err != nil {
		return err
	}
	if err := p.stageMuxFlow(ctx, st, rep); err != nil {
		return err
	}
	return p.finish(ctx, st, rep)
}

// stageVideoFlow is the video half of WorkerDoWork: a re-encode encodes and
// muxes every part and then joins them, a normal task encodes the whole file.
func (p *Pipeline) stageVideoFlow(ctx context.Context, st *runState, rep *reporter) error {
	if !st.reEncode() {
		return p.stageVideo(ctx, st, rep)
	}
	if err := p.stageReEncodeVideo(ctx, st, rep); err != nil {
		return err
	}
	return p.stageAppend(ctx, st, rep)
}

// stageMuxFlow is the container half: a re-encode without ReExtractSource merges
// the new video with the old release's remaining tracks, every other shape muxes
// the media file the earlier stages assembled.
func (p *Pipeline) stageMuxFlow(ctx context.Context, st *runState, rep *reporter) error {
	if st.reEncode() && !st.reExtractSource() {
		return p.stageMergeOld(ctx, st, rep)
	}
	return p.stageMux(ctx, st, rep)
}

// finish runs the two stages that always come last: the RPC check and, when it
// was asked for, the cleanup. A failure in the container stage skips both,
// exactly like the legacy catch block did.
func (p *Pipeline) finish(ctx context.Context, st *runState, rep *reporter) error {
	if err := p.stageRPC(ctx, st, rep); err != nil {
		return err
	}
	p.stageCleanup(st)
	return nil
}
