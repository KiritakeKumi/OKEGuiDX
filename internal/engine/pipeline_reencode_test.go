package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// reEncodeSpec is the fake behaviour of a re-encode whose old release has
// I-frames at 0, 100, 200, 300 and 1000 frames.
func reEncodeSpec() fakeSpec {
	spec := normalSpec()
	spec.VSPipeInfo = "Width: 1920\nHeight: 1080\nFrames: 1000\nFPS: 24000/1001 (23.976 fps)\nFormat Name: YUV420P8\nColor Family: YUV\nBits: 8\n"
	spec.IFrameList = "IFrameList: [0, 100, 200, 300]"
	// The encode is a partial one: the aligned part is [100, 300), so 200
	// frames is a complete run.
	spec.EncoderProgress = "encoded 200 frames in 8.00s (25.00 fps), 1000.00 kb/s, Avg QP:20.00"
	return spec
}

// newReEncodePipeline sets up a re-encode task. reExtract selects whether the
// source tracks are re-extracted (true) or taken from the old release (false).
func newReEncodePipeline(t *testing.T, reExtract bool, mutate func(*profile.EpisodeConfig)) (*fakeTools, PipelineOptions, *model.Task) {
	t.Helper()
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), reEncodeSpec())
	oldFile := filepath.Join(dir, "old.mkv")
	cfg := &profile.EpisodeConfig{
		EnableReEncode:     true,
		ReExtractSource:    reExtract,
		ReEncodeOldFile:    oldFile,
		ReEncodeSliceArray: []model.SliceInfo{{Begin: 150, End: 250}},
	}
	if mutate != nil {
		mutate(cfg)
	}
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.IsReEncode = true
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, cfg)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))
	return ft, ft.options(t), task
}

func TestPipelineReEncodeAlignsAndEncodesParts(t *testing.T) {
	ft, opts, task := newReEncodePipeline(t, false, nil)
	_, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// The requested slice [150, 250) must move outwards to [100, 300), leaving
	// a leading copy [0, 100) and a trailing copy [300, 1000).
	//
	// The frame range is expressed on the vspipe side (-s/-e), not on the
	// encoder: vspipe reads the frames and pipes them, so it is the producer
	// that is told where to start and stop.
	encodes := ft.callsFor(t, roleEncoder)
	if len(encodes) != 1 {
		t.Fatalf("the encoder ran %d times, want 1: %v", len(encodes), encodes)
	}
	if !strings.Contains(encodes[0], "_part1.hevc") {
		t.Errorf("the encode output is not the aligned part: %s", encodes[0])
	}
	producers := ft.callsFor(t, roleVSPipeEncode)
	if len(producers) != 1 {
		t.Fatalf("vspipe ran %d times, want 1: %v", len(producers), producers)
	}
	produce := producers[0]
	if !strings.Contains(produce, "-s 100") {
		t.Errorf("the encode does not start at the aligned frame: %s", produce)
	}
	if !strings.Contains(produce, "-e 299") {
		// vspipe's -e is inclusive, so an exclusive end of 300 becomes 299.
		t.Errorf("the encode does not end at the aligned frame: %s", produce)
	}

	// Three parts, so three single-video muxes and one append.
	muxCalls := ft.callsFor(t, roleMkvmerge)
	var appends int
	for _, c := range muxCalls {
		if strings.Contains(c, "--append-to") {
			appends++
		}
	}
	if appends != 1 {
		t.Errorf("the append step ran %d times, want 1: %v", appends, muxCalls)
	}
	// The two copied parts are cut out of the old release with --split.
	var splits int
	for _, c := range muxCalls {
		if strings.Contains(c, "--split parts-frames:") {
			splits++
		}
	}
	if splits != 2 {
		t.Errorf("the copied parts used --split %d times, want 2: %v", splits, muxCalls)
	}
}

func TestPipelineReEncodeSliceOutOfRangeIsRejected(t *testing.T) {
	ft, opts, task := newReEncodePipeline(t, false, func(cfg *profile.EpisodeConfig) {
		// The old release is 1000 frames long; a slice at 1200 cannot exist.
		cfg.ReEncodeSliceArray = []model.SliceInfo{{Begin: 1200, End: 1300}}
	})
	_, err := runPipeline(t, opts, task)
	if err == nil {
		t.Fatal("Run() = nil, want an illegal-slice error")
	}
	if !strings.Contains(err.Error(), "切片") {
		t.Errorf("error = %v, want the legacy slice message", err)
	}
	if ft.hasRole(t, roleEncoder) {
		t.Error("the encoder ran despite the illegal slice")
	}
}

