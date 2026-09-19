package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// capsWith builds capabilities with the given features and demuxer choice.
func capsWith(features ...string) node.Capabilities {
	caps := node.NewCapabilities(node.RoleStandalone)
	for _, f := range features {
		caps.AddFeature(f)
	}
	return caps
}

// normalSpec is the fake behaviour of a healthy run: one FLAC audio track that
// is audible, and an x265 encode that finishes.
func normalSpec() fakeSpec {
	return fakeSpec{
		VSPipeInfo:      "Width: 1920\nHeight: 1080\nFrames: 100\nFPS: 24000/1001 (23.976 fps)\nFormat Name: YUV420P8\nColor Family: YUV\nBits: 8\n",
		Tracks:          []fakeTrack{{Codec: "flac", Index: 2, Type: "audio", Language: "jpn"}},
		EncoderProgress: "encoded 100 frames in 4.00s (25.00 fps), 1000.00 kb/s, Avg QP:20.00",
	}
}

// normalAudioTrack is the profile entry that matches normalSpec's source.
func normalAudioTrack() profile.AudioTrackSpec {
	return profile.AudioTrackSpec{OutputCodec: "FLAC", MuxOption: model.MuxOptionDefault}
}

// newNormalPipeline sets up a task whose source has one FLAC audio track and a
// whole-file x265 encode. It returns the tools, the options and the task.
func newNormalPipeline(t *testing.T, mutate func(*profile.Profile)) (*fakeTools, PipelineOptions, *model.Task) {
	t.Helper()
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
		if mutate != nil {
			mutate(p)
		}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))
	return ft, ft.options(t), task
}

func TestPipelineNormalRunStages(t *testing.T) {
	ft, opts, task := newNormalPipeline(t, nil)
	events, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	last := events[len(events)-1]
	if last.Progress != model.TaskFinished {
		t.Errorf("final progress = %v, want FINISHED", last.Progress)
	}

	// The stage order is the contract. Every role below must have run, and the
	// relative order of the ones that depend on each other is asserted.
	for _, role := range []string{roleVSPipeInfo, roleEac3to, roleEncoder, roleMkvmerge} {
		if !ft.hasRole(t, role) {
			t.Errorf("tool %q never ran; roles = %v", role, ft.roles(t))
		}
	}
	if a, b := ft.indexOfRole(t, roleVSPipeInfo), ft.indexOfRole(t, roleEac3to); a > b {
		t.Errorf("vspipe --info ran after the demuxer: %d > %d", a, b)
	}
	if a, b := ft.indexOfRole(t, roleEac3to), ft.indexOfRole(t, roleEncoder); a > b {
		t.Errorf("the demuxer ran after the encoder: %d > %d", a, b)
	}
	if a, b := ft.indexOfRole(t, roleEncoder), ft.indexOfRole(t, roleMkvmerge); a > b {
		t.Errorf("the encoder ran after the muxer: %d > %d", a, b)
	}

	// The deliverable is named after the input and the container, and carries
	// the CRC32 tag the legacy pipeline appended. The task records the tagged
	// name, which is what the operator copies out of the UI.
	prof := task.Profile.(*profile.Profile)
	outDir := filepath.Dir(prof.OutputPathPrefix)
	entries, readErr := os.ReadDir(outDir)
	if readErr != nil {
		t.Fatalf("read output dir: %v", readErr)
	}
	var deliverable string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "00001.m2ts") && strings.HasSuffix(e.Name(), ".mkv") {
			deliverable = e.Name()
		}
	}
	if deliverable == "" {
		t.Fatalf("no deliverable in %s: %v", outDir, entries)
	}
	if !strings.Contains(deliverable, " [") || !strings.Contains(deliverable, "]") {
		t.Errorf("the deliverable carries no CRC32 tag: %s", deliverable)
	}
	if got := task.Output.Base(); got != deliverable {
		t.Errorf("task.Output = %q, want the tagged name %q", got, deliverable)
	}
}

func TestPipelineReportsProgress(t *testing.T) {
	_, opts, task := newNormalPipeline(t, nil)
	events, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(events) < 4 {
		t.Fatalf("got %d events, want several", len(events))
	}
	seen := make(map[string]bool)
	for _, ev := range events {
		if ev.TaskID != task.ID {
			t.Errorf("event task id = %s, want %s", ev.TaskID, task.ID)
		}
		seen[ev.Step] = true
	}
	for _, want := range []string{statusFetchInfo, statusPrepare, statusVideoEncode, statusFinalMux} {
		if !seen[want] {
			t.Errorf("no event carried step %q; seen = %v", want, seen)
		}
	}
}

