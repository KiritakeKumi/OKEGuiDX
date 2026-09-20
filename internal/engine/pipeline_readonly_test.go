package engine

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// The queue shares a task's profile and episode config with every snapshot it
// hands out (TaskManager.cloneTask), so the pipeline must treat both as
// read-only and keep everything it derives in its own run state. These tests are
// the acceptance criterion for that contract: they run a whole task and compare
// the inputs before and after.
//
// They exist because the pipeline used to write two values back — IsReEncode,
// reconciled from the episode config, and the I-frame-aligned slice array — and
// a REST client reading GET /api/v1/tasks then raced with the worker running the
// task. A regression that reintroduces a write fails these tests
// deterministically, without needing the race detector.

// TestPipelineDoesNotMutateItsInputs runs one task per shape and asserts that
// neither the profile nor the episode config changed.
func TestPipelineDoesNotMutateItsInputs(t *testing.T) {
	cases := []struct {
		name      string
		reEncode  bool
		reExtract bool
	}{
		{name: "normal task"},
		{name: "re-encode that keeps the old tracks", reEncode: true},
		{name: "re-encode that re-extracts the source", reEncode: true, reExtract: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				opts PipelineOptions
				task *model.Task
			)
			if tc.reEncode {
				_, opts, task = newReEncodePipeline(t, tc.reExtract, nil)
			} else {
				_, opts, task = newNormalPipeline(t, nil)
			}

			prof, ok := task.Profile.(*profile.Profile)
			if !ok {
				t.Fatal("the task does not carry a typed profile")
			}
			cfg, _ := task.Config.(*profile.EpisodeConfig)

			beforeProf := inputSnapshot(t, prof)
			beforeCfg := inputSnapshot(t, cfg)

			if _, err := runPipeline(t, opts, task); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			if after := inputSnapshot(t, prof); after != beforeProf {
				t.Errorf("the run modified the input profile:\nbefore = %s\nafter  = %s", beforeProf, after)
			}
			if after := inputSnapshot(t, cfg); after != beforeCfg {
				t.Errorf("the run modified the input episode config:\nbefore = %s\nafter  = %s", beforeCfg, after)
			}
		})
	}
}

// TestPipelineReconcilesReEncodeWithoutWritingItBack pins the exact write
// pipeline_load.go used to perform: the episode config's EnableReEncode is OR-ed
// into the profile's IsReEncode. A task recovered from the queue can have the
// profile flag unset, so the reconciliation must still happen — in the run
// state, not in the queue's profile.
func TestPipelineReconcilesReEncodeWithoutWritingItBack(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), reEncodeSpec())
	cfg := &profile.EpisodeConfig{
		EnableReEncode:     true,
		ReEncodeOldFile:    filepath.Join(dir, "old.mkv"),
		ReEncodeSliceArray: []model.SliceInfo{{Begin: 150, End: 250}},
	}
	prof := makeProfile(dir, func(p *profile.Profile) {
		// The wizard left the flag unset; only the episode config enables the
		// re-encode, which is what the pipeline used to copy across.
		p.IsReEncode = false
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, cfg)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	if _, err := runPipeline(t, ft.options(t), task); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if prof.IsReEncode {
		t.Error("the run wrote IsReEncode back into the input profile")
	}
	if got := cfg.ReEncodeSliceArray; len(got) != 1 || got[0] != model.NewSliceInfo(150, 250) {
		t.Errorf("the config's slice array was rewritten: %v", got)
	}
	// The run still has to be a re-encode, and the requested slice [150, 250)
	// still has to be aligned to the I-frames at 100 and 300: the three-part
	// layout is what proves the derived state reached the stages.
	want := []model.SliceInfo{
		model.NewSliceInfo(0, 100),
		model.NewSliceInfo(100, 300),
		model.NewSliceInfo(300, 1000),
	}
	got := task.SliceParts
	if len(got) != len(want) {
		t.Fatalf("SliceParts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SliceParts[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}

// TestValidateForRunNormalizesAPrivateCopy pins the third write the read-only
// contract removed: profile.ValidateEpisodeConfig sorts and merges its argument
// in place, so validateForRun must hand it a copy and return the normalized
// slices for the run state to carry.
func TestValidateForRunNormalizesAPrivateCopy(t *testing.T) {
	prof := &profile.Profile{
		InputScript:       "ep01.vpy",
		WorkingPathPrefix: filepath.Join("work", "ep01"),
		OutputPathPrefix:  filepath.Join("out", "ep01"),
		EncoderType:       string(profile.EncoderX265),
		FpsNum:            24000,
		FpsDen:            1001,
	}
	// Two touching slices: validation merges them into one.
	cfg := &profile.EpisodeConfig{
		EnableReEncode:     true,
		ReEncodeOldFile:    "old.mkv",
		ReEncodeSliceArray: []model.SliceInfo{{Begin: 40, End: 100}, {Begin: 100, End: 200}},
	}

	slices, err := validateForRun(prof, cfg)
	if err != nil {
		t.Fatalf("validateForRun() error = %v", err)
	}
	if want := []model.SliceInfo{model.NewSliceInfo(40, 200)}; len(slices) != 1 || slices[0] != want[0] {
		t.Errorf("validateForRun() slices = %v, want %v", slices, want)
	}
	if len(cfg.ReEncodeSliceArray) != 2 {
		t.Errorf("the config's slice array was normalized in place: %v", cfg.ReEncodeSliceArray)
	}
	if got := cfg.ReEncodeSliceArray[0]; got != model.NewSliceInfo(40, 100) {
		t.Errorf("the config's first slice changed: %v", got)
	}

	// A normal task has no slices and must not fail.
	if slices, err := validateForRun(prof, nil); err != nil || slices != nil {
		t.Errorf("validateForRun(prof, nil) = %v, %v; want nil, nil", slices, err)
	}
}

// TestPipelineRunDoesNotRaceWithAQueueReader reproduces the symptom that
// motivated the read-only contract: a REST client marshalling the queue
// (GET /api/v1/tasks) while a worker runs the same task. The queue hands the
// pipeline the profile and episode config pointers themselves, so the run must
// not write to either. Under -race a reintroduced write fails here.
//
// The task struct is deliberately not marshalled: the pool hands the pipeline a
// copy of it (TaskManager.cloneTask), so writing the task's own status fields is
// the pool's design. The profile and the config are the shared values.
func TestPipelineRunDoesNotRaceWithAQueueReader(t *testing.T) {
	_, opts, task := newReEncodePipeline(t, false, nil)
	prof, ok := task.Profile.(*profile.Profile)
	if !ok {
		t.Fatal("the task does not carry a typed profile")
	}
	cfg, ok := task.Config.(*profile.EpisodeConfig)
	if !ok {
		t.Fatal("the task does not carry a typed episode config")
	}

	stop := make(chan struct{})
	done := make(chan struct{})
	// The deferred stop runs even when runPipeline fails the test, so the
	// reader goroutine never outlives this test.
	defer func() {
		close(stop)
		<-done
	}()
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = json.Marshal(prof)
			_, _ = json.Marshal(cfg)
		}
	}()

	if _, err := runPipeline(t, opts, task); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

// inputSnapshot renders a profile or episode config as JSON for an exact
// before/after comparison. A nil value is the empty string, so a nil config
// never compares equal to a present one.
func inputSnapshot(t *testing.T, v any) string {
	t.Helper()
	if v == nil {
		return ""
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	return string(raw)
}