func TestPipelineReEncodeWithoutOldFileIsRejected(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), reEncodeSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.IsReEncode = true
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err == nil {
		t.Fatal("Run() = nil, want a missing-config error")
	}
	if !strings.Contains(err.Error(), "ReEncode") {
		t.Errorf("error = %v, want it to mention the missing re-encode config", err)
	}
}

func TestPipelineReEncodeWithReExtractDemuxes(t *testing.T) {
	ft, opts, task := newReEncodePipeline(t, true, nil)
	_, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// ReExtractSource means the source is demuxed and its tracks muxed into the
	// new container, so the final mux is an episode mux rather than a merge.
	if !ft.hasRole(t, roleEac3to) {
		t.Errorf("the demuxer did not run for ReExtractSource: %v", ft.roles(t))
	}
	muxCalls := ft.callsFor(t, roleMkvmerge)
	if len(muxCalls) == 0 {
		t.Fatal("the muxer never ran")
	}
	final := muxCalls[len(muxCalls)-1]
	if strings.Contains(final, "--no-video") {
		t.Errorf("ReExtractSource produced a merge instead of an episode mux: %s", final)
	}
	if !strings.Contains(final, "00001_2.flac") {
		t.Errorf("the re-extracted audio track is missing: %s", final)
	}
}

func TestPipelineMKAIsMuxedSeparately(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		// The track goes to the external audio file instead of the main one.
		p.AudioTracks = []profile.AudioTrackSpec{
			{OutputCodec: "FLAC", MuxOption: model.MuxOptionMka},
		}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	var mkaMux bool
	for _, c := range ft.callsFor(t, roleMkvmerge) {
		if strings.Contains(c, ".mka") {
			mkaMux = true
		}
	}
	if !mkaMux {
		t.Errorf("the external audio file was never muxed: %v", ft.callsFor(t, roleMkvmerge))
	}
}

// chapterDoc is the `tchapter info` document for the chapter tests. Its shape is
// the one native/tchapter's CLI writes (tc_cli.c) and
// internal/chapter/parser.go reads.
func chapterDoc(times []string) string {
	var b strings.Builder
	b.WriteString(`{"version":1,"format":"ogm","entries":[{"title":"","source":"00001",`)
	b.WriteString(`"fps_num":24000,"fps_den":1001,"duration_ns":30000000000,"chapters":[`)
	for i, ts := range times {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"name":"Chapter 0`)
		b.WriteString(itoa(i + 1))
		b.WriteString(`","time_ns":`)
		b.WriteString(ts)
		b.WriteString(`,"frames":0}`)
	}
	b.WriteString(`]}]}`)
	return b.String()
}

// marshalProfile serializes a profile the way profile.Load expects to read it.
func marshalProfile(p *profile.Profile) ([]byte, error) { return json.Marshal(p) }

// withRPCTemplate writes a minimal RpcTemplate.vpy into dir and returns its
// path. The RPC processor renders the template's placeholders; the test only
// needs a file that exists.
func withRPCTemplate(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "RpcTemplate.vpy")
	body := "import vapoursynth as vs\n" +
		"# OKE:SOURCE_SCRIPT\n" +
		"# OKE:VIDEO_FILE\n" +
		"# OKE:VSPIPE_ARGS\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write rpc template: %v", err)
	}
	return path
}

func TestPipelineRPCFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	spec := normalSpec()
	// A PSNR below the 30 dB threshold fails the check.
	spec.RPCLines = []string{"RPCOUT: 0 25.0"}
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.Rpc = true
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))
	opts := ft.options(t)
	opts.RPCTemplate = withRPCTemplate(t, dir)

	_, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !ft.hasRole(t, roleVSPipeRPC) {
		t.Errorf("the RPC check never ran: %v", ft.roles(t))
	}
	// The result file records the failure. A failing check writes to
	// OutputPathPrefix + ".rpc", again with the status inserted.
	entries, _ := os.ReadDir(filepath.Dir(prof.OutputPathPrefix))
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "未通过") && strings.HasSuffix(e.Name(), ".rpc") {
			found = true
		}
	}
	if !found {
		t.Errorf("no failed-RPC result in %v", entries)
	}
}

