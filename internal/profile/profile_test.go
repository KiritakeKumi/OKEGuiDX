package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// exampleDir holds the profiles shipped with the current release. They are the
// regression fixtures for the frozen format (WORKSTREAMS.md §1 P0-3): if these
// stop parsing, existing installations break.
const exampleDir = "../../dist/windows/examples"

func TestParseRealExampleProfiles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		file         string
		wantVersion  int
		wantEncoder  string
		wantFormat   string
		wantInputs   int
		wantAudio    int
		wantSubtitle int
	}{
		{"demo.json", 3, "x265", "HEVC", 3, 2, 1},
		{"demo_720p.json", 2, "x264", "AVC", 3, 2, 1},
		{"vfr.json", 3, "x265", "HEVC", 3, 2, 0},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(exampleDir, tc.file)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Skipf("example profile not available: %v", err)
			}
			p, err := Parse(string(raw), path)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}

			if p.Version != tc.wantVersion {
				t.Errorf("Version = %d, want %d", p.Version, tc.wantVersion)
			}
			if p.EncoderType != tc.wantEncoder {
				t.Errorf("EncoderType = %q, want %q", p.EncoderType, tc.wantEncoder)
			}
			if len(p.InputFiles) != tc.wantInputs {
				t.Errorf("len(InputFiles) = %d, want %d", len(p.InputFiles), tc.wantInputs)
			}
			if len(p.AudioTracks) != tc.wantAudio {
				t.Errorf("len(AudioTracks) = %d, want %d", len(p.AudioTracks), tc.wantAudio)
			}
			if len(p.SubtitleTracks) != tc.wantSubtitle {
				t.Errorf("len(SubtitleTracks) = %d, want %d", len(p.SubtitleTracks), tc.wantSubtitle)
			}
			if p.ProjectName == "" {
				t.Error("ProjectName is empty")
			}
			if p.EncoderParam == "" {
				t.Error("EncoderParam is empty")
			}
			if p.ConfigFilePath == "" {
				t.Error("ConfigFilePath was not filled in")
			}
		})
	}
}

func TestParseDemoProfileFields(t *testing.T) {
	t.Parallel()
	p := loadExample(t, "demo.json")

	// The format is frozen; these assertions pin the field-by-field mapping.
	if p.VSVersion != "2024H1" {
		t.Errorf("VSVersion = %q, want %q", p.VSVersion, "2024H1")
	}
	if p.ProjectName != "Demo - 1080p" {
		t.Errorf("ProjectName = %q", p.ProjectName)
	}
	if p.ContainerFormat != "mkv" {
		t.Errorf("ContainerFormat = %q, want the raw value before validation", p.ContainerFormat)
	}
	if p.Fps != 23.976 {
		t.Errorf("Fps = %v, want 23.976", p.Fps)
	}
	if !p.Rpc {
		t.Error("Rpc = false, want true")
	}
	if p.InputScript != "demo.vpy" {
		t.Errorf("InputScript = %q", p.InputScript)
	}
	if len(p.InputFiles) != 3 {
		t.Fatalf("len(InputFiles) = %d, want 3", len(p.InputFiles))
	}
	if p.InputFiles[0] != `Main_Disc\BDMV\STREAM\00000.m2ts` {
		t.Errorf("InputFiles[0] = %q, want the raw relative path", p.InputFiles[0])
	}

	// Audio tracks: [0] is the main FLAC track, [1] is an optional AAC
	// commentary track. Optional and Name must survive parsing.
	if got := p.AudioTracks[0].OutputCodec; got != "flac" {
		t.Errorf("AudioTracks[0].OutputCodec = %q, want %q", got, "flac")
	}
	if got := p.AudioTracks[1].OutputCodec; got != "aac" {
		t.Errorf("AudioTracks[1].OutputCodec = %q, want %q", got, "aac")
	}
	if got := p.AudioTracks[1].Bitrate; got != 192 {
		t.Errorf("AudioTracks[1].Bitrate = %d, want 192", got)
	}
	if got := p.AudioTracks[1].Name; got != "Commentary" {
		t.Errorf("AudioTracks[1].Name = %q, want %q", got, "Commentary")
	}
	if !p.AudioTracks[1].Optional {
		t.Error("AudioTracks[1].Optional = false, want true")
	}
	if got := p.AudioTracks[1].Language; got != "eng" {
		t.Errorf("AudioTracks[1].Language = %q, want %q", got, "eng")
	}
	if got := p.SubtitleTracks[0].Language; got != "jpn" {
		t.Errorf("SubtitleTracks[0].Language = %q, want %q", got, "jpn")
	}
}

