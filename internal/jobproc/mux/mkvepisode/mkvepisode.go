// Package mkvepisode drives the final Matroska mux of one episode: the step
// that turns an encoded video, its audio and subtitle tracks and an optional
// chapter file into the deliverable .mkv (or the external .mka).
//
// Behaviour is defined by three legacy files:
//
//   - JobProcessor/Muxer/MkvmergeMuxer.cs and its subclass
//     JobProcessor/Muxer/NewMkvEpisodeMuxer.cs define the command line. That
//     construction lives in internal/jobproc/mux/mkvmerge, which this package
//     calls rather than reimplements.
//   - Worker/ExecuteTaskService.GenerateMuxJob(MediaFile, containerFormat)
//     defines the output path and the MKA special case.
//   - Task/TaskDetail.UpdateOutputFileName defines the name of the final
//     deliverable.
//
// Multi-part episodes never reach mkvmerge as several input files. The legacy
// pipeline muxed each encoded part on its own (SingleVideoMuxer), joined the
// parts with AppendVideoMuxer and only then handed the single joined video to
// the episode mux. Parts below models that intermediate step; the final command
// line is identical whether the video came from one encode or from three.
package mkvepisode

import (
	"context"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/mux/mkvmerge"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/mux/simple"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// Name is the processor name used in logs and status events. It matches the
// legacy NLog logger name so log lines stay greppable across the rewrite.
const Name = "NewMkvEpisodeMuxer"

// NameMKA is the name used when the job produces the external audio file.
// ExecuteTaskService labelled that run "封装MKA中" rather than "最终封装中".
const NameMKA = "NewMkvEpisodeMuxer.MKA"

// Options configures one final mux.
type Options struct {
	// Mkvmerge is the absolute path to mkvmerge.
	Mkvmerge string
	// Output is the destination file: the .mkv deliverable, or the .mka when
	// the container is MKA. Callers that only have a prefix use OutputPath for
	// the MKV case and MKAOutputPath for the MKA one.
	Output string
	// Media describes the tracks to mux. A nil Media is rejected by Run before
	// mkvmerge starts, because the legacy muxer dereferenced it and a job
	// without tracks cannot produce a container anyway.
	Media *model.MediaFile
	// Container selects the output container. The zero value is MKV; the
	// pipeline chooses MKA when a separate audio file was requested.
	Container mkvmerge.Container
	// Roots maps volume ids to local roots for every FileRef. Nil is
	// standalone mode, as in mkvmerge.Options.
	Roots map[string]string
	// Priority is applied to the child process. The zero value selects
	// proc.DefaultPriority.
	Priority proc.Priority
}

// OutputPath returns the destination of the final mkv. It is the Go equivalent
// of the path ExecuteTaskService.GenerateMuxJob built:
//
//	Path.Combine(Path.GetDirectoryName(profile.OutputPathPrefix), task.OutputFile)
//
// outputPathPrefix is the profile's OutputPathPrefix; outputFileName is
// TaskDetail.OutputFile, i.e. the result of UpdateOutputFileName.
func OutputPath(outputPathPrefix, outputFileName string) string {
	return joinPath(dirName(outputPathPrefix), outputFileName)
}

// MKAOutputPath returns the destination of the external audio file:
// profile.OutputPathPrefix + ".mka".
func MKAOutputPath(outputPathPrefix string) string {
	return outputPathPrefix + ".mka"
}

// FileName returns the final deliverable's name, i.e. the Go equivalent of
// TaskDetail.UpdateOutputFileName:
//
//	FileInfo(inputFile).Name + "." + (containerFormat != "" ? containerFormat : videoFormat)
//
// The suffix is lowercased. When containerFormat is empty the video format is
// used instead, which is what a profile that only muxed the raw video stream
// produced (for example "00001.m2ts.hevc").
func FileName(inputFile, containerFormat, videoFormat string) string {
	format := containerFormat
	if format == "" {
		format = videoFormat
	}
	return baseName(inputFile) + "." + lower(format)
}

// Part is one encoded piece of a multi-part episode, in playback order.
type Part struct {
	// File is the part's video file, already muxed by SingleVideoMuxer.
	File string
}

// Parts are the encoded pieces of a multi-part episode, in playback order.
//
// The type is the bridge between the episode mux and the append step: the
// legacy pipeline filled MuxJob.VideoSlices from task.ReEncodeVideoSlices and
// let AppendVideoMuxer join them. AppendOptions reproduces that wiring.
type Parts struct {
	// Slices are the part files in playback order.
	Slices []Part
}

// AppendOptions returns the options for the step that joins the parts. It
// mirrors ExecuteTaskService.GenerateMuxJob(task, profile, info):
//
//	output = profile.WorkingPathPrefix + "_all." + codec.lower()
//	slices = task.ReEncodeVideoSlices (in order)
//
// codecString is the mux job's CodecString ("MKV" for a Matroska episode, "MKA"
// for the external audio file); it is lowercased for the extension.
//
// An episode with a single part still goes through the append step, exactly as
// the legacy pipeline did; mkvmerge then copies the one file it was given.
func (p Parts) AppendOptions(mkvmergePath, workingPathPrefix, codecString string, priority proc.Priority) simple.AppendVideoOptions {
	slices := make([]string, 0, len(p.Slices))
	for _, part := range p.Slices {
		slices = append(slices, part.File)
	}
	return simple.AppendVideoOptions{
		Options: simple.Options{
			Mkvmerge: mkvmergePath,
			Output:   workingPathPrefix + "_all." + lower(codecString),
			Priority: priority,
		},
		Slices: slices,
	}
}

// Processor runs the final mkv mux as a jobproc step.
type Processor struct {
	inner *mkvmerge.Processor
	opts  Options
	args  []string
}

// New returns a Processor for the given options.
func New(opts Options) *Processor {
	mopts := mkvmerge.Options{
		Mkvmerge:  opts.Mkvmerge,
		Output:    opts.Output,
		Media:     opts.Media,
		Container: opts.Container,
		Roots:     opts.Roots,
		Priority:  opts.Priority,
	}
	return &Processor{
		opts:  opts,
		inner: mkvmerge.New(mopts),
		args:  mkvmerge.BuildArgs(mopts),
	}
}

// Name implements jobproc.Processor.
func (p *Processor) Name() string {
	if p.opts.Container == mkvmerge.ContainerMKA {
		return NameMKA
	}
	return Name
}

// Args returns a copy of the mkvmerge arguments this processor runs with, so
// callers can log or compare the command line without starting a process.
func (p *Processor) Args() []string { return append([]string(nil), p.args...) }

// Run implements jobproc.Processor. A job without a media file is rejected
// before mkvmerge is started: the legacy muxer dereferenced it and would have
// surfaced a NullReferenceException as the catch-all "未知错误". A missing
// media is a pipeline bug, so it gets the config kind and its own summary; the
// mkvmerge error wording is reserved for failures of the tool itself.
func (p *Processor) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if p.opts.Media == nil {
		return okerr.New(okerr.KindConfig, "封装任务不完整", "没有可封装的轨道").
			WithTool(mkvmerge.Name, 0)
	}
	return p.inner.Run(ctx, sink)
}

// Close implements jobproc.Processor.
func (p *Processor) Close() error { return p.inner.Close() }

// Pause implements jobproc.Controllable.
func (p *Processor) Pause() error { return p.inner.Pause() }

// Resume implements jobproc.Controllable.
func (p *Processor) Resume() error { return p.inner.Resume() }

// SetPriority implements jobproc.Prioritizable.
func (p *Processor) SetPriority(prio jobproc.Priority) error { return p.inner.SetPriority(prio) }

// lower is the ASCII-only lowercase the legacy ToLower() applied to file
// extensions and format names.
func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
