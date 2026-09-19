// Package mkvmerge wraps mkvmerge, the Matroska multiplexer.
//
// Behaviour is defined by JobProcessor/Muxer/MkvmergeMuxer.cs and its concrete
// subclass JobProcessor/Muxer/NewMkvEpisodeMuxer.cs. Two details are easy to
// lose when porting it:
//
//   - Argument order is significant. mkvmerge resolves --track-order against
//     the order the source files appear in, so the legacy order is reproduced
//     verbatim.
//   - mkvmerge exits with 1 for warnings and 2 for errors; only 2 is a failure
//     (see MkvmergeMuxer.onExited).
//
// Which tracks end up in which container is decided before the muxer runs in
// the legacy pipeline (ExecuteTaskService.AddSubtitle and the AudioJob branch
// of DoAllJobs): Default tracks go to the main file, Mka tracks to the external
// audio file, External tracks are only renamed with a CRC32 and ExtractOnly and
// Skip tracks are never muxed. Options.Container selects the container, so a
// track list may be handed to either job unchanged.
package mkvmerge

import (
	"context"
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
const Name = "mkvmerge"

// Container selects which of the two containers a mux job produces. The legacy
// pipeline decided this with the container format string ("MKV" or "MKA") and
// by routing each track to MediaOutFile or MkaOutFile beforehand.
type Container int

// Containers.
const (
	// ContainerMKV is the main episode container: video, Default audio and
	// subtitle tracks and chapters.
	ContainerMKV Container = iota
	// ContainerMKA is the external audio container: Mka tracks only. It never
	// holds video or chapters, exactly like MkaOutFile in the legacy code.
	ContainerMKA
)

// Options configures one mkvmerge run.
type Options struct {
	// Mkvmerge is the absolute path to the mkvmerge executable.
	Mkvmerge string
	// Output is the destination file (.mkv or .mka).
	Output string
	// Media describes the tracks to mux. Nil produces the base arguments only.
	Media *model.MediaFile
	// Container selects the output container. The zero value is ContainerMKV.
	Container Container
	// Roots maps volume ids to local roots and is used to resolve every
	// FileRef. Nil means standalone mode, where a reference resolves below its
	// volume root (model.FileRef.Resolve).
	Roots map[string]string
	// Priority is applied to the child process. The zero value means
	// proc.DefaultPriority, as in every other wrapper.
	Priority proc.Priority
}

// BuildArgs assembles the mkvmerge command line.
//
// It reproduces MkvmergeMuxer.BuildCommandline followed by
// NewMkvEpisodeMuxer.BuildCommandline token for token; the legacy code
// concatenated them into one string, the tokens it produced are returned here.
//
// --track-order is appended even when no track was emitted, because the legacy
// code appended it unconditionally; a job without tracks is rejected by
// mkvmerge itself.
func BuildArgs(opts Options) []string {
	args := []string{"--ui-language", "en", "--output", opts.Output}
	media := opts.Media
	if media == nil {
		return append(args, "--track-order", "")
	}

	// The video track has no mux option in the profile: GenerateVideoJob adds
	// it to the main container unconditionally.
	hasVideo := media.Video != nil && opts.Container == ContainerMKV
	fileID := 0
	order := make([]string, 0, len(media.AudioTracks)+len(media.SubtitleTracks)+1)

	if hasVideo {
		if tc := media.Video.Video.TimeCodeFile; !tc.IsZero() {
			args = append(args, "--timestamps", "0:"+opts.resolve(tc))
		}
		args = appendTrack(args, true, "und", "", opts.resolve(media.Video.File))
		order = append(order, strconv.Itoa(fileID)+":0")
		fileID++
	}

	// The first audio track is default only when a video track precedes it;
	// in the external audio container no track is default.
	audio := muxedAudio(media.AudioTracks, opts.Container)
	for i, t := range audio {
		args = appendTaggedTrack(args, hasVideo && i == 0, t.Info.Language, t.Info.Name, opts.resolve(t.File))
		order = append(order, strconv.Itoa(fileID)+":0")
		fileID++
	}

	for _, t := range muxedSubtitles(media.SubtitleTracks, opts.Container) {
		args = appendTaggedTrack(args, false, t.Info.Language, t.Info.Name, opts.resolve(t.File))
		order = append(order, strconv.Itoa(fileID)+":0")
		fileID++
	}

	if media.Chapter != nil && opts.Container == ContainerMKV {
		if lang := media.Chapter.Info.Language; lang != "" {
			args = append(args, "--chapter-language", lang)
		}
		args = append(args, "--chapters", opts.resolve(media.Chapter.File))
	}

	return append(args, "--track-order", strings.Join(order, ","))
}

// appendTrack writes one source file with its per-track options, mirroring the
// legacy trackTemplate. mkvmerge applies these to track 0 of the file that
// follows, hence the parentheses that group the file with its options.
func appendTrack(args []string, def bool, language, name, path string) []string {
	flag := "0"
	if def {
		flag = "1"
	}
	return append(args,
		"--default-track", "0:"+flag,
		"--language", "0:"+language,
		"--track-name", "0:"+name,
		"(", path, ")",
	)
}

// appendTaggedTrack is appendTrack preceded by the tag template that every
// audio and subtitle file gets.
func appendTaggedTrack(args []string, def bool, language, name, path string) []string {
	args = append(args, "--no-track-tags", "--no-global-tags")
	return appendTrack(args, def, language, name, path)
}

// muxedIn reports whether a track with the given mux option reaches the
// container.
func muxedIn(m model.MuxOption, c Container) bool {
	if c == ContainerMKA {
		return m == model.MuxOptionMka
	}
	return m == model.MuxOptionDefault
}

// muxedAudio returns the audio tracks that belong in the container, ordered by
// Info.Order. OrderBy was a stable sort in the legacy code, so tracks sharing
// an order keep their original position.
func muxedAudio(tracks []*model.AudioTrack, c Container) []*model.AudioTrack {
	out := make([]*model.AudioTrack, 0, len(tracks))
	for _, t := range tracks {
		if muxedIn(t.Info.Mux, c) {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Info.Order < out[j].Info.Order })
	return out
}

// muxedSubtitles returns the subtitle tracks that belong in the container,
// ordered by Info.Order.
func muxedSubtitles(tracks []*model.SubtitleTrack, c Container) []*model.SubtitleTrack {
	out := make([]*model.SubtitleTrack, 0, len(tracks))
	for _, t := range tracks {
		if muxedIn(t.Info.Mux, c) {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Info.Order < out[j].Info.Order })
	return out
}

// resolve turns a logical reference into the path mkvmerge is given.
func (o Options) resolve(r model.FileRef) string { return r.Resolve(o.Roots) }

// Progress line pattern, taken from MkvmergeMuxer.ProcessLine.
var progressRe = regexp.MustCompile(`Progress: (\d*?)%`)

// Line markers. Both spellings of the completion line are accepted because
// different mkvmerge versions print different wordings.
const (
	errMarker       = "Error: "
	oldFinishMarker = "Muxing took"
	finishMarker    = "Multiplexing took"
)

// parseLine classifies one line of mkvmerge output. It returns the completion
// percentage when the line carried progress information.
//
// The legacy code tested for substrings rather than prefixes, so a line that
// merely mentions "Error: " is treated as an error as well.
func parseLine(line string) (float64, bool, error) {
	if strings.Contains(line, errMarker) {
		// Mirrors line.Substring(7): the operator-facing detail is the text
		// after the marker, not the whole line.
		detail := line[len(errMarker):]
		return 0, false, okerr.New(okerr.KindTool, okerr.ErrMkvmerge.Summary, "%s", detail).
			WithOutput(detail)
	}
	if m := progressRe.FindStringSubmatch(line); m != nil {
		// The legacy guard ignored percentages of 1 or less.
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 1 {
			return v, true, nil
		}
	}
	if strings.Contains(line, oldFinishMarker) || strings.Contains(line, finishMarker) {
		return 100, true, nil
	}
	return 0, false, nil
}

// Processor runs mkvmerge as a jobproc step.
type Processor struct {
	opts Options

	mu         sync.Mutex
	proc       *proc.Process
	sink       jobproc.ProgressSink
	failureErr error
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

// Run implements jobproc.Processor. It blocks until mkvmerge exits or ctx is
// canceled.
func (p *Processor) Run(ctx context.Context, sink jobproc.ProgressSink) error {
	if sink == nil {
		sink = jobproc.NopSink{}
	}
	p.mu.Lock()
	p.sink = sink
	p.mu.Unlock()

	args := BuildArgs(p.opts)
	log.Info("开始封装", "tool", Name, "args", strings.Join(args, " "))

	child, err := proc.Start(proc.Spec{
		Path:     p.opts.Mkvmerge,
		Args:     args,
		Priority: p.opts.Priority,
		Name:     Name,
	})
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.proc = child
	p.mu.Unlock()

	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = child.Kill()
		case <-stopWatch:
		}
	}()

	// mkvmerge keeps writing after it reports a problem, and the legacy code
	// recorded the first exception while continuing to read. Returning nil here
	// keeps both readers draining; the failure is reported after the exit.
	onLine := func(line string) error {
		if err := p.onLine(line); err != nil {
			p.recordFailure(err)
		}
		return nil
	}
	runErr := child.Finish(onLine, onLine)

	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", Name)
	}
	// A fatal line carries the text mkvmerge printed, which is what the
	// operator needs; it wins over the exit code, as in the legacy code.
	if failure := p.failure(); failure != nil {
		return failure
	}
	// mkvmerge uses 0 for success, 1 for "finished with warnings" and 2 for a
	// failure. The reference reported only 2, and a warning must not fail the
	// task: the output file is complete.
	code := child.ExitCode()
	if code == 1 {
		log.Warn("mkvmerge 完成但带有警告", "tool", Name, "exit_code", code)
		return nil
	}
	if code >= 2 {
		return okerr.New(okerr.KindTool, okerr.ErrMkvmerge.Summary, "mkvmerge 退出代码 %d", code).
			WithTool(Name, code).
			WithOutput(lastLine(child.RecentOutput()))
	}
	if runErr != nil {
		return okerr.Wrap(runErr, okerr.KindTool, okerr.ErrMkvmerge.Summary, "%s 异常退出", Name).
			WithTool(Name, code)
	}
	return nil
}

