package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// The pipeline drives a dozen external tools, so a test that ran the real ones
// would need VapourSynth, an encoder, mkvmerge and a source file. Instead the
// suite points every tool at the test binary itself and has the child decide
// what to do from its own argument list; internal/jobproc/demux/ffmpeg uses the
// same pattern for the same reason.
//
// The fakes are deliberately *real processes*: these tests exist to pin the
// stage order, the argument shapes and the error mapping, and a stub that
// returned canned values would exercise none of the process lifecycle.

const (
	// fakeEnvMarker identifies a child that must behave as a tool.
	fakeEnvMarker = "OKEGUIDX_ENGINE_FAKE"
	// fakeEnvSpec names the JSON file describing every fake tool's behaviour.
	fakeEnvSpec = "OKEGUIDX_ENGINE_FAKE_SPEC"
	// fakeEnvArgsLog is the file every fake tool appends its argv to.
	fakeEnvArgsLog = "OKEGUIDX_ENGINE_ARGS_LOG"
	// fakeEnvWorkingPrefix mirrors the profile's WorkingPathPrefix, which the
	// demuxer needs to reproduce its output file names.
	fakeEnvWorkingPrefix = "OKEGUIDX_ENGINE_WORKING_PREFIX"
	// fakeEnvSource mirrors the task's source file.
	fakeEnvSource = "OKEGUIDX_ENGINE_SOURCE"
)

// Tool roles a fake child can detect from its argv.
const (
	roleVSPipeInfo   = "vspipe-info"
	roleVSPipeIFrame = "vspipe-iframe"
	roleVSPipeRPC    = "vspipe-rpc"
	roleVSPipeEncode = "vspipe-encode"
	roleEac3to       = "eac3to"
	roleFFprobe      = "ffprobe"
	roleFFmpegDemux  = "ffmpeg-demux"
	roleFFmpegVolume = "ffmpeg-volume"
	roleEncoder      = "encoder"
	roleMkvmerge     = "mkvmerge"
	roleLSmash       = "l-smash"
	roleQAAC         = "qaac"
	roleTChapter     = "tchapter"
)

// fakeSpec is the JSON document the test writes for its children. Every field is
// optional; an absent one means "behave like a successful tool".
type fakeSpec struct {
	// VSPipeInfo is the text `vspipe --info` prints for the encode script.
	VSPipeInfo string `json:"vspipe_info"`
	// IFrameList is the "IFrameList: [...]" line the iframe script prints.
	IFrameList string `json:"iframe_list"`
	// Tracks are the tracks the demuxer reports.
	Tracks []fakeTrack `json:"tracks"`
	// EncoderProgress is the encoder line that ends a run.
	EncoderProgress string `json:"encoder_progress"`
	// RPCLines are the RPCOUT lines the RPC script prints.
	RPCLines []string `json:"rpc_lines"`
	// ChapterJSON is the document `tchapter info` prints.
	ChapterJSON string `json:"chapter_json"`
	// VolumeByFile maps an audio file's base name to the two level lines the
	// volume checker prints for it. A file with no entry reports audible
	// levels, so only the tracks a test wants marked silent need one.
	VolumeByFile map[string]string `json:"volume_by_file"`
	// BlockTool makes one role sleep until it is killed, which is how the
	// cancellation test keeps a stage alive. Empty means no role blocks.
	BlockTool string `json:"block_tool"`
	// FailTool names a role that exits non-zero.
	FailTool string `json:"fail_tool"`
	// FailCode is that role's exit code.
	FailCode int `json:"fail_code"`
}

// fakeTrack is one track the demuxer reports.
type fakeTrack struct {
	// Codec is the ffprobe codec name, e.g. "flac", "ass", "h264".
	Codec string `json:"codec"`
	// Index is the track's number in the listing.
	Index int `json:"index"`
	// Type is the ffprobe codec_type: audio, subtitle, video or chapter.
	Type string `json:"type"`
	// Language is the track's language tag.
	Language string `json:"language"`
	// Silent makes the extracted file empty, which the demuxer reports as a
	// silent track.
	Silent bool `json:"silent"`
}

