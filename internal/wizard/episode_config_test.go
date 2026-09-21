package wizard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// episodeFixture lays out a source file with an old deliverable beside it, which
// is what a re-encode config points at. It returns the project directory and the
// resolved source path.
func episodeFixture(t *testing.T) (dir, input, old string) {
	t.Helper()
	dir = t.TempDir()
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	input = filepath.Join(srcDir, "ep01.mkv")
	if err := os.WriteFile(input, nil, 0o600); err != nil {
		t.Fatalf("write the source: %v", err)
	}
	old = filepath.Join(srcDir, "old.mkv")
	if err := os.WriteFile(old, nil, 0o600); err != nil {
		t.Fatalf("write the old deliverable: %v", err)
	}
	return dir, input, old
}

// writeSiblingConfig writes `<input>.json` with the given EnableReEncode.
func writeSiblingConfig(t *testing.T, input string, enable bool) {
	t.Helper()
	body := `{"EnableReEncode":false,"ReEncodeOldFile":"","ReEncodeSliceArray":[]}`
	if enable {
		body = `{"EnableReEncode":true,"ReEncodeOldFile":"old.mkv",` +
			`"ReEncodeSliceArray":[{"begin":0,"end":10}]}`
	}
	if err := os.WriteFile(input+".json", []byte(body), 0o600); err != nil {
		t.Fatalf("write the sibling config: %v", err)
	}
}

// inlineConfig builds the profile-side Config a request may carry. It is always
// well formed, so a failure in these tests is the precedence rule and not the
// validator.
func inlineConfig(dir string, enable bool) *profile.EpisodeConfig {
	cfg := &profile.EpisodeConfig{
		EnableReEncode:     enable,
		ReEncodeOldFile:    filepath.Join(dir, "inline-old.mkv"),
		ReEncodeSliceArray: []model.SliceInfo{{Begin: 0, End: 10}},
	}
	return cfg
}

// TestAttachEpisodeConfigPrefersTheSiblingFile covers the rule that the
// per-episode `<input>.json` wins over a Config written inline in the profile.
// The sibling file is the older mechanism and the one a technical director
// edits, so a project that has both must be driven by the file.
func TestAttachEpisodeConfigPrefersTheSiblingFile(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		siblingOn   bool
		inlineOn    bool
		wantIsReEnc bool
	}{
		{"sibling on, inline off", true, false, true},
		{"sibling off, inline on", false, true, false},
		{"both on", true, true, true},
		{"both off", false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, input, old := episodeFixture(t)
			writeSiblingConfig(t, input, tc.siblingOn)

			prof := &profile.Profile{
				ContainerFormat: "MKV",
				Config:          inlineConfig(dir, tc.inlineOn),
			}
			if err := AttachEpisodeConfig(prof, input, dir); err != nil {
				t.Fatalf("AttachEpisodeConfig() error = %v", err)
			}
			if prof.IsReEncode != tc.wantIsReEnc {
				t.Errorf("IsReEncode = %v, want %v", prof.IsReEncode, tc.wantIsReEnc)
			}
			// The sibling replaced the inline config, whatever it said.
			if prof.Config == nil || prof.Config.EnableReEncode != tc.siblingOn {
				t.Fatalf("Config = %+v, want the sibling's (EnableReEncode %v)",
					prof.Config, tc.siblingOn)
			}
			if tc.siblingOn && prof.Config.ReEncodeOldFile != old {
				t.Errorf("ReEncodeOldFile = %q, want the resolved %q",
					prof.Config.ReEncodeOldFile, old)
			}
		})
	}
}

