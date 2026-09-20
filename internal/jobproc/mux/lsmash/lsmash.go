// Package lsmash wraps the l-smash `muxer` tool for MP4 output.
//
// It covers both shapes the legacy code had: the full episode mux
// (NewMp4EpisodeMuxer, the only instantiation of LSmashMuxer the pipeline ever
// built) and a bare mux from explicit inputs. The command line is reproduced
// argument for argument, because the product of an established pipeline is
// compared against the legacy one.
//
// Three legacy behaviours are preserved deliberately and documented where they
// matter:
//
//   - The order of the `-i` options *is* the output track order: video, then
//     audio ordered by Info.Order, then (as a global option) the chapter file.
//   - Audio whose extension l-smash cannot import is dropped with a warning.
//   - Subtitle tracks are never passed. The legacy MP4 muxer ignored them; only
//     the MKV one consumed MediaFile.SubtitleTracks.
package lsmash

import (
	"context"
	"math"
	"regexp"
	"sort"
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
const Name = "l-smash"

// outputFormat is the only container this wrapper produces. Mirrors the
// `--file-format mp4` that LSmashMuxer.BuildCommandline hardcodes.
const outputFormat = "mp4"

// Progress and termination markers, taken verbatim from muxer.c:
//
//	eprintf("Importing: %"PRIu64" bytes\r", total_media_size);  // per 4 MiB
//	eprintf("Muxing completed!\n");
//
// LSmashMuxer.ProcessLine used a regex for the byte count and a substring test
// for the completion marker, and so does this.
var (
	progressPattern = regexp.MustCompile(`Importing: ([0-9]+) bytes`)
	doneMarker      = "Muxing completed"
	errMarker       = "Error: "
)

// AcceptableAudioExtensions lists the audio containers l-smash can import.
// Mirrors the local list in NewMp4EpisodeMuxer.BuildCommandline; anything else
// is skipped with a warning.
var AcceptableAudioExtensions = []string{".aac", ".m4a", ".ac3", ".dts", ".eac3"}

// TrackSource is one `-i` input: a file path plus the track options l-smash
// parses out of the same argument (parse_track_options in muxer.c).
//
// Options are rendered verbatim after a "?" in the order given. That is how the
// legacy code built them, including the unconditional `language=` and
// `handler=` it emitted for audio even when the value was empty.
type TrackSource struct {
	// File is the path passed to l-smash.
	File string
	// Options are the raw track options, e.g. "fps=24000/1001".
	Options []string
}

// VideoSource builds the video input of an episode mux. The legacy code always
// emitted fps=, even when the profile left the rate at zero.
func VideoSource(path string, fpsNum, fpsDen int64) TrackSource {
	return TrackSource{
		File:    path,
		Options: []string{"fps=" + strconv.FormatInt(fpsNum, 10) + "/" + strconv.FormatInt(fpsDen, 10)},
	}
}

// AudioSource builds an audio input of an episode mux. language= and handler=
// are always emitted, matching NewMp4EpisodeMuxer.BuildCommandline; l-smash
// treats an empty handler as the empty string rather than falling back to its
// default name.
func AudioSource(path, language, name string) TrackSource {
	return TrackSource{File: path, Options: []string{"language=" + language, "handler=" + name}}
}

// Options configures one l-smash run.
type Options struct {
	// Muxer is the absolute path to the l-smash muxer executable.
	Muxer string
	// Output is the destination .mp4 file.
	Output string
	// TotalFileSize is the sum of the input sizes, used to turn the imported
	// byte counter into a percentage. Mirrors MuxJob.TotalFileSize; a value of
	// zero disables percentage reporting.
	TotalFileSize int64
	// Video is the video input. Nil means a video-less mux, which is what an
	// m4a-style output looks like.
	Video *TrackSource
	// Audio are the audio inputs, in the order they should be muxed.
	Audio []TrackSource
	// ChapterFile is the chapter source. Empty means no --chapter option.
	ChapterFile string
	// Priority is applied to the child process.
	Priority proc.Priority
}

// EpisodeOptions converts a MediaFile into Options, applying exactly the
// selection rules of NewMp4EpisodeMuxer.BuildCommandline.
//
// roots maps a FileRef to a local path and is passed straight to
// model.FileRef.Resolve; the caller supplies the node's volume roots (see
// toolchain.Discover). A nil map resolves on the local volume.
//
// Audio tracks are ordered by Info.Order with a stable sort, so tracks sharing
// an order keep their slice position, exactly as LINQ's OrderBy did. Tracks
// whose extension l-smash cannot import are dropped with a warning. Subtitle
// tracks are ignored: the legacy MP4 muxer never passed them.
func EpisodeOptions(muxerPath, output string, media *model.MediaFile, totalSize int64, roots map[string]string) Options {
	opts := Options{Muxer: muxerPath, Output: output, TotalFileSize: totalSize}
	if media == nil {
		return opts
	}
	resolve := func(ref model.FileRef) string { return ref.Resolve(roots) }
	if media.Video != nil {
		v := media.Video
		src := VideoSource(resolve(v.File), v.Video.FpsNum, v.Video.FpsDen)
		opts.Video = &src
	}
	opts.Audio = orderedAudio(media, resolve)
	if media.Chapter != nil {
		opts.ChapterFile = resolve(media.Chapter.File)
	}
	return opts
}

// orderedAudio selects and orders the audio inputs of an episode mux.
func orderedAudio(media *model.MediaFile, resolve func(model.FileRef) string) []TrackSource {
	audio := make([]*model.AudioTrack, len(media.AudioTracks))
	copy(audio, media.AudioTracks)
	sort.SliceStable(audio, func(i, j int) bool {
		return audio[i].Info.Order < audio[j].Info.Order
	})

	out := make([]TrackSource, 0, len(audio))
	for _, a := range audio {
		path := resolve(a.File)
		if !isAcceptableAudio(a.File.Ext()) {
			log.Warn("MP4不支持封装该格式音轨", "ext", a.File.Ext(), "file", path)
			continue
		}
		out = append(out, AudioSource(path, a.Info.Language, a.Info.Name))
	}
	return out
}

// isAcceptableAudio reports whether l-smash can import the given extension. The
// comparison ignores case, matching GetExtension().ToLower().
func isAcceptableAudio(ext string) bool {
	for _, ok := range AcceptableAudioExtensions {
		if strings.EqualFold(ext, ok) {
			return true
		}
	}
	return false
}

// BuildArgs assembles the l-smash command line.
//
// The order is the legacy order: global options first (--file-format, -o), then
// one -i per input in mux order, then the chapter option. l-smash numbers the
// output tracks in the order the inputs appear, so the -i sequence *is* the
// track order.
func BuildArgs(opts Options) []string {
	args := make([]string, 0, 4+2*len(opts.Audio))
	args = append(args, "--file-format", outputFormat, "-o", opts.Output)
	if opts.Video != nil {
		args = append(args, "-i", trackArg(*opts.Video))
	}
	for _, a := range opts.Audio {
		args = append(args, "-i", trackArg(a))
	}
	if opts.ChapterFile != "" {
		args = append(args, "--chapter", opts.ChapterFile)
	}
	return args
}

// trackArg renders one -i value.
func trackArg(t TrackSource) string {
	if len(t.Options) == 0 {
		return t.File
	}
	return t.File + "?" + strings.Join(t.Options, ",")
}

// Processor runs l-smash muxer.
type Processor struct {
	opts Options
	args []string

	mu  sync.Mutex
	run *proc.Process
}

// New returns a Processor for the given options.
func New(opts Options) *Processor {
	return &Processor{opts: opts, args: BuildArgs(opts)}
}

// NewEpisode returns a Processor for a full episode mux. See EpisodeOptions for
// the meaning of roots.
func NewEpisode(muxerPath, output string, media *model.MediaFile, totalSize int64, roots map[string]string) *Processor {
	return New(EpisodeOptions(muxerPath, output, media, totalSize, roots))
}

// Name implements jobproc.Processor.
func (p *Processor) Name() string { return Name }

// Args returns a copy of the argument slice the processor passes to l-smash, so
// callers can log or compare the command line without running the tool.
func (p *Processor) Args() []string { return append([]string(nil), p.args...) }

// Run implements jobproc.Processor.
func (p *Processor) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if sink == nil {
		sink = jobproc.NopSink{}
	}
	priority := p.opts.Priority
	if priority == 0 {
		priority = proc.DefaultPriority
	}

	child, err := proc.Start(proc.Spec{
		Path:     p.opts.Muxer,
		Args:     p.args,
		Priority: priority,
		Name:     Name,
	})
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.run = child
	p.mu.Unlock()

	// Cancellation has to kill the child: Finish blocks on its pipes.
	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = child.Kill()
		case <-stopWatch:
		}
	}()

	// The legacy processor parsed both streams, so both are parsed here. The
	// handler runs on the two stream goroutines, so the first structured error
	// it produces is recorded under a mutex.
	var (
		handlerMu  sync.Mutex
		handlerErr error
	)
	handler := func(line string) error {
		err := p.handleLine(line, sink)
		if err != nil {
			handlerMu.Lock()
			if handlerErr == nil {
				handlerErr = err
			}
			handlerMu.Unlock()
		}
		return err
	}
	finishErr := child.Finish(handler, handler)

	handlerMu.Lock()
	lineErr := handlerErr
	handlerMu.Unlock()

	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", Name)
	}
	if lineErr != nil {
		return lineErr
	}
	if code := child.ExitCode(); code != 0 {
		// The legacy onExited only logged here; the failure has to become a
		// structured error so the operator sees the legacy message.
		log.Error("l-smash封装出错", "exitCode", code)
		return okerr.New(okerr.KindTool, okerr.ErrLSmash.Summary, "l-smash 退出代码 %d", code).
			WithTool(Name, code).
			WithFile(p.opts.Output).
			WithOutput(child.RecentOutput())
	}
	if finishErr != nil {
		return okerr.Wrap(finishErr, okerr.KindTool, okerr.ErrLSmash.Summary, "%s 异常退出", Name).
			WithTool(Name, child.ExitCode()).
			WithFile(p.opts.Output)
	}
	return nil
}

