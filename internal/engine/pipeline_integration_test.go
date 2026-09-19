package engine

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// TestPipelineUsesTheWorkerNUMANode proves the pipeline reads the node the
// worker pool assigned through the context, which is what replaces the legacy
// WorkerArgs.numaNode field across the frozen Executor boundary.
func TestPipelineUsesTheWorkerNUMANode(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	// A four-node allocator: the assigned node's mask has a "+" in its slot and
	// "-" in the others.
	opts := ft.options(t)
	opts.Numa = platform.NewNumaWithCount(4)

	p := NewPipeline(opts)
	ctx := WithNUMANode(t.Context(), 2)
	events := make(chan model.StatusEvent, 64)
	if err := p.Run(ctx, task, events); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	calls := ft.callsFor(t, roleEncoder)
	if len(calls) != 1 {
		t.Fatalf("the encoder ran %d times, want 1", len(calls))
	}
	if !strings.Contains(calls[0], "--pools -,-,+,-") {
		t.Errorf("the encoder was not given the assigned node's mask: %s", calls[0])
	}
}

// TestPipelineEncoderParamsCarryTheProfileParameters proves the profile's own
// EncoderParam survives the pipeline's additions, since the wrappers tokenise it
// themselves.
func TestPipelineEncoderParamsCarryTheProfileParameters(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.EncoderParam = "--preset slower --crf 16 --keyint 360"
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
	for _, want := range []string{"--preset", "slower", "--crf", "16", "--keyint", "360"} {
		if !strings.Contains(calls[0], want) {
			t.Errorf("the encoder argument list lost %q: %s", want, calls[0])
		}
	}
}

// TestPipelineVspipeArgsReachBothProbes proves the profile's extra vspipe
// arguments are passed to the info probe and to the encoder's producer, which is
// what makes a parameterised script work.
func TestPipelineVspipeArgsReachBothProbes(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	cfg := &profile.EpisodeConfig{VspipeArgs: []string{"op_start=8000", "op_end=16000"}}
	task := prepare(t, ft, prof, cfg)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, role := range []string{roleVSPipeInfo, roleVSPipeEncode} {
		calls := ft.callsFor(t, role)
		if len(calls) == 0 {
			t.Fatalf("%s never ran", role)
		}
		for _, want := range []string{"--arg", "op_start=8000", "--arg", "op_end=16000"} {
			if !strings.Contains(calls[0], want) {
				t.Errorf("%s lost %q: %s", role, want, calls[0])
			}
		}
	}
}

// TestPipelineDefaultsTheRPCResultPath proves the skipped RPC status reaches the
// task, which is what the UI's "RPC" column reads.
func TestPipelineSkippedRPCSetsTheTaskStatus(t *testing.T) {
	_, opts, task := newNormalPipeline(t, func(p *profile.Profile) {
		p.Rpc = false
	})
	if _, err := runPipeline(t, opts, task); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if task.Status.RPC != model.RPCSkipped {
		t.Errorf("task RPC status = %v, want 跳过", task.Status.RPC)
	}
}

// TestPipelineRecordsTheTaskOutput proves the task's output fields are filled in
// for the queue, which is the state a UI lists.
func TestPipelineRecordsTheTaskOutput(t *testing.T) {
	_, opts, task := newNormalPipeline(t, nil)
	if _, err := runPipeline(t, opts, task); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if task.Output.IsZero() {
		t.Error("task.Output is empty after a successful run")
	}
	if task.Frames != 100 {
		t.Errorf("task.Frames = %d, want the script's frame count", task.Frames)
	}
	if task.LengthMS <= 0 {
		t.Errorf("task.LengthMS = %d, want a computed duration", task.LengthMS)
	}
	if !strings.Contains(task.Output.Base(), ".mkv") {
		t.Errorf("task.Output = %q, want the mkv deliverable", task.Output)
	}
	if task.Status.Output != task.Output {
		t.Errorf("task.Status.Output = %q, want %q", task.Status.Output, task.Output)
	}
}