// Close implements jobproc.Processor.
func (p *Processor) Close() error {
	if child := p.current(); child != nil {
		return child.Close()
	}
	return nil
}

// Pause implements jobproc.Controllable.
func (p *Processor) Pause() error {
	if child := p.current(); child != nil {
		return child.Pause()
	}
	return nil
}

// Resume implements jobproc.Controllable.
func (p *Processor) Resume() error {
	if child := p.current(); child != nil {
		return child.Resume()
	}
	return nil
}

// SetPriority implements jobproc.Prioritizable.
func (p *Processor) SetPriority(prio jobproc.Priority) error {
	if child := p.current(); child != nil {
		return child.SetPriority(proc.Priority(prio))
	}
	return nil
}

// onLine feeds one line of output into the parser and forwards progress. A
// failure is returned so the caller can record it; it is not reported as a
// handler error, because mkvmerge keeps writing after it prints one and the
// legacy code kept reading.
func (p *Processor) onLine(line string) error {
	percent, ok, err := parseLine(line)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	p.mu.Lock()
	sink := p.sink
	p.mu.Unlock()
	if sink != nil {
		sink.Report(jobproc.Progress{Percent: percent})
	}
	return nil
}

// recordFailure keeps the first failure seen on either output stream, mirroring
// CommandlineJobProcessor.ThrowException.
func (p *Processor) recordFailure(err error) {
	p.mu.Lock()
	if p.failureErr == nil {
		p.failureErr = err
	}
	p.mu.Unlock()
}

func (p *Processor) failure() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failureErr
}

func (p *Processor) current() *proc.Process {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.proc
}

// lastLine returns the final non-empty line of captured output, which is what
// the rendered message should show.
func lastLine(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			return s
		}
	}
	return ""
}