func TestParseVfrProfileSetsTimeCode(t *testing.T) {
	t.Parallel()
	p := loadExample(t, "vfr.json")
	if !p.TimeCode {
		t.Error("TimeCode = false, want true")
	}
	if p.Fps != 0 {
		t.Errorf("Fps = %v, want 0 (VFR profiles omit it)", p.Fps)
	}
}

func TestParseReEncodeConfig(t *testing.T) {
	t.Parallel()
	// 00001.m2ts.json is an EpisodeConfig, not a task profile.
	path := filepath.Join(exampleDir, "00001.m2ts.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("example config not available: %v", err)
	}
	cfg, err := parseEpisodeConfigRaw(raw)
	if err != nil {
		t.Fatalf("parse episode config: %v", err)
	}
	if !cfg.EnableReEncode {
		t.Error("EnableReEncode = false, want true")
	}
	if cfg.ReExtractSource {
		t.Error("ReExtractSource = true, want false")
	}
	if got := cfg.ReEncodeOldFile; got != `output\BDMV\STREAM\00002.m2ts [6BB45BA9].mkv` {
		t.Errorf("ReEncodeOldFile = %q", got)
	}
	// Three raw slices, two after validation: [40,2400] and [2400,2900] are
	// contiguous and merge into [40,2900]. This mirrors what the legacy
	// SliceInfoArray.CheckAndMerge produced.
	if len(cfg.ReEncodeSliceArray) != 2 {
		t.Fatalf("len(ReEncodeSliceArray) = %d, want 2 after merging: %v",
			len(cfg.ReEncodeSliceArray), cfg.ReEncodeSliceArray)
	}
	if len(cfg.VspipeArgs) != 2 {
		t.Errorf("len(VspipeArgs) = %d, want 2", len(cfg.VspipeArgs))
	}
}

func TestValidateAcceptsRealProfiles(t *testing.T) {
	t.Parallel()
	for _, file := range []string{"demo.json", "demo_720p.json", "vfr.json"} {
		t.Run(file, func(t *testing.T) {
			t.Parallel()
			p := loadExample(t, file)
			in := Inputs{
				InstalledVSVersion: p.VSVersion,
				VpyRead:            true,
				VpyText:            "# OKE:INPUTFILE arg=''\" + r\"'\n" + ".set_output(1)\n",
				InputExists:        func(string) bool { return true },
				ToolchainEncoder:   func(EncoderType) (string, bool) { return "/tools/x265", true },
			}
			if err := Validate(p, in); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if p.VideoFormat == "" {
				t.Error("VideoFormat was not filled in by Validate")
			}
			if p.FpsNum == 0 || p.FpsDen == 0 {
				t.Error("frame rate was not normalized to a rational")
			}
		})
	}
}

func TestValidateNormalizesEncoderAndContainer(t *testing.T) {
	t.Parallel()
	p := loadExample(t, "demo_720p.json")
	if err := Validate(p, baseInputs(p)); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	// The profile says "x264" and "mp4"; both must come out canonicalised.
	if p.EncoderType != "x264" {
		t.Errorf("EncoderType = %q, want %q", p.EncoderType, "x264")
	}
	if p.ContainerFormat != "MP4" {
		t.Errorf("ContainerFormat = %q, want %q", p.ContainerFormat, "MP4")
	}
	if p.VideoFormat != "AVC" {
		t.Errorf("VideoFormat = %q, want %q", p.VideoFormat, "AVC")
	}
	if p.AudioFormat != "AAC" {
		t.Errorf("AudioFormat = %q, want %q", p.AudioFormat, "AAC")
	}
}

