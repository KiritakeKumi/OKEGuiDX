package engine

import (
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/chapter"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/mux/mkvepisode"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/mux/simple"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video/iframe"
	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// runState is everything one task run accumulates. The legacy code kept these
// across half a dozen places at once: TaskDetail (MediaOutFile, MkaOutFile,
// ReEncodeVideoSlices, NumberOfFrames, LengthInMiliSec, OutputFile, BitRate),
// TaskProfile (IsReEncode, WorkingPathPrefix, OutputPathPrefix) and local
// variables inside WorkerDoWork (vsInfo, finalVideoInfo, timeCodeFile, qpFile).
//
// Bringing them together is what makes a stage signature possible: a stage takes
// the state plus a reporter, and nothing else.
type runState struct {
	// opts and t are the pipeline's configuration and the task being run.
	opts *PipelineOptions
	t    *model.Task

	// p and cfg are the profile and the episode config. p is non-nil once
	// loadProfile returned; cfg is non-nil only for a re-encode task.
	p   *profile.Profile
	cfg *profile.EpisodeConfig

	// vspipe is the resolved vspipe path, cached because three stages need it.
	vspipe string
	// vsInfo is the script's properties as vspipe reported them.
	vsInfo model.VSVideoInfo
	// frames is the script's frame count, i.e. vsInfo.NumFrames.
	frames int64
	// iFrames is the old release's I-frame index, re-encode only.
	iFrames iframe.IFrameInfo

	// timecode is the parsed timecode file, non-nil only for a VFR job.
	timecode *Timecode
	// timecodeFile is the path handed to mkvmerge: the corrected .v2.tcfile
	// when it could be written, the original .tcfile otherwise.
	timecodeFile string
	// chapterFrames are the chapter positions as frame numbers; the qpfile
	// drives the encoder's key frames from them.
	chapterFrames []int64
	// qpValue is what the encoder receives for its qpfile option: a path for
	// x264/x265, an inline comma-separated list for SVT-AV1.
	qpValue string

	// media is the main container's track list, mka the external audio file's.
	media *model.MediaFile
	mka   *model.MediaFile
	// srcAudio and srcSubs are the extracted tracks the pipeline must route.
	srcAudio []*model.AudioTrack
	srcSubs  []*model.SubtitleTrack
	// audioJobs are the audio encodes to run, in extraction order.
	audioJobs []audioJob

	// parts is the re-encode part layout, in playback order.
	parts []part
	// appended is the file the append step produced, if any.
	appended string
	// deliverable is the final container, once it exists.
	deliverable model.FileRef
}

// audioJob is one audio encode: an extracted file, the metadata that decides how
// to encode it, and its destination.
type audioJob struct {
	info   model.AudioInfo
	input  string
	output string
	// file is the encoded track's reference, set once the encode succeeded.
	file model.FileRef
	// done records that the encode ran, so the routing stage can skip a job
	// that was rejected before it started.
	done bool
}

// part is one piece of a re-encode episode, or the single piece of a normal one.
//
// The legacy code carried the same information in VideoSliceInfo plus a
// VideoSliceTrack for the output file; the two are merged here because they are
// only ever read together.
type part struct {
	// id is the part's number in the layout, which is what names its files and
	// what the "Part N/M" status shows. It is the legacy partId.
	id int
	// reEncode reports whether this part must be encoded, as opposed to being
	// copied out of the old release.
	reEncode bool
	// frames is the part's half-open frame range in the script's numbering.
	// End == OpenEnded means "to the end of the video".
	frames model.SliceInfo
	// output is the encoder's destination, e.g. `<prefix>_part1.hevc`.
	output string
	// muxed is the file the part mux was written to, set once it succeeded.
	muxed string
	// file is the part's reference for the append step.
	file model.FileRef
	// qpValue overrides the episode-wide qpfile for this part; empty means the
	// part has no chapter marks of its own.
	qpValue string
}

// source returns the file the part's mux reads: the encoder's output for a
// re-encoded part, the old release for a copied one.
func (p part) source(oldFile string) string {
	if p.reEncode {
		return p.output
	}
	return oldFile
}

// frameCount is the number of frames the part covers.
//
// A whole-file encode has an open range and therefore reports the script's frame
// count; a partial one reports its own length. GenerateVideoJob:404 made the
// same distinction with `sliceInfo.FrameRange.GetLength()`, and the encoders use
// the number both for the progress percentage and for the truncated-encode
// check, so getting it wrong turns a healthy part into a reported crash.
func (p part) frameCount(total int64) int64 {
	if p.frames.End == model.OpenEnded {
		return total
	}
	return p.frames.Length()
}

// reEncode reports whether this is a re-encode task.
//
// The legacy code had two sources of truth, TaskProfile.IsReEncode (set by the
// wizard from the episode config) and the presence of a config object, and used
// them interchangeably. The Go model keeps both in the profile, so the pipeline
// reads the one field; loadProfile is what makes the two agree.
func (st *runState) reEncode() bool { return st.p != nil && st.p.IsReEncode }

// inputPath resolves the task's first input.
func (st *runState) inputPath() string {
	if len(st.t.Inputs) == 0 {
		return ""
	}
	return st.t.Inputs[0].Resolve(st.opts.Caps.Volumes)
}

// container returns the output container, uppercase as the profile validator
// leaves it.
func (st *runState) container() string { return st.p.ContainerFormat }

// vspipeArgs returns the profile's extra vspipe arguments.
func (st *runState) vspipeArgs() []string {
	if st.cfg == nil {
		return nil
	}
	return st.cfg.VspipeArgs
}

// chapterService builds the chapter service for this task. Every external tool
// it needs is looked up in the node's capabilities, so a node without tchapter
// gets a service that reports the missing tool rather than a nil dereference.
func (st *runState) chapterService(caps node.Capabilities, prio proc.Priority) *chapter.Service {
	svc := &chapter.Service{
		Roots:            caps.Volumes,
		RenumberChapters: st.p.RenumberChapters,
	}
	toolPath, _ := toolchain.Require(caps, toolchain.ToolTChapter)
	svc.Parser = &chapter.Parser{Tool: toolPath, Priority: prio}
	if extract, err := toolchain.Require(caps, toolchain.ToolMkvextract); err == nil {
		svc.Parser.MkvExtract = extract
	}
	probePath, _ := toolchain.Require(caps, chapter.ProbeToolName())
	svc.Probe = &chapter.Probe{Tool: probePath, Priority: prio}
	return svc
}

// prepareTimecode is the first half of DoPreparation: write the corrected v2
// timecode file and compute the task length.
//
// The legacy code derived the length from the timecode when the job was VFR and
// from the frame rate otherwise. Both are reproduced; the difference matters
// because the chapter service drops trailing marks using that length.
func (st *runState) prepareTimecode() error {
	input := st.inputPath()
	if !st.vsInfo.VFR {
		// (frames - 1) / fps * 1000 + 0.5, truncated: the legacy expression.
		if fps := st.vsInfo.FPS; fps > 0 {
			st.t.LengthMS = int64(float64(st.frames-1)/fps*1000 + 0.5)
		}
		st.t.Status.LengthMS = st.t.LengthMS
		return nil
	}

	source := replaceExt(input, ".tcfile")
	tc, err := LoadTimecode(source, st.frames)
	if err != nil {
		return err
	}
	st.timecode = tc

	target := replaceExt(input, ".v2.tcfile")
	// The legacy code deleted the target first and fell back to the original
	// file when the write failed, which is what makes a read-only working
	// directory work at all.
	st.timecodeFile = target
	if err := tc.SaveTimecode(target); err != nil {
		log.Warn("无法写入修正后timecode，将使用原文件", "file", target, "err", err)
		st.timecodeFile = source
	}
	st.t.LengthMS = tc.TotalLength().Milliseconds()
	st.t.Status.LengthMS = st.t.LengthMS
	return nil
}

// writeQPFile renders the episode-wide qpfile and returns the value the encoder
// is given.
//
// The legacy GenerateQpFile had two shapes: SVT-AV1 takes the list inline
// (--force-key-frames accepts a comma-separated frame list) while x264 and x265
// take a path. Both are reproduced, including the comma join the original built
// by replacing " I" with "f".
func (st *runState) writeQPFile(path string) (string, error) {
	if len(st.chapterFrames) == 0 {
		return "", nil
	}
	return st.writeQPFileValue(path, st.chapterFrames)
}

// writeQPFileValue renders a qpfile for an explicit frame list.
func (st *runState) writeQPFileValue(path string, frames []int64) (string, error) {
	if len(frames) == 0 {
		return "", nil
	}
	if st.p.VideoFormat == "AV1" {
		return chapter.QPFileAV1(frames), nil
	}
	if err := chapter.WriteQPFile(path, chapter.QPFile(frames)); err != nil {
		return "", err
	}
	return path, nil
}

// chapterFrames converts the chapter marks into frame numbers. Mirrors
// ChapterService.GetChapterIFrameInfo for both the CFR and the VFR form.
func chapterFrames(info *chapter.Info, vsInfo model.VSVideoInfo, tc *Timecode) []int64 {
	frames := make([]int64, 0, info.Count())
	for _, c := range info.Chapters {
		if tc != nil {
			frames = append(frames, tc.FrameNumberFromTime(c.Time))
			continue
		}
		ms := c.Time.Milliseconds()
		frames = append(frames, int64(float64(ms)/1000.0*vsInfo.FPS+0.5))
	}
	return frames
}

// videoInfo builds the VideoInfo the muxers read. It mirrors the
// `new VideoInfo(fpsNum, fpsDen, timeCodeFile, qpFile, chapterIFrameInfo)`
// construction at the end of DoPreparation.
func (st *runState) videoInfo() model.VideoInfo {
	info := model.NewVideoInfo()
	info.FpsNum = st.vsInfo.FpsNum
	info.FpsDen = st.vsInfo.FpsDen
	if st.timecodeFile != "" {
		info.TimeCodeFile = model.NewFileRef(st.timecodeFile)
	}
	info.IFrames = append([]int64(nil), st.chapterFrames...)
	return info
}

// videoTrack builds the main video track for a produced file.
func (st *runState) videoTrack(path string) *model.VideoTrack {
	return &model.VideoTrack{
		Track: model.Track{
			File:      model.NewFileRef(path),
			Info:      model.NewInfo(),
			TrackType: model.TrackTypeVideo,
		},
		Video: st.videoInfo(),
	}
}

// encoderTool returns the toolchain key of the profile's encoder.
func (st *runState) encoderTool() string {
	switch profile.EncoderType(st.p.EncoderType) {
	case profile.EncoderX264:
		return toolchain.ToolX264
	case profile.EncoderSVTAV1:
		return toolchain.ToolSVTAV1
	default:
		return toolchain.ToolX265
	}
}

// wholeVideoPart describes the single encode of a normal task: every frame, one
// output file, no part number.
func (st *runState) wholeVideoPart() part {
	p := part{
		id:       -1,
		reEncode: true,
		frames:   model.NewSliceInfo(0, model.OpenEnded),
	}
	p.output = st.partOutputPath(p)
	return p
}

// planParts lays out the re-encode parts. It mirrors the first loop of
// GenerateReEncodeJob: the gaps between the requested slices become "copied"
// parts, the requested slices become "encoded" ones, and a trailing gap becomes a
// final copied part.
//
// The numbering is the legacy numbering, and it is what the `_partN` file names
// and the operator-facing "Part N/M" status use.
//
// The per-part qpfile is written here rather than lazily, because the legacy loop
// wrote it while it built the layout and File.WriteAllText threw on failure. A
// part whose chapter marks cannot be written must fail the task, not run without
// them.
func (st *runState) planParts() error {
	var parts []part
	var prevEnd int64
	partID := 0

	for i, s := range st.cfg.ReEncodeSliceArray {
		if i == 0 {
			if s.Begin != 0 {
				parts = append(parts, st.copiedPart(partID, model.NewSliceInfo(0, s.Begin)))
				partID++
			}
		} else {
			parts = append(parts, st.copiedPart(partID, model.NewSliceInfo(prevEnd, s.Begin)))
			partID++
		}

		encoded := part{
			id:       partID,
			reEncode: true,
			frames:   s,
		}
		qp, err := st.partQPFile(s, partID)
		if err != nil {
			return err
		}
		encoded.qpValue = qp
		encoded.output = st.partOutputPath(encoded)
		parts = append(parts, encoded)
		partID++
		prevEnd = s.End
	}
	if prevEnd != st.frames {
		parts = append(parts, st.copiedPart(partID, model.NewSliceInfo(prevEnd, st.frames)))
	}
	st.parts = parts
	return nil
}

// copiedPart builds a part that is cut out of the old release rather than
// encoded.
func (st *runState) copiedPart(partID int, frames model.SliceInfo) part {
	p := part{id: partID, reEncode: false, frames: frames}
	p.output = st.partOutputPath(p)
	return p
}

// partOutputPath is the encoder's destination for a part:
//
//	profile.WorkingPathPrefix + "_partN" + the codec's extension
//
// (ExecuteTaskService.GenerateVideoJob:418-446). A non-re-encode task has no
// "_partN" suffix.
func (st *runState) partOutputPath(p part) string {
	prefix := st.p.WorkingPathPrefix
	if !st.reEncode() {
		return prefix + codecExtension(st.p.VideoFormat, st.container())
	}
	return prefix + "_part" + itoa(p.id) + codecExtension(st.p.VideoFormat, st.container())
}

// partMuxPath is where a part's single-video container goes:
//
//	profile.WorkingPathPrefix + "_partN." + codecString.lower()
//
// (ExecuteTaskService.GenerateMuxJob:472). The codec string is the MuxJob's
// container format, not the video format: a SingleVideoMuxer is an
// MkvmergeMuxer, so the intermediate container is always Matroska even for an
// MP4 profile. That is why a re-encode of an MP4 profile would drive mkvmerge
// (REPORTS-A12-lsmash.md §4); the wizard refuses that combination before a task
// is created, and the pipeline reproduces the naming rather than "fixing" it.
func (st *runState) partMuxPath(p part) string {
	return st.p.WorkingPathPrefix + "_part" + itoa(p.id) + "." + lowerASCII(st.container())
}

// appendOutputPath is where the joined video goes:
//
//	profile.WorkingPathPrefix + "_all." + codecString.lower()
//
// (ExecuteTaskService.GenerateMuxJob:485). See partMuxPath for why the
// container format, and not the video format, is the extension.
func (st *runState) appendOutputPath() string {
	return st.p.WorkingPathPrefix + "_all." + lowerASCII(st.container())
}

// finalOutputPath is the deliverable:
//
//	Path.Combine(Path.GetDirectoryName(profile.OutputPathPrefix), task.OutputFile)
//
// where task.OutputFile is TaskDetail.UpdateOutputFileName. Both halves live in
// internal/jobproc/mux/mkvepisode, which ports the .NET path helpers precisely
// (Path.GetDirectoryName and FileInfo.Name have edge cases around roots and
// trailing separators), so the expression is built from those rather than
// re-derived.
func (st *runState) finalOutputPath() string {
	name := mkvepisode.FileName(st.inputPath(), st.container(), st.p.VideoFormat)
	return mkvepisode.OutputPath(st.p.OutputPathPrefix, name)
}

// partQPFile derives the qpfile of one encoded part from the episode-wide
// chapter I-frames. Mirrors the second half of GenerateReEncodeJob's layout loop:
// only the chapter marks that fall inside the part survive, and they are rebased
// onto the part's own frame numbering.
//
// A part with no marks inside it has no qpfile, which is the legacy
// `index == null` case; a write failure is reported, because the legacy
// File.WriteAllText threw there.
func (st *runState) partQPFile(s model.SliceInfo, partID int) (string, error) {
	if len(st.chapterFrames) == 0 {
		return "", nil
	}
	idx, ok := iframe.IFrameInfo(st.chapterFrames).FindInRangeIndex(s)
	if !ok {
		return "", nil
	}
	subset := iframe.IFrameInfo(st.chapterFrames).InRange(idx)
	rebased := make([]int64, 0, len(subset))
	for _, f := range subset {
		rebased = append(rebased, f-s.Begin)
	}
	path := st.p.WorkingPathPrefix + "_part" + itoa(partID) + ".qpf"
	return st.writeQPFileValue(path, rebased)
}

// partRanges returns the parts' frame ranges for the task record.
func (st *runState) partRanges() []model.SliceInfo {
	out := make([]model.SliceInfo, 0, len(st.parts))
	for _, p := range st.parts {
		out = append(out, p.frames)
	}
	return out
}

// partFiles returns the part files in playback order, for the append step.
func (st *runState) partFiles() []string {
	out := make([]string, 0, len(st.parts))
	for _, p := range st.parts {
		out = append(out, p.file.Resolve(st.opts.Caps.Volumes))
	}
	return out
}

// appendOptions builds the append step's options.
func (st *runState) appendOptions(mkvmergePath string, prio proc.Priority) simple.AppendVideoOptions {
	return simple.AppendVideoOptions{
		Options: simple.Options{
			Mkvmerge:   mkvmergePath,
			Output:     st.appendOutputPath(),
			SourceFile: st.inputPath(),
			Priority:   prio,
		},
		Slices:       st.partFiles(),
		TimecodeFile: st.timecodeFile,
	}
}

// enqueueAudio builds the audio jobs. Mirrors GenerateAudioJob: a track whose
// mux option is Default, Mka or External is encoded; ExtractOnly and Skip are
// not.
func (st *runState) enqueueAudio() {
	for i := range st.srcAudio {
		track := st.srcAudio[i]
		switch track.Info.Mux {
		case model.MuxOptionDefault, model.MuxOptionMka, model.MuxOptionExternal:
		default:
			continue
		}
		input := track.File.Resolve(st.opts.Caps.Volumes)
		output := replaceExt(input, "."+lowerASCII(track.Audio.OutputCodec))
		st.audioJobs = append(st.audioJobs, audioJob{
			info:   track.Audio,
			input:  input,
			output: output,
		})
	}
}

// routeAudioFiles adds every encoded audio track to the container its mux option
// names. Mirrors the tail of DoAllJobs' AudioJob branch, including the External
// case, which only tags the file.
func (st *runState) routeAudioFiles() {
	for i := range st.audioJobs {
		job := &st.audioJobs[i]
		if !job.done {
			continue
		}
		track := &model.AudioTrack{
			Track: model.Track{
				File:      job.file,
				Info:      job.info.Info,
				TrackType: model.TrackTypeAudio,
			},
			Audio: job.info,
		}
		switch job.info.Mux {
		case model.MuxOptionDefault:
			if err := st.media.AddAudioTrack(track); err != nil {
				log.Warn("无法添加音轨", "err", err)
			}
		case model.MuxOptionMka:
			if err := st.mka.AddAudioTrack(track); err != nil {
				log.Warn("无法添加音轨", "err", err)
			}
		case model.MuxOptionExternal:
			if err := addCRC32(job.file.Resolve(st.opts.Caps.Volumes)); err != nil {
				log.Warn("无法为外置音轨添加CRC32", "file", job.file.String(), "err", err)
			}
		}
	}
}

// addSubtitles routes every extracted subtitle track. Mirrors AddSubtitle: a
// Default track goes into the main file, an Mka one into the external audio
// file, and an External one is only tagged with its CRC32.
func (st *runState) addSubtitles() {
	for i := range st.srcSubs {
		track := st.srcSubs[i]
		switch track.Info.Mux {
		case model.MuxOptionDefault:
			if err := st.media.AddSubtitleTrack(track); err != nil {
				log.Warn("无法添加字幕轨", "err", err)
			}
		case model.MuxOptionMka:
			if err := st.mka.AddSubtitleTrack(track); err != nil {
				log.Warn("无法添加字幕轨", "err", err)
			}
		case model.MuxOptionExternal:
			if err := addCRC32(track.File.Resolve(st.opts.Caps.Volumes)); err != nil {
				log.Warn("无法为外置字幕轨添加CRC32", "file", track.File.String(), "err", err)
			}
		}
	}
}

// recordDeliverable records the finished container: its name, its size for the
// UI, and the fact that the task produced something. Mirrors the NewMkvEpisode,
// NewMp4Episode and MergeOldRemux branches of DoAllJobs.
//
// The CRC32 tag is part of the recorded name. The legacy code called
// OKEFile.AddCRC32 and then read GetFileName and GetFileSize off the same
// FileInfo, and .NET's FileInfo.MoveTo updates the object to point at the new
// path — so the task carried the tagged name and the size of the tagged file.
// Reproducing that matters because the operator copies the name out of the UI.
func (st *runState) recordDeliverable(path string) error {
	// The tagged name has to be computed before the rename: afterwards the
	// original path no longer exists and the sum cannot be read.
	tagged := crc32Name(path)
	if err := addCRC32(path); err != nil {
		log.Warn("无法为成品添加CRC32", "file", path, "err", err)
		tagged = path
	}
	ref := model.NewFileRef(tagged)
	st.deliverable = ref
	st.t.Output = ref
	st.t.Status.Output = ref
	st.t.Status.BitRate = humanReadableFilesize(st.fileSizeOf(ref), 2)
	return nil
}

// crc32Name returns the name addCRC32 produces for a path. It is the identity
// function when the file cannot be read, which keeps a caller from inventing a
// tag for a file that is not there.
func crc32Name(path string) string {
	sum, err := fileCRC32(path)
	if err != nil {
		return path
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return filepath.Join(dir, stem+" ["+upperHex8(sum)+"]"+filepath.Ext(base))
}

// whiteList is the set of files the cleaner must never delete: the script and
// the deliverable.
//
// It mirrors the legacy call site, which passed `{json.InputScript,
// inputFile + ".lwi"}` (WizardWindow.xaml.cs:244); the cleaner appends the input
// file itself. The extracted audio tracks are deliberately *not* listed: they are
// exactly what the sweep exists to reclaim, and the legacy list did not protect
// them either.
//
// The deliverable is listed under both its tagged and untagged names, because an
// operator may have renamed the file back before the sweep ran.
func (st *runState) whiteList() []string {
	paths := []string{st.p.InputScript}
	if !st.deliverable.IsZero() {
		path := st.deliverable.Resolve(st.opts.Caps.Volumes)
		paths = append(paths, path)
		if tagged := crc32Name(path); tagged != path {
			paths = append(paths, tagged)
		}
	}
	return paths
}

// videoPath returns the main video's path, which the RPC check compares against
// the script.
func (st *runState) videoPath() string {
	if st.media.Video == nil {
		return ""
	}
	return st.media.Video.File.Resolve(st.opts.Caps.Volumes)
}

// commit publishes the state model.StatusEvent cannot carry. The worker pool
// hands the pipeline a copy of the queued task, so the fields written directly
// on st.t would otherwise be lost when the run ends.
func (st *runState) commit() {
	if st.opts.UpdateTask == nil {
		return
	}
	if err := st.opts.UpdateTask(st.t.ID, func(t *model.Task) {
		t.Output = st.t.Output
		t.SliceParts = st.t.SliceParts
		t.LengthMS = st.t.LengthMS
		t.Status.Output = st.t.Status.Output
		t.Status.Chapter = st.t.Status.Chapter
		t.Status.RPC = st.t.Status.RPC
		t.Status.RPCOutput = st.t.Status.RPCOutput
		t.Status.LengthMS = st.t.Status.LengthMS
		t.Status.Frames = st.t.Status.Frames
		t.Status.BitRate = st.t.Status.BitRate
	}); err != nil {
		log.Debug("无法写回任务状态", "task", st.t.ID, "err", err)
	}
}

// codecExtension is the extension a part's encoded stream gets, per
// GenerateVideoJob:418-446.
//
// The AVC case is the legacy quirk that a partial encode of an MKV profile
// writes a `_.mkv` container directly instead of a raw .h264 stream, because
// x264 can mux Matroska itself. The name keeps the underscore that made the
// intermediate file sort next to the source in the legacy directory listing.
func codecExtension(videoFormat, container string) string {
	switch videoFormat {
	case "AVC":
		if container == string(profile.ContainerMKV) {
			return "_.mkv"
		}
		return ".h264"
	case "AV1":
		return ".ivf"
	default:
		return ".hevc"
	}
}

// lowerASCII is the ASCII-only lowercase the legacy ToLower() applied to file
// extensions and format names.
func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// itoa renders an int without pulling strconv into every call site.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// humanReadableFilesize renders a byte count the way
// CommandlineVideoEncoder.HumanReadableFilesize did:
//
//	Math.Round(size * Math.Pow(10, digit)) / Math.Pow(10, digit) + " " + units[i]
//
// Three details of that expression are reproduced because the result is a
// display string the operator and the regression fixtures compare against:
//
//   - the units are binary (1024), and the loop divides until the value is
//     below 1024, so 1024 bytes is "1 KB";
//   - Math.Round(double) rounds halfway cases to the *even* neighbour;
//   - the result is formatted with .NET's default double formatting, which is
//     the shortest representation that round-trips, so a round number keeps no
//     trailing zeros: 1536 bytes is "1.5 KB" and not "1.50 KB".
func humanReadableFilesize(size int64, digit int) string {
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	mod := 1024.0
	v := float64(size)
	i := 0
	for v >= mod && i < len(units)-1 {
		v /= mod
		i++
	}
	scale := 1.0
	for range digit {
		scale *= 10
	}
	rounded := math.RoundToEven(v*scale) / scale
	return formatShortest(rounded) + " " + units[i]
}

// formatShortest renders a float the way .NET's default double formatting does:
// the shortest decimal that round-trips, without an exponent for the magnitudes
// a file size can produce.
func formatShortest(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