// TestPipelineSkipAllAudioTracks proves the profile's skip switches reach the
// demuxer, so a profile can ignore a source's tracks entirely.
func TestPipelineSkipAllAudioTracks(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.SkipAllAudioTracks = true
		// With the source's track dropped before the count check, an empty
		// profile list is the matching configuration.
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, c := range ft.callsFor(t, roleMkvmerge) {
		if strings.Contains(c, "_2.flac") {
			t.Errorf("a skipped audio track reached the muxer: %s", c)
		}
	}
}

// TestPipelineSubtitleRouting pins AddSubtitle: a Default subtitle goes into the
// main container, an Mka one into the external file.
func TestPipelineSubtitleRouting(t *testing.T) {
	cases := []struct {
		name     string
		mux      model.MuxOption
		wantMain bool
		wantMKA  bool
	}{
		{name: "default goes to the main container", mux: model.MuxOptionDefault, wantMain: true},
		{name: "mka goes to the external audio file", mux: model.MuxOptionMka, wantMKA: true},
		{name: "external is only tagged", mux: model.MuxOptionExternal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			spec := normalSpec()
			spec.Tracks = []fakeTrack{
				{Codec: "flac", Index: 2, Type: "audio", Language: "jpn"},
				{Codec: "pgs", Index: 3, Type: "subtitle", Language: "jpn"},
			}
			ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
			prof := makeProfile(dir, func(p *profile.Profile) {
				p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
				p.SubtitleTracks = []profile.TrackSpec{
					{MuxOption: tc.mux, Language: "jpn"},
				}
			})
			task := prepare(t, ft, prof, nil)
			ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

			if _, err := runPipeline(t, ft.options(t), task); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			calls := ft.callsFor(t, roleMkvmerge)
			var main, mka bool
			for _, c := range calls {
				switch {
				case strings.Contains(c, ".mka"):
					if strings.Contains(c, "_3.sup") {
						mka = true
					}
				default:
					if strings.Contains(c, "_3.sup") {
						main = true
					}
				}
			}
			if main != tc.wantMain {
				t.Errorf("subtitle in the main container = %v, want %v: %v", main, tc.wantMain, calls)
			}
			if mka != tc.wantMKA {
				t.Errorf("subtitle in the external file = %v, want %v: %v", mka, tc.wantMKA, calls)
			}
		})
	}
}

// TestPipelineProfileIsReReadForAQueuedTask proves the LoadProfile hook is used
// when a task arrives without typed values, which is the crash-recovery path.
func TestPipelineProfileIsReReadForAQueuedTask(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	// Simulate a JSON round trip: the profile becomes an opaque map.
	task.Profile = map[string]any{"Version": 3}
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	opts := ft.options(t)
	loaded := false
	opts.LoadProfile = func(t *model.Task) (*profile.Profile, *profile.EpisodeConfig, error) {
		loaded = true
		return LoadProfileFromDisk(prof.ConfigFilePath)
	}
	if _, err := runPipeline(t, opts, task); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !loaded {
		t.Error("the pipeline did not call LoadProfile for a task without typed values")
	}
}

// TestPipelineMissingLoaderIsReported proves a task with no typed profile and no
// loader fails with a clear message instead of running with a zero profile.
func TestPipelineMissingLoaderIsReported(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, nil)
	task := prepare(t, ft, prof, nil)
	task.Profile = nil
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err == nil {
		t.Fatal("Run() = nil, want a missing-profile error")
	}
	if !strings.Contains(err.Error(), "配置") {
		t.Errorf("error = %v, want it to name the missing configuration", err)
	}
}

// TestPipelineOutputNaming pins the deliverable path for a profile whose output
// prefix sits in a different directory from the source.
func TestPipelineOutputNaming(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	outDir := filepath.Join(dir, "elsewhere")
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.OutputPathPrefix = filepath.Join(outDir, "ep01")
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	if _, err := runPipeline(t, ft.options(t), task); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// The deliverable goes to the output prefix's directory, named after the
	// source and the container. The comparison goes through the same FileRef
	// resolution the pipeline used, because a Windows drive letter is normalised
	// away by NewFileRef.
	wantDir := model.NewFileRef(outDir).Resolve(ft.caps.Volumes)
	if got := filepath.Dir(task.Output.Resolve(ft.caps.Volumes)); got != wantDir {
		t.Errorf("deliverable directory = %q, want %q", got, wantDir)
	}
}