// TestPipelineReEncodeStatuses pins the "Part N/M" wording the legacy UI showed
// while a re-encode ran, including the part number it named.
func TestPipelineReEncodeStatuses(t *testing.T) {
	_, opts, task := newReEncodePipeline(t, false, nil)
	events, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	seen := make(map[string]bool)
	for _, ev := range events {
		seen[ev.Step] = true
	}
	// The layout is: part 0 copied, part 1 encoded, part 2 copied, so three
	// parts in total.
	for _, want := range []string{"Part 2/3 压制中", "Part 2/3 封装中", "Part 1/3 封装中"} {
		if !seen[want] {
			t.Errorf("no event carried step %q; seen = %v", want, seen)
		}
	}
}

func TestPipelineFpsMismatch(t *testing.T) {
	ft, opts, task := newNormalPipeline(t, func(p *profile.Profile) {
		// The script reports 24000/1001; the profile asks for 25.
		p.FpsNum = 25
		p.FpsDen = 1
	})
	_, err := runPipeline(t, opts, task)
	if err == nil {
		t.Fatal("Run() = nil, want a frame-rate mismatch")
	}
	if !errors.Is(err, okerr.ErrFpsMismatch) {
		t.Errorf("error = %v, want ErrFpsMismatch", err)
	}
	// okerr.Render produces the operator-facing Constants.fpsMismatchMsg. The
	// legacy template interpolates the source file, so it names the input.
	rendered := okerr.Render(okerr.AsError(err))
	if !strings.Contains(rendered, "23.976") || !strings.Contains(rendered, "25.000") {
		t.Errorf("rendered = %q, want both frame rates", rendered)
	}
	// The encode must not have started.
	if ft.hasRole(t, roleEncoder) {
		t.Error("the encoder ran despite the frame-rate mismatch")
	}
}

func TestPipelineTrackCountMismatch(t *testing.T) {
	dir := t.TempDir()
	// The source carries one audio track, the profile asks for two.
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{
			normalAudioTrack(),
			{OutputCodec: "FLAC", MuxOption: model.MuxOptionDefault},
		}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err == nil {
		t.Fatal("Run() = nil, want a track-count mismatch")
	}
	if !errors.Is(err, okerr.ErrAudioNumMismatch) {
		t.Errorf("error = %v, want ErrAudioNumMismatch", err)
	}
	// The legacy operator message names the counts and the source file.
	rendered := okerr.Render(okerr.AsError(err))
	if !strings.Contains(rendered, "轨道数") {
		t.Errorf("rendered = %q, want the legacy track-count message", rendered)
	}
}

func TestPipelineSubtitleCountMismatch(t *testing.T) {
	dir := t.TempDir()
	// The source has no subtitle track, the profile asks for one.
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
		p.SubtitleTracks = []profile.TrackSpec{
			{MuxOption: model.MuxOptionDefault, Language: "jpn"},
		}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err == nil {
		t.Fatal("Run() = nil, want a subtitle-count mismatch")
	}
	if !errors.Is(err, okerr.ErrSubNumMismatch) {
		t.Errorf("error = %v, want ErrSubNumMismatch", err)
	}
}