// TestMain turns the test binary into every external tool the pipeline drives.
//
// The pipeline builds its own argument lists, so a child cannot be told to run
// only a helper test: Go's flag parser rejects an encoder's arguments outright.
// The child is identified by an environment marker instead.
func TestMain(m *testing.M) {
	if os.Getenv(fakeEnvMarker) != "" {
		runFakeTool()
		return
	}
	log.SetLevel("ERROR")
	os.Exit(m.Run())
}

// runFakeTool is the child body. It never returns.
func runFakeTool() {
	spec := loadFakeSpec()
	role := detectRole(os.Args[1:])
	appendArgsLog(role, os.Args[1:])

	if spec.FailTool == role {
		os.Exit(spec.FailCode)
	}
	if spec.BlockTool == role {
		// A plain `select {}` would trip the runtime's deadlock detector and
		// exit the child immediately, which is the opposite of blocking.
		time.Sleep(time.Hour)
	}

	switch role {
	case roleVSPipeInfo:
		os.Stdout.WriteString(spec.VSPipeInfo)
	case roleVSPipeIFrame:
		if spec.IFrameList != "" {
			os.Stderr.WriteString(spec.IFrameList + "\n")
		}
		os.Stdout.WriteString("Frames: 100\n")
	case roleVSPipeRPC:
		for _, line := range spec.RPCLines {
			os.Stderr.WriteString(line + "\n")
		}
		os.Stderr.WriteString("Output 100 frames\n")
	case roleVSPipeEncode:
		// The producer's stdout belongs to the encoder, so it only has to stay
		// alive long enough for the encoder to finish.
		os.Exit(0)
	case roleEac3to:
		runFakeEac3to(spec, os.Args[1:])
	case roleFFprobe:
		os.Stdout.WriteString(probeJSON(spec))
	case roleFFmpegDemux:
		runFakeFFmpegDemux(spec)
	case roleFFmpegVolume:
		os.Stderr.WriteString(volumeLines(spec, os.Args[1:]))
	case roleEncoder:
		// The encoder reads the y4m stream the producer writes. Draining stdin
		// is not optional: a fake that exits immediately makes a real vspipe
		// fail with a broken pipe, which the base class reports as a vspipe
		// error.
		drainStdin()
		if spec.EncoderProgress != "" {
			os.Stderr.WriteString(spec.EncoderProgress + "\n")
		}
		writeOutputs(os.Args[1:])
	case roleMkvmerge, roleLSmash:
		os.Stdout.WriteString("Progress: 50%\n")
		os.Stdout.WriteString("Muxing took 1 second.\n")
		writeFlaggedOutputs(os.Args[1:])
	case roleQAAC:
		os.Stderr.WriteString("[50.0%] 0:00:02.000/0:00:05.000\n")
		os.Stderr.WriteString("Done.\n")
		writeOutputs(os.Args[1:])
	case roleTChapter:
		os.Stdout.WriteString(spec.ChapterJSON)
	}
	os.Exit(0)
}

// detectRole classifies a child by its argument list. It is the only way a fake
// can tell which tool it is, because every tool resolves to the same binary.
func detectRole(args []string) string {
	joined := " " + strings.Join(args, " ") + " "
	switch {
	case strings.Contains(joined, " --file-format mp4 "):
		return roleLSmash
	case strings.Contains(joined, " --ui-language en "):
		return roleMkvmerge
	case strings.Contains(joined, " info ") || strings.HasPrefix(joined, " info "):
		return roleTChapter
	case strings.Contains(joined, " -show_streams "):
		return roleFFprobe
	case strings.Contains(joined, " astats="):
		return roleFFmpegVolume
	case strings.Contains(joined, " -nostdin "):
		return roleFFmpegDemux
	case strings.Contains(joined, " -log="):
		return roleEac3to
	case strings.Contains(joined, " --info "):
		// Both vspipe probes ask with --info; the script they name says which.
		if strings.Contains(joined, iframeScriptSuffix) {
			return roleVSPipeIFrame
		}
		return roleVSPipeInfo
	case strings.HasSuffix(joined, " . "):
		// The RPC check runs `vspipe "<script>" .`.
		return roleVSPipeRPC
	case isVSPipeEncode(joined):
		// The encode pipeline's producer: vspipe reads the script and writes
		// y4m to stdout. Its stdout belongs to the encoder, so it must not be
		// mistaken for the encoder itself.
		return roleVSPipeEncode
	case strings.Contains(joined, " -q 2 ") && strings.Contains(joined, " -o "):
		return roleQAAC
	default:
		return roleEncoder
	}
}