func TestValidateFpsNormalization(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		fps     float64
		num     int64
		den     int64
		wantNum int64
		wantDen int64
		wantErr bool
	}{
		{"friendly 23.976", 23.976, 0, 0, 24000, 1001, false},
		{"friendly 25", 25, 0, 0, 25, 1, false},
		{"friendly 59.94", 59.940, 0, 0, 60000, 1001, false},
		{"explicit rational wins", 0, 24000, 1001, 24000, 1001, false},
		{"unknown rate needs rationals", 12.345, 0, 0, 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &Profile{
				Version: 3, VSVersion: "2024H1", EncoderType: "x265",
				ContainerFormat: "mkv", Fps: tc.fps, FpsNum: tc.num, FpsDen: tc.den,
			}
			err := Validate(p, baseInputs(p))
			if tc.wantErr {
				if err == nil {
					t.Fatal("Validate() = nil, want a frame-rate error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if p.FpsNum != tc.wantNum || p.FpsDen != tc.wantDen {
				t.Errorf("fps = %d/%d, want %d/%d", p.FpsNum, p.FpsDen, tc.wantNum, tc.wantDen)
			}
		})
	}
}

func TestValidateRejectsDeprecatedOptions(t *testing.T) {
	t.Parallel()
	raw := `{"Version":3,"VSVersion":"2024H1","EncoderType":"x265","ContainerFormat":"mkv","SkipMuxing":true}`
	_, err := Parse(raw, "x.json")
	if err == nil {
		t.Fatal("Parse() = nil, want a deprecated-option error")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	if ve.Field != "SkipMuxing" {
		t.Errorf("Field = %q, want %q", ve.Field, "SkipMuxing")
	}
}

func TestValidateRejectsBadProfiles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*Profile)
		wantFld string
	}{
		{"wrong version", func(p *Profile) { p.Version = 1 }, "Version"},
		{"missing VSVersion", func(p *Profile) { p.VSVersion = "" }, "VSVersion"},
		{"VS version mismatch", func(p *Profile) { p.VSVersion = "2099H9" }, "VSVersion"},
		{"unknown encoder", func(p *Profile) { p.EncoderType = "x266" }, "EncoderType"},
		{"unknown container", func(p *Profile) { p.ContainerFormat = "avi" }, "ContainerFormat"},
		{"mp4 with VFR", func(p *Profile) {
			p.ContainerFormat = "mp4"
			p.TimeCode = true
		}, "TimeCode"},
		{"missing fps", func(p *Profile) {
			p.Fps = 0
			p.FpsNum, p.FpsDen = 0, 0
		}, "Fps"},
		{"audio without codec", func(p *Profile) {
			p.AudioTracks = []AudioTrackSpec{{}}
		}, "AudioTracks[0].OutputCodec"},
		{"unsupported audio codec", func(p *Profile) {
			p.AudioTracks = []AudioTrackSpec{{OutputCodec: "opus"}}
		}, "AudioTracks[0].OutputCodec"},
		{"flac in mp4", func(p *Profile) {
			p.ContainerFormat = "mp4"
			p.AudioTracks = []AudioTrackSpec{{OutputCodec: "flac"}}
		}, "AudioTracks[0].OutputCodec"},
		{"quality out of range", func(p *Profile) {
			q := 200
			p.AudioTracks = []AudioTrackSpec{{OutputCodec: "aac", Quality: &q}}
		}, "AudioTracks[0].Quality"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := loadExample(t, "demo.json")
			tc.mutate(p)
			in := baseInputs(p)
			// The VS version check only fires when the installed version is
			// known, so make it explicit for that case.
			in.InstalledVSVersion = "2024H1"
			err := Validate(p, in)
			if err == nil {
				t.Fatal("Validate() = nil, want an error")
			}
			var ve *ValidationError
			ok := errors.As(err, &ve)
			if !ok {
				t.Fatalf("error type = %T, want *ValidationError", err)
			}
			if ve.Field != tc.wantFld {
				t.Errorf("Field = %q, want %q (detail: %s)", ve.Field, tc.wantFld, ve.Detail)
			}
		})
	}
}