func TestPipelinePlatformRejectsAAC(t *testing.T) {
	dir := t.TempDir()
	// A node without the AAC feature: no qaac, so an AAC profile must be
	// refused before the encoder runs (PLAN.md §2.2).
	caps := capsWith(node.FeatureEac3to)
	ft := newFakeTools(t, dir, caps, normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{
			{OutputCodec: "AAC", MuxOption: model.MuxOptionDefault},
		}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err == nil {
		t.Fatal("Run() = nil, want an unsupported-AAC error")
	}
	if !errors.Is(err, okerr.ErrUnsupportedAAC) {
		t.Errorf("error = %v, want ErrUnsupportedAAC", err)
	}
	if ft.hasRole(t, roleQAAC) {
		t.Error("qaac ran on a node that does not advertise AAC")
	}
}

func TestPipelineRejectsUnknownEncoder(t *testing.T) {
	_, opts, task := newNormalPipeline(t, func(p *profile.Profile) {
		p.EncoderType = "xvid"
	})
	_, err := runPipeline(t, opts, task)
	if err == nil {
		t.Fatal("Run() = nil, want a rejected encoder")
	}
	if !strings.Contains(err.Error(), "编码器") {
		t.Errorf("error = %v, want the legacy encoder message", err)
	}
}

func TestPipelineCancelStopsEarly(t *testing.T) {
	dir := t.TempDir()
	spec := normalSpec()
	// The encoder blocks, so the test can cancel while a stage is alive.
	spec.BlockTool = roleEncoder
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	ctx, cancel := context.WithCancel(context.Background())
	p := NewPipeline(ft.options(t))
	exec := NewLocalExecutor(ft.caps, p.Run)
	events, err := exec.Submit(ctx, task)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	// Wait for the encoder to start, then cancel. The child appends to the log
	// before it blocks, so the poll sees it.
	deadline := time.After(30 * time.Second)
	for !ft.hasRole(t, roleEncoder) {
		select {
		case <-deadline:
			cancel()
			for range events {
			}
			t.Fatalf("the encoder never started; roles = %v", ft.roles(t))
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()

	final := model.StatusEvent{}
	for ev := range events {
		final = ev
	}
	exec.Wait()
	if final.Progress != model.TaskError {
		t.Errorf("final progress = %v, want ERROR", final.Progress)
	}
	if final.Error == nil {
		t.Fatal("final event carries no error")
	}
	if !strings.Contains(final.Error.Summary, "取消") && !strings.Contains(final.Error.Summary, "终止") {
		t.Errorf("error summary = %q, want a cancellation message", final.Error.Summary)
	}
	// The muxer must not have run after the cancel.
	if ft.hasRole(t, roleMkvmerge) {
		t.Error("the muxer ran after the task was cancelled")
	}
}

func TestPipelineMissingToolIsReported(t *testing.T) {
	dir := t.TempDir()
	caps := capsWith(node.FeatureEac3to, node.FeatureAAC)
	ft := newFakeTools(t, dir, caps, normalSpec())
	// Drop vspipe, which every task needs.
	delete(ft.caps.Tools, toolchain.ToolVSPipe)
	prof := makeProfile(dir, nil)
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err == nil {
		t.Fatal("Run() = nil, want a missing-tool error")
	}
	if !errors.Is(err, okerr.ErrToolNotFound) {
		t.Errorf("error = %v, want ErrToolNotFound", err)
	}
}

func TestPipelineEmptyTrackIsDropped(t *testing.T) {
	dir := t.TempDir()
	// Two audio tracks, the second silent. The demuxer marks it DupOrEmpty and
	// the pipeline must not mux it.
	spec := normalSpec()
	spec.Tracks = []fakeTrack{
		{Codec: "flac", Index: 2, Type: "audio", Language: "jpn"},
		{Codec: "flac", Index: 3, Type: "audio", Language: "eng"},
	}
	spec.VolumeByFile = map[string]string{
		"00001_3.flac": "RMS level dB: -90.0\nPeak level dB: -90.0\n",
	}
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{
			normalAudioTrack(),
			{OutputCodec: "FLAC", MuxOption: model.MuxOptionDefault},
		}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// Only the audible track reaches the muxer's command line.
	calls := ft.callsFor(t, roleMkvmerge)
	if len(calls) == 0 {
		t.Fatal("the muxer never ran")
	}
	final := calls[len(calls)-1]
	if !strings.Contains(final, "00001_2.flac") {
		t.Errorf("the audible track is missing from the muxer command line: %s", final)
	}
	if strings.Contains(final, "00001_3.flac") {
		t.Errorf("the silent track reached the muxer: %s", final)
	}
}

func TestPipelineUsesFFmpegDemuxerWithoutEac3to(t *testing.T) {
	dir := t.TempDir()
	// No eac3to feature: the ffmpeg demuxer must run instead.
	ft := newFakeTools(t, dir, capsWith(node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !ft.hasRole(t, roleFFprobe) {
		t.Errorf("ffprobe never ran; roles = %v", ft.roles(t))
	}
	if ft.hasRole(t, roleEac3to) {
		t.Error("eac3to ran on a node that does not advertise it")
	}
}

func TestPipelineSkipsDemuxWhenReEncodeKeepsOldTracks(t *testing.T) {
	dir := t.TempDir()
	oldFile := filepath.Join(dir, "old.mkv")
	cfg := &profile.EpisodeConfig{
		EnableReEncode:     true,
		ReExtractSource:    false,
		ReEncodeOldFile:    oldFile,
		ReEncodeSliceArray: []model.SliceInfo{{Begin: 20, End: 40}},
	}
	spec := normalSpec()
	spec.IFrameList = "IFrameList: [0, 20, 40, 100]"
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.IsReEncode = true
	})
	task := prepare(t, ft, prof, cfg)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if ft.hasRole(t, roleEac3to) || ft.hasRole(t, roleFFprobe) {
		t.Errorf("the demuxer ran for a re-encode without ReExtractSource: %v", ft.roles(t))
	}
	if !ft.hasRole(t, roleVSPipeIFrame) {
		t.Errorf("the I-frame probe never ran: %v", ft.roles(t))
	}
	if !ft.hasRole(t, roleMkvmerge) {
		t.Errorf("the merge muxer never ran: %v", ft.roles(t))
	}
}