// isVSPipeEncode reports whether the argument list is the vspipe half of an
// encode: it names the script and ends with the stdout marker.
func isVSPipeEncode(joined string) bool {
	return strings.Contains(joined, ".vpy") && strings.HasSuffix(joined, " - ")
}

// iframeScriptSuffix is the suffix the iframe processor names its script with,
// repeated here because the package constant is unexported from the pipeline's
// point of view.
const iframeScriptSuffix = "_iframe.vpy"

// runFakeEac3to serves both eac3to passes: the analysis pass lists the tracks,
// the extraction pass writes the files.
func runFakeEac3to(spec fakeSpec, args []string) {
	if !hasExtraction(args) {
		os.Stdout.WriteString(eacListing(spec))
		return
	}
	for _, t := range spec.Tracks {
		writeTrackFile(spec, t)
	}
	os.Stdout.WriteString("Progress: 100%\n")
}

// volumeLines returns the level report for the file an ffmpeg volume run reads.
// The demuxer treats "mean < -70 and max < -30" as silence, so a test marks a
// track silent by giving it a very low mean.
func volumeLines(spec fakeSpec, args []string) string {
	for _, a := range args {
		if report, ok := spec.VolumeByFile[filepath.Base(a)]; ok {
			return report
		}
	}
	return "RMS level dB: -20.5\nPeak level dB: -3.0\n"
}

// runFakeFFmpegDemux serves the ffmpeg demuxer's extraction pass. The analysis
// pass goes through ffprobe and never reaches here.
func runFakeFFmpegDemux(spec fakeSpec) {
	writeLastArg(os.Args[1:])
	os.Stdout.WriteString("out_time_us=1000000\nprogress=end\n")
}

// hasExtraction reports whether an eac3to argument list names an output track,
// which is the "N:" pair the extraction pass adds.
func hasExtraction(args []string) bool {
	for _, a := range args {
		if strings.HasSuffix(a, ":") {
			return true
		}
	}
	return false
}

// writeTrackFile creates the file the demuxer would have written for a track.
// The demuxer rejects a zero-byte file outright, so a "silent" track is written
// with content and reported silent by the volume check instead.
func writeTrackFile(_ fakeSpec, t fakeTrack) {
	writeFilled(trackOutput(t))
}

// trackOutput reproduces TrackInfo.OutFileName:
//
//	{dir(WorkingPathPrefix)}/{stem(SourceFile)}_{Index}{ext}
func trackOutput(t fakeTrack) string {
	prefix := os.Getenv(fakeEnvWorkingPrefix)
	source := os.Getenv(fakeEnvSource)
	dir := filepath.Dir(prefix)
	stem := strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	return filepath.Join(dir, stem+"_"+itoa(t.Index)+"."+trackExt(t.Codec))
}

// trackExt is the extension a track's file gets, per TrackInfo.FileExtension.
func trackExt(codec string) string {
	switch codec {
	case "flac", "aac", "ac3", "eac3", "dts", "opus":
		return codec
	case "pgs":
		return "sup"
	case "ass":
		return "ass"
	case "srt":
		return "srt"
	default:
		return "bin"
	}
}

// probeJSON renders the ffprobe document for the configured tracks.
func probeJSON(spec fakeSpec) string {
	streams := make([]map[string]any, 0, len(spec.Tracks))
	var chapters []map[string]any
	for _, t := range spec.Tracks {
		if t.Type == "chapter" {
			chapters = append(chapters, map[string]any{
				"id": 1, "start_time": "0.000000", "end_time": "5.000000",
			})
			continue
		}
		streams = append(streams, map[string]any{
			"index":          t.Index,
			"codec_name":     t.Codec,
			"codec_type":     t.Type,
			"sample_rate":    "48000",
			"channels":       2,
			"bit_rate":       "192000",
			"start_time":     "0.000000",
			"duration":       "5.000000",
			"disposition":    map[string]any{"attached_pic": 0},
			"tags":           map[string]any{"language": t.Language},
			"avg_frame_rate": "0/0",
			"r_frame_rate":   "0/0",
		})
	}
	doc := map[string]any{
		"streams": streams,
		"format":  map[string]any{"duration": "5.000000", "format_name": "matroska,webm"},
	}
	if len(chapters) > 0 {
		doc["chapters"] = chapters
	}
	out, _ := json.Marshal(doc)
	return string(out)
}