// Close implements jobproc.Processor.
func (p *Processor) Close() error {
	if child := p.child(); child != nil {
		return child.Close()
	}
	return nil
}

// Pause implements jobproc.Controllable.
func (p *Processor) Pause() error {
	if child := p.child(); child != nil {
		return child.Pause()
	}
	return nil
}

// Resume implements jobproc.Controllable.
func (p *Processor) Resume() error {
	if child := p.child(); child != nil {
		return child.Resume()
	}
	return nil
}

// SetPriority implements jobproc.Prioritizable.
func (p *Processor) SetPriority(prio jobproc.Priority) error {
	if child := p.child(); child != nil {
		return child.SetPriority(proc.Priority(prio))
	}
	return nil
}

func (p *Processor) child() *proc.Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.run
}

// handleLine turns one raw line of l-smash output into progress updates or a
// structured error. It mirrors LSmashMuxer.ProcessLine.
func (p *Processor) handleLine(raw string, sink jobproc.ProgressSink) error {
	// muxer.c separates its progress steps with "\r" so the console line is
	// overwritten in place. .NET's StreamReader accepted that as a line
	// terminator, so the legacy ProcessLine saw one step per call; bufio.Scanner
	// (used by internal/proc) only splits on "\n". The split is reproduced here
	// so the parsing stays identical on both runtimes.
	for _, line := range strings.Split(raw, "\r") {
		if err := p.handleSegment(line, sink); err != nil {
			return err
		}
	}
	return nil
}