func TestPipelineRPCSkippedWithoutTheProfileFlag(t *testing.T) {
	ft, opts, task := newNormalPipeline(t, func(p *profile.Profile) {
		p.Rpc = false
	})
	_, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if ft.hasRole(t, roleVSPipeRPC) {
		t.Error("the RPC check ran although the profile disabled it")
	}
}

func TestPipelineRPCPassingRun(t *testing.T) {
	dir := t.TempDir()
	spec := normalSpec()
	spec.RPCLines = []string{"RPCOUT: 0 45.0", "RPCOUT: 1 46.0"}
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.Rpc = true
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))
	opts := ft.options(t)
	opts.RPCTemplate = withRPCTemplate(t, dir)

	_, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// A passing check writes its result next to the source script
	// (Path.ChangeExtension(InputScript, ".rpc")), and the status is inserted
	// into the extension.
	entries, _ := os.ReadDir(filepath.Dir(prof.InputScript))
	var found bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "通过") && strings.HasSuffix(e.Name(), ".rpc") {
			found = true
		}
	}
	if !found {
		t.Errorf("no passing-RPC result in %v", entries)
	}
}

func TestPipelineMP4UsesLSmash(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.ContainerFormat = string(profile.ContainerMP4)
		p.AudioTracks = []profile.AudioTrackSpec{
			{OutputCodec: "FLAC", MuxOption: model.MuxOptionDefault},
		}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !ft.hasRole(t, roleLSmash) {
		t.Errorf("l-smash never ran for an MP4 profile: %v", ft.roles(t))
	}
	calls := ft.callsFor(t, roleLSmash)
	if len(calls) != 1 {
		t.Fatalf("l-smash ran %d times, want 1", len(calls))
	}
	if !strings.Contains(calls[0], "--file-format mp4") {
		t.Errorf("the muxer is not l-smash's: %s", calls[0])
	}
}

func TestPipelineAV1UsesSvtAv1AndInlineQPFile(t *testing.T) {
	dir := t.TempDir()
	spec := normalSpec()
	spec.EncoderProgress = "Encoding: 100/100 Frames @ 25.00 fps | 1000.00 kb/s"
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.EncoderType = string(profile.EncoderSVTAV1)
		p.VideoFormat = "AV1"
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	calls := ft.callsFor(t, roleEncoder)
	if len(calls) != 1 {
		t.Fatalf("the encoder ran %d times, want 1", len(calls))
	}
	if !strings.Contains(calls[0], "-b ") {
		t.Errorf("SVT-AV1 was not given its bitstream flag: %s", calls[0])
	}
	if !strings.Contains(calls[0], ".ivf") {
		t.Errorf("the AV1 output is not an ivf: %s", calls[0])
	}
}

func TestPipelineX264IntoMP4WritesRawStream(t *testing.T) {
	dir := t.TempDir()
	spec := normalSpec()
	spec.EncoderProgress = "encoded 100 frames, 25.00 fps, 1000.00 kb/s"
	// The source is FLAC and the profile asks for FLAC passthrough, so the
	// demuxer extracts it unchanged. The interesting part of this case is the
	// encoder's output extension, not the audio path.
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.EncoderType = string(profile.EncoderX264)
		p.VideoFormat = "AVC"
		p.ContainerFormat = string(profile.ContainerMP4)
		p.AudioTracks = []profile.AudioTrackSpec{
			{OutputCodec: "FLAC", MuxOption: model.MuxOptionDefault},
		}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	calls := ft.callsFor(t, roleEncoder)
	if len(calls) != 1 {
		t.Fatalf("the encoder ran %d times, want 1", len(calls))
	}
	if !strings.Contains(calls[0], ".h264") {
		t.Errorf("x264 into an MP4 profile must write a raw stream: %s", calls[0])
	}
}

// TestPipelineAudioFormatMismatch pins the third branch of the legacy encoder
// choice: a source format that matches neither the FLAC->AAC path nor the
// lossy path is Constants.audioFormatMistachSmr.
func TestPipelineAudioFormatMismatch(t *testing.T) {
	dir := t.TempDir()
	spec := normalSpec()
	// The source is FLAC but the profile asks for AC3, so no branch applies.
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{
			{OutputCodec: "AC3", MuxOption: model.MuxOptionDefault},
		}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err == nil {
		t.Fatal("Run() = nil, want a format mismatch")
	}
	rendered := okerr.Render(okerr.AsError(err))
	if !strings.Contains(rendered, "FLAC") || !strings.Contains(rendered, "AC3") {
		t.Errorf("rendered = %q, want both formats named", rendered)
	}
}