// eacListing renders the eac3to track listing for the configured tracks.
func eacListing(spec fakeSpec) string {
	var b strings.Builder
	b.WriteString("eac3to (v3.36)\n")
	for _, t := range spec.Tracks {
		b.WriteString(itoa(t.Index))
		b.WriteString(": ")
		b.WriteString(eacDescription(t))
		b.WriteString("\n")
	}
	b.WriteString("Duration: 0:00:05\n")
	return b.String()
}

// eacDescription renders the part of an eac3to listing line after the index. The
// spelling is the one internal/jobproc/demux/eac3to matches against.
func eacDescription(t fakeTrack) string {
	var name string
	switch t.Codec {
	case "flac":
		name = "FLAC, 2.0 channels, 48khz"
	case "aac":
		name = "AAC, 2.0 channels, 48khz"
	case "ac3":
		name = "AC3, 2.0 channels, 48khz"
	case "eac3":
		name = "EAC3, 2.0 channels, 48khz"
	case "dts":
		name = "DTS, 2.0 channels, 48khz"
	case "opus":
		name = "OPUS, 2.0 channels, 48khz"
	case "pgs":
		name = "Subtitle (PGS)"
	case "ass":
		name = "Subtitle (ASS)"
	case "srt":
		name = "Subtitle (SRT)"
	case "chapter":
		name = "Chapters, 2 chapters"
	case "h264":
		name = "h264/AVC, 1920x1080 23.976p"
	default:
		name = strings.ToUpper(t.Codec)
	}
	if t.Language != "" {
		name += ", " + t.Language
	}
	return name
}

// loadFakeSpec reads the JSON the test wrote for its children.
func loadFakeSpec() fakeSpec {
	raw, err := os.ReadFile(os.Getenv(fakeEnvSpec))
	if err != nil {
		return fakeSpec{}
	}
	var spec fakeSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return fakeSpec{}
	}
	return spec
}

// appendArgsLog records the child's argv so a test can assert what the pipeline
// asked for. O_APPEND makes concurrent children safe.
func appendArgsLog(role string, args []string) {
	path := os.Getenv(fakeEnvArgsLog)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.WriteString(role + "\t" + strings.Join(args, " ") + "\n")
}

// drainStdin reads standard input to EOF and discards it. A fake consumer of a
// pipeline must drain, or the producer dies on a broken pipe.
func drainStdin() {
	buf := make([]byte, 64*1024)
	for {
		if _, err := os.Stdin.Read(buf); err != nil {
			return
		}
	}
}

// writeLastArg creates the file the argument list names last. Every tool this
// suite fakes writes its output as the final argument, which is how the wrappers
// build their command lines.
func writeLastArg(args []string) {
	if len(args) == 0 {
		return
	}
	writeFilled(args[len(args)-1])
}

// writeOutputs creates the file a fake tool is asked to produce. The wrappers
// name the destination differently, so every argument that looks like a path is
// created: the encoder's base class appends "-o <out>", mkvmerge uses
// "--output <out>", and l-smash's muxer takes "-o <out>" after its format
// option. Creating every candidate is harmless and keeps the fakes independent
// of each wrapper's spelling.
func writeOutputs(args []string) {
	for i, a := range args {
		if isOutputFlag(a) && i+1 < len(args) {
			writeFilled(args[i+1])
		}
	}
	writeLastArg(args)
}

// writeFlaggedOutputs creates only the files a muxer names with a flag.
//
// The last argument is deliberately not treated as an output here: mkvmerge's
// argument list ends with the --track-order value, and an append run ends with
// the --append-to value, so writing "the last argument" would litter the working
// directory with files named after an option's value.
func writeFlaggedOutputs(args []string) {
	for i, a := range args {
		if isOutputFlag(a) && i+1 < len(args) {
			writeFilled(args[i+1])
		}
	}
}

// isOutputFlag reports whether a flag names the destination file.
func isOutputFlag(a string) bool {
	switch a {
	case "-o", "--output", "-b":
		return true
	default:
		return false
	}
}