func (p *Processor) handleSegment(line string, sink jobproc.ProgressSink) error {
	// Raw output is already traced by internal/proc; only the lines this
	// processor acts on are worth a second look.
	if idx := strings.Index(line, errMarker); idx >= 0 {
		detail := strings.TrimSpace(line[idx+len(errMarker):])
		return okerr.New(okerr.KindTool, okerr.ErrLSmash.Summary, "%s", detail).
			WithTool(Name, 0).
			WithFile(p.opts.Output).
			WithOutput(detail)
	}
	if strings.Contains(line, doneMarker) {
		sink.Report(jobproc.Progress{Percent: 100, Status: "封装完成"})
		return nil
	}
	m := progressPattern.FindStringSubmatch(line)
	if m == nil {
		return nil
	}
	size, parseErr := strconv.ParseInt(m[1], 10, 64)
	if parseErr != nil {
		// The pattern only matches digits, so the sole failure mode is a value
		// beyond int64. Such a byte count means the progress is unknown; clamp
		// it rather than dropping the update silently.
		log.Warn("l-smash进度字节数超出范围", "bytes", m[1])
		size = math.MaxInt64
	}
	percent := percentOf(size, p.opts.TotalFileSize)
	// The legacy code forwarded only values above one percent, to keep the UI
	// from flickering on every 4 MiB step.
	if percent <= 1 {
		return nil
	}
	sink.Report(jobproc.Progress{Percent: percent, Status: "封装中"})
	return nil
}

// percentOf converts an imported byte count into a percentage. A zero total
// yields -1, which the engine reads as "unknown".
func percentOf(size, total int64) float64 {
	if total <= 0 {
		return -1
	}
	return float64(size) / float64(total) * 100
}
