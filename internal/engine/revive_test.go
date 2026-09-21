package engine

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// TestReviveStoredProfileRecoversAQueuedTask is the crash-recovery test. The
// task manager persists the *assembled* profile - the one carrying the generated
// InputScript and the two derived path prefixes - but model.Task holds it as
// `any`, so a JSON round trip turns it into a map. Without reviveStored the
// pipeline fell back to re-reading the profile file, which never had those three
// fields (the wizard derived them and did not write them back), and the task
// could not run again after a restart.
func TestReviveStoredProfileRecoversAQueuedTask(t *testing.T) {
	t.Parallel()
	assembled := &profile.Profile{
		ProjectName:       "ep01",
		InputScript:       `D:\work\C_\src\ep01.mkv-09200905.vpy`,
		WorkingPathPrefix: `D:\work\C_\src\ep01.mkv`,
		OutputPathPrefix:  `D:\work\output\src\ep01.mkv`,
		ContainerFormat:   "MKV",
		EncoderType:       "x265",
		FpsNum:            24000,
		FpsDen:            1001,
		Config: &profile.EpisodeConfig{
			EnableReEncode:     true,
			ReEncodeOldFile:    `D:\work\src\old.mkv`,
			ReEncodeSliceArray: []model.SliceInfo{{Begin: 0, End: 10}},
		},
	}
	queued := &model.Task{ID: model.NewTaskID(), Profile: assembled, Config: assembled.Config}

	// What the queue does: marshal on the way out, unmarshal on the way in.
	data, err := json.Marshal(queued)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	recovered := &model.Task{}
	if err := json.Unmarshal(data, recovered); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if _, ok := recovered.Profile.(*profile.Profile); ok {
		t.Fatal("the round trip kept the typed profile; this test would not prove anything")
	}

	// The point of recovering it: profileFor uses the queued profile and never
	// falls back to the caller's loader, which for a recovered task would
	// re-read a profile file that lacks the three derived fields.
	loaderCalled := false
	p := NewPipeline(PipelineOptions{
		LoadProfile: func(*model.Task) (*profile.Profile, *profile.EpisodeConfig, error) {
			loaderCalled = true
			return nil, nil, errors.New("the loader must not be reached")
		},
	})
	prof, cfg, err := p.profileFor(recovered)
	if err != nil {
		t.Fatalf("profileFor() error = %v", err)
	}
	if loaderCalled {
		t.Error("profileFor() fell back to LoadProfile instead of using the queued profile")
	}
	if prof.InputScript != assembled.InputScript {
		t.Errorf("InputScript = %q, want %q", prof.InputScript, assembled.InputScript)
	}
	if prof.WorkingPathPrefix != assembled.WorkingPathPrefix {
		t.Errorf("WorkingPathPrefix = %q, want %q", prof.WorkingPathPrefix, assembled.WorkingPathPrefix)
	}
	if prof.OutputPathPrefix != assembled.OutputPathPrefix {
		t.Errorf("OutputPathPrefix = %q, want %q", prof.OutputPathPrefix, assembled.OutputPathPrefix)
	}
	if cfg == nil || !cfg.EnableReEncode || cfg.ReEncodeOldFile != assembled.Config.ReEncodeOldFile {
		t.Fatalf("config = %+v, want the queued episode config", cfg)
	}

	// The point of recovering it: the pipeline accepts it.
	if _, err := validateForRun(prof, cfg); err != nil {
		t.Errorf("validateForRun() on the recovered profile error = %v", err)
	}
}

// TestReviveStoredProfileRejectsAStranger pins that a value that is not a profile
// is reported as absent rather than silently producing a zero profile, so
// profileFor falls through to the caller's loader.
func TestReviveStoredProfileRejectsAStranger(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		task *model.Task
	}{
		{"nil task", nil},
		{"no profile", &model.Task{ID: model.NewTaskID()}},
		{"profile is a string", &model.Task{Profile: "not a profile"}},
		{"profile is a number", &model.Task{Profile: 42}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, _, ok := reviveStored(tc.task); ok {
				t.Error("reviveStored() = true, want false")
			}
		})
	}
}