// writeFilled creates a non-empty file, which is what every "the tool produced
// something" check needs.
func writeFilled(path string) {
	if path == "" || strings.HasPrefix(path, "-") {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte("fake"), 0o600)
}

// ---------------------------------------------------------------------------
// Test-side harness
// ---------------------------------------------------------------------------

// fakeTools installs the test binary as every tool the pipeline may need.
type fakeTools struct {
	caps     node.Capabilities
	dir      string
	specPath string
	argsLog  string
	mu       sync.Mutex
	spec     fakeSpec
}

// newFakeTools builds a fake toolchain in dir. caps configures which features
// the node advertises; the paths always point at the test binary.
func newFakeTools(t *testing.T, dir string, caps node.Capabilities, spec fakeSpec) *fakeTools {
	t.Helper()

	ft := &fakeTools{
		dir:      dir,
		specPath: filepath.Join(dir, "fake-spec.json"),
		argsLog:  filepath.Join(dir, "args.log"),
		spec:     spec,
	}
	ft.writeSpec(t)

	// Every tool resolves to the test binary. The role is recovered from the
	// argument list, so the path only has to exist and be executable.
	self := os.Args[0]
	for _, name := range []string{
		toolchain.ToolVSPipe, toolchain.ToolX264, toolchain.ToolX265,
		toolchain.ToolSVTAV1, toolchain.ToolFFmpeg, toolchain.ToolFFprobe,
		toolchain.ToolMkvmerge, toolchain.ToolMkvextract, toolchain.ToolLSmash,
		toolchain.ToolQAAC, toolchain.ToolEac3to, toolchain.ToolTChapter,
		toolchain.ToolRPCChecker,
	} {
		caps.Tools[name] = node.ToolInfo{Path: self}
	}
	if caps.Volumes == nil {
		caps.Volumes = map[string]string{}
	}
	if _, ok := caps.Volumes[model.LocalVolume]; !ok {
		caps.Volumes[model.LocalVolume] = ""
	}
	if caps.NUMANodes < 1 {
		caps.NUMANodes = 1
	}
	ft.caps = caps
	return ft
}

// writeSpec publishes a new spec for the children. It is what a test calls to
// change a fake's behaviour between runs.
func (ft *fakeTools) writeSpec(t *testing.T) {
	t.Helper()
	ft.mu.Lock()
	spec := ft.spec
	ft.mu.Unlock()
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal fake spec: %v", err)
	}
	if err := os.WriteFile(ft.specPath, raw, 0o600); err != nil {
		t.Fatalf("write fake spec: %v", err)
	}
}

// childEnv is the environment a task's children inherit. It carries the marker,
// the spec path, the argument log and the two paths the demuxer needs.
//
// The pipeline starts children with the process environment (no wrapper takes an
// explicit one), so a test installs this with t.Setenv and the children inherit
// it.
func (ft *fakeTools) childEnv(workingPrefix, source string) []string {
	return []string{
		fakeEnvMarker + "=1",
		fakeEnvSpec + "=" + ft.specPath,
		fakeEnvArgsLog + "=" + ft.argsLog,
		fakeEnvWorkingPrefix + "=" + workingPrefix,
		fakeEnvSource + "=" + source,
	}
}

// installEnv puts the fake environment into the test process, so every child the
// pipeline starts inherits it. It must be called before the pipeline runs.
func (ft *fakeTools) installEnv(t *testing.T, workingPrefix, source string) {
	t.Helper()
	for _, kv := range ft.childEnv(workingPrefix, source) {
		key, value, _ := strings.Cut(kv, "=")
		t.Setenv(key, value)
	}
}

// options builds the pipeline options a test runs with.
func (ft *fakeTools) options(t *testing.T) PipelineOptions {
	t.Helper()
	return PipelineOptions{
		Caps:     ft.caps,
		Priority: proc.DefaultPriority,
		Numa:     platform.NewNumaWithCount(1),
		UpdateTask: func(model.TaskID, func(*model.Task)) error {
			return nil
		},
	}
}