// TestAttachEpisodeConfigFallsBackToTheInlineConfig covers the case the legacy
// code had no equivalent for: a profile that carries its Config itself, with no
// sibling file. The config is then relative to the profile's directory, and it
// still needs the resolve and the container check.
func TestAttachEpisodeConfigFallsBackToTheInlineConfig(t *testing.T) {
	t.Parallel()
	dir, input, old := episodeFixture(t)
	prof := &profile.Profile{
		ContainerFormat: "MKV",
		Config: &profile.EpisodeConfig{
			EnableReEncode:     true,
			ReEncodeOldFile:    "src/old.mkv",
			ReEncodeSliceArray: []model.SliceInfo{{Begin: 0, End: 10}},
		},
	}
	if err := AttachEpisodeConfig(prof, input, dir); err != nil {
		t.Fatalf("AttachEpisodeConfig() error = %v", err)
	}
	if !prof.IsReEncode {
		t.Error("IsReEncode = false, want true from the inline config")
	}
	if prof.Config.ReEncodeOldFile != old {
		t.Errorf("ReEncodeOldFile = %q, want the resolved %q", prof.Config.ReEncodeOldFile, old)
	}
}

// TestAttachEpisodeConfigRefusesAnInlineNonMkvReEncode pins that the container
// check applies to the inline form too. Nothing else checks it on the server:
// the wizard's JavaScript does, but a request that skips the page would reach
// the muxer otherwise.
func TestAttachEpisodeConfigRefusesAnInlineNonMkvReEncode(t *testing.T) {
	t.Parallel()
	dir, input, _ := episodeFixture(t)
	prof := &profile.Profile{
		ContainerFormat: "MP4",
		Config: &profile.EpisodeConfig{
			EnableReEncode:     true,
			ReEncodeOldFile:    "src/old.mkv",
			ReEncodeSliceArray: []model.SliceInfo{{Begin: 0, End: 10}},
		},
	}
	if err := AttachEpisodeConfig(prof, input, dir); err == nil {
		t.Fatal("AttachEpisodeConfig() = nil, want the container error")
	}
}

// TestAttachEpisodeConfigWithoutAnyConfig pins that a plain task is left alone.
func TestAttachEpisodeConfigWithoutAnyConfig(t *testing.T) {
	t.Parallel()
	dir, input, _ := episodeFixture(t)
	prof := &profile.Profile{ContainerFormat: "MKV"}
	if err := AttachEpisodeConfig(prof, input, dir); err != nil {
		t.Fatalf("AttachEpisodeConfig() error = %v", err)
	}
	if prof.IsReEncode || prof.Config != nil {
		t.Errorf("IsReEncode = %v, Config = %+v, want no re-encode and no config",
			prof.IsReEncode, prof.Config)
	}
}

// TestAttachEpisodeConfigRefusesANonMkvReEncode covers
// WizardWindow.xaml.cs:341-348: the re-encode merge joins the old mkv's
// non-video tracks to the new video, so a deliverable that is not an mkv has
// nothing to join. The legacy wizard skipped the source; a headless run reports
// it.
func TestAttachEpisodeConfigRefusesANonMkvReEncode(t *testing.T) {
	t.Parallel()
	dir, input, _ := episodeFixture(t)
	writeSiblingConfig(t, input, true)
	prof := &profile.Profile{ContainerFormat: "MP4"}
	err := AttachEpisodeConfig(prof, input, dir)
	if err == nil {
		t.Fatal("AttachEpisodeConfig() = nil, want the container error")
	}
	if !strings.Contains(err.Error(), "mkv") {
		t.Errorf("error = %v, want it to name the mkv requirement", err)
	}
}

// TestAttachEpisodeConfigIsIdempotent pins that calling it twice, which is what
// happens when the API attaches to the profile and Derive then attaches to its
// per-source copy, does not change the answer.
func TestAttachEpisodeConfigIsIdempotent(t *testing.T) {
	t.Parallel()
	dir, input, old := episodeFixture(t)
	writeSiblingConfig(t, input, true)
	prof := &profile.Profile{ContainerFormat: "MKV"}
	for i := range 2 {
		if err := AttachEpisodeConfig(prof, input, dir); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if !prof.IsReEncode || prof.Config == nil || prof.Config.ReEncodeOldFile != old {
		t.Errorf("after two calls: IsReEncode = %v, Config = %+v", prof.IsReEncode, prof.Config)
	}
}