func TestValidateVpyRequirements(t *testing.T) {
	t.Parallel()
	t.Run("missing vpy", func(t *testing.T) {
		t.Parallel()
		p := loadExample(t, "demo.json")
		in := baseInputs(p)
		in.VpyRead = false
		if err := Validate(p, in); err == nil {
			t.Fatal("Validate() = nil, want a missing-script error")
		}
	})
	t.Run("rpc output missing", func(t *testing.T) {
		t.Parallel()
		p := loadExample(t, "demo.json")
		in := baseInputs(p)
		in.VpyText = "# OKE:INPUTFILE arg='x'\n"
		if err := Validate(p, in); err == nil {
			t.Fatal("Validate() = nil, want an rpc-output error")
		}
	})
	t.Run("input tag missing", func(t *testing.T) {
		t.Parallel()
		p := loadExample(t, "demo.json")
		in := baseInputs(p)
		in.VpyText = ".set_output(1)\n"
		if err := Validate(p, in); err == nil {
			t.Fatal("Validate() = nil, want a missing-tag error")
		}
	})
}

func TestValidateInputFileChecks(t *testing.T) {
	t.Parallel()
	t.Run("duplicate inputs", func(t *testing.T) {
		t.Parallel()
		p := loadExample(t, "demo.json")
		p.InputFiles = []string{"a.m2ts", "a.m2ts"}
		in := baseInputs(p)
		in.InputExists = func(string) bool { return true }
		err := Validate(p, in)
		if err == nil {
			t.Fatal("Validate() = nil, want a duplicate-input error")
		}
	})
	t.Run("missing input", func(t *testing.T) {
		t.Parallel()
		p := loadExample(t, "demo.json")
		in := baseInputs(p)
		in.InputExists = func(string) bool { return false }
		if err := Validate(p, in); err == nil {
			t.Fatal("Validate() = nil, want a missing-input error")
		}
	})
}

func TestInputTagPatternMatchesRealScript(t *testing.T) {
	t.Parallel()
	// The exact tag form used by the shipped demo scripts.
	script := `# OKE:INPUTFILE arg="" + r""` + "\n" + `clip = core.lsmas.LWLibavSource("")` + "\n"
	if !InputTagPattern.MatchString(script) {
		t.Fatalf("InputTagPattern did not match %q", script)
	}
	variants := []string{
		"#OKE:INPUTFILE arg='x'\n",
		"# oke:inputfile arg='x'\n",
		"#   OKE:INPUTFILE   arg = 'x'\n",
	}
	for _, v := range variants {
		if !InputTagPattern.MatchString(v) {
			t.Errorf("InputTagPattern did not match %q", v)
		}
	}
	if InputTagPattern.MatchString("no tag here\n") {
		t.Error("InputTagPattern matched a script without a tag")
	}
}

func TestEpisodeConfigValidationMergesSlices(t *testing.T) {
	t.Parallel()
	cfg := &EpisodeConfig{
		EnableReEncode:  true,
		ReEncodeOldFile: "old.mkv",
		// Deliberately unsorted and with a contiguous pair, mirroring
		// 00001.m2ts.json.
		ReEncodeSliceArray: []model.SliceInfo{
			{Begin: 2400, End: 2900},
			{Begin: 4500, End: -1},
			{Begin: 40, End: 2400},
		},
	}
	if err := ValidateEpisodeConfig(cfg); err != nil {
		t.Fatalf("ValidateEpisodeConfig() error = %v", err)
	}
	if len(cfg.ReEncodeSliceArray) != 2 {
		t.Fatalf("got %d slices, want 2 after merging: %v", len(cfg.ReEncodeSliceArray), cfg.ReEncodeSliceArray)
	}
	if cfg.ReEncodeSliceArray[0].Begin != 40 || cfg.ReEncodeSliceArray[0].End != 2900 {
		t.Errorf("first slice = %v, want [40, 2900]", cfg.ReEncodeSliceArray[0])
	}
	if cfg.ReEncodeSliceArray[1].End != -1 {
		t.Errorf("second slice end = %d, want -1 (open ended)", cfg.ReEncodeSliceArray[1].End)
	}
}

func TestEpisodeConfigRejectsOverlappingSlices(t *testing.T) {
	t.Parallel()
	cfg := &EpisodeConfig{
		EnableReEncode:     true,
		ReEncodeOldFile:    "old.mkv",
		ReEncodeSliceArray: []model.SliceInfo{{Begin: 0, End: 500}, {Begin: 400, End: 900}},
	}
	if err := ValidateEpisodeConfig(cfg); err == nil {
		t.Fatal("ValidateEpisodeConfig() = nil, want an overlap error")
	}
}