// recordedArgs returns every argv the children recorded, one per line.
func (ft *fakeTools) recordedArgs(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile(ft.argsLog)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read args log: %v", err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// roles returns the tool roles the children recorded, in order.
func (ft *fakeTools) roles(t *testing.T) []string {
	t.Helper()
	lines := ft.recordedArgs(t)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		role, _, _ := strings.Cut(line, "\t")
		out = append(out, role)
	}
	return out
}

// callsFor returns the recorded argv lines of one tool role.
func (ft *fakeTools) callsFor(t *testing.T, role string) []string {
	t.Helper()
	lines := ft.recordedArgs(t)
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		r, args, _ := strings.Cut(line, "\t")
		if r == role {
			out = append(out, args)
		}
	}
	return out
}

// hasRole reports whether a role ran at least once.
func (ft *fakeTools) hasRole(t *testing.T, role string) bool {
	t.Helper()
	for _, r := range ft.roles(t) {
		if r == role {
			return true
		}
	}
	return false
}

// indexOfRole returns the position of a role in the recorded order, or -1.
func (ft *fakeTools) indexOfRole(t *testing.T, role string) int {
	t.Helper()
	for i, r := range ft.roles(t) {
		if r == role {
			return i
		}
	}
	return -1
}

// makeProfile writes a profile for dir.
func makeProfile(dir string, mutate func(*profile.Profile)) *profile.Profile {
	prof := &profile.Profile{
		Version:           3,
		VSVersion:         "test",
		ProjectName:       "test",
		EncoderType:       string(profile.EncoderX265),
		VideoFormat:       "HEVC",
		ContainerFormat:   string(profile.ContainerMKV),
		FpsNum:            24000,
		FpsDen:            1001,
		InputScript:       filepath.Join(dir, "test.vpy"),
		WorkingPathPrefix: filepath.Join(dir, "work", "ep01"),
		OutputPathPrefix:  filepath.Join(dir, "out", "ep01"),
		ConfigFilePath:    filepath.Join(dir, "test.json"),
	}
	if mutate != nil {
		mutate(prof)
	}
	return prof
}

// makeTask builds a task that carries its profile directly, which is the
// in-process path.
func makeTask(prof *profile.Profile, cfg *profile.EpisodeConfig) *model.Task {
	t := &model.Task{
		ID:      model.NewTaskID(),
		Name:    prof.ProjectName,
		Profile: prof,
		Inputs:  []model.FileRef{model.NewFileRef(filepath.Join(filepath.Dir(prof.ConfigFilePath), "00001.m2ts"))},
	}
	if cfg != nil {
		t.Config = cfg
	}
	return t
}

// prepare writes the profile file, the script and the source file a task needs,
// so a test can run the whole pipeline against a real directory.
func prepare(t *testing.T, ft *fakeTools, prof *profile.Profile, cfg *profile.EpisodeConfig) *model.Task {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(prof.WorkingPathPrefix), 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(prof.OutputPathPrefix), 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	if err := os.WriteFile(prof.InputScript, []byte("# OKE:INPUTFILE arg=r\"x\"\n"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	raw, err := json.Marshal(prof)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	if err := os.WriteFile(prof.ConfigFilePath, raw, 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	task := makeTask(prof, cfg)
	for _, in := range task.Inputs {
		writeFilled(in.Resolve(ft.caps.Volumes))
	}
	if cfg != nil && cfg.ReEncodeOldFile != "" {
		writeFilled(cfg.ReEncodeOldFile)
	}
	return task
}

// runPipeline runs one task through a fresh LocalExecutor and returns every
// event plus the error the pipeline reported.
func runPipeline(t *testing.T, opts PipelineOptions, task *model.Task) ([]model.StatusEvent, error) {
	t.Helper()
	p := NewPipeline(opts)

	var (
		mu     sync.Mutex
		runErr error
	)
	run := func(ctx context.Context, task *model.Task, events chan<- model.StatusEvent) error {
		err := p.Run(ctx, task, events)
		mu.Lock()
		runErr = err
		mu.Unlock()
		return err
	}

	exec := NewLocalExecutor(opts.Caps, run)
	events, err := exec.Submit(t.Context(), task)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	all := collectEvents(events)
	exec.Wait()

	mu.Lock()
	defer mu.Unlock()
	return all, runErr
}

// collectEvents drains an event channel into a slice.
func collectEvents(ch <-chan model.StatusEvent) []model.StatusEvent {
	var out []model.StatusEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}