func TestEpisodeConfigRejectsIllegalSlice(t *testing.T) {
	t.Parallel()
	cfg := &EpisodeConfig{
		EnableReEncode:     true,
		ReEncodeOldFile:    "old.mkv",
		ReEncodeSliceArray: []model.SliceInfo{{Begin: 500, End: 100}},
	}
	if err := ValidateEpisodeConfig(cfg); err == nil {
		t.Fatal("ValidateEpisodeConfig() = nil, want an illegal-slice error")
	}
}

func TestAudioSpecToModelAppliesDefaults(t *testing.T) {
	t.Parallel()
	// The legacy code used jpn as the default language and 192 as the default
	// bitrate; both must survive the conversion.
	got := AudioSpecToModel(AudioTrackSpec{OutputCodec: "aac"})
	if got.Bitrate != DefaultBitrate {
		t.Errorf("Bitrate = %d, want %d", got.Bitrate, DefaultBitrate)
	}
	if got.Language != model.DefaultLanguage {
		t.Errorf("Language = %q, want %q", got.Language, model.DefaultLanguage)
	}
	if got.OutputCodec != "AAC" {
		t.Errorf("OutputCodec = %q, want %q", got.OutputCodec, "AAC")
	}
	if got.Order != model.MaxOrder {
		t.Errorf("Order = %d, want %d", got.Order, model.MaxOrder)
	}
}

func TestAudioSpecToModelHonoursExplicitValues(t *testing.T) {
	t.Parallel()
	q := 100
	got := AudioSpecToModel(AudioTrackSpec{
		OutputCodec: "aac", Bitrate: 320, Quality: &q,
		Language: "eng", Name: "Commentary", Optional: true,
		MuxOption: model.MuxOptionMka, Order: 2,
	})
	if got.Bitrate != 320 {
		t.Errorf("Bitrate = %d, want 320", got.Bitrate)
	}
	if got.Quality == nil || *got.Quality != 100 {
		t.Errorf("Quality = %v, want 100", got.Quality)
	}
	if got.Language != "eng" || got.Name != "Commentary" || !got.Optional {
		t.Errorf("track metadata not preserved: %+v", got)
	}
	if got.Mux != model.MuxOptionMka {
		t.Errorf("Mux = %v, want Mka", got.Mux)
	}
	if got.Order != 2 {
		t.Errorf("Order = %d, want 2", got.Order)
	}
}

func TestDeprecatedOptionFoundIsCaseInsensitive(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{"SkipMuxing":true}`,
		`{"skipmuxing":true}`,
		`{"SKIPMUXING":true}`,
	} {
		if got := DeprecatedOptionFound(raw); got != "SkipMuxing" {
			t.Errorf("DeprecatedOptionFound(%q) = %q, want SkipMuxing", raw, got)
		}
	}
	if got := DeprecatedOptionFound(`{"Version":3}`); got != "" {
		t.Errorf("DeprecatedOptionFound() = %q, want empty", got)
	}
}

func TestProfileStringMentionsKeyFields(t *testing.T) {
	t.Parallel()
	p := loadExample(t, "demo.json")
	_ = Validate(p, baseInputs(p))
	s := p.String()
	for _, want := range []string{"Demo - 1080p", "x265", "HEVC", "MKV"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() = %q, want it to mention %q", s, want)
		}
	}
}

// loadExample reads and parses one of the shipped example profiles.
func loadExample(t *testing.T, name string) *Profile {
	t.Helper()
	path := filepath.Join(exampleDir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("example profile %s not available: %v", name, err)
	}
	p, err := Parse(string(raw), path)
	if err != nil {
		t.Fatalf("Parse(%s) error = %v", name, err)
	}
	return p
}

func baseInputs(p *Profile) Inputs {
	return Inputs{
		InstalledVSVersion: p.VSVersion,
		VpyRead:            true,
		VpyText:            "# OKE:INPUTFILE arg='x'\n.set_output(1)\n",
		InputExists:        func(string) bool { return true },
		ToolchainEncoder:   func(EncoderType) (string, bool) { return "/tools/encoder", true },
	}
}
