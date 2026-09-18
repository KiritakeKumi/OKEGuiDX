package profile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// InputTagPattern matches the "# OKE:INPUTFILE arg=" marker that the technical
// director places in a .vpy script so the engine knows where to inject the
// source path. Mirrors Constants.inputRegex.
var InputTagPattern = regexp.MustCompile(`(?mi)^# *OKE:INPUTFILE([\s]+\w+[ ]*=[ ]*)(r*["'].*["'])`)

// ProjectDirTagPattern matches "# OKE:PROJECTDIR". Mirrors
// Constants.projectDirRegex.
var ProjectDirTagPattern = regexp.MustCompile(`(?mi)^# *OKE:PROJECTDIR([\s]+\w+[ ]*=[ ]*)(r*["'].*["'])`)

// DebugTagPattern matches "# OKE:DEBUG". Mirrors Constants.debugRegex.
var DebugTagPattern = regexp.MustCompile(`(?mi)^# *OKE:DEBUG([\s]+[\w]+[ ]*=[ ]*)(\w+)`)

// Load reads a profile from disk. It rejects profiles containing deprecated
// options before parsing, exactly as the legacy loader did, and returns a
// *ValidationError so callers never have to inspect raw JSON errors.
func Load(path string) (*Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &ValidationError{
			Summary: "无法读取json文件",
			Detail:  err.Error(),
			Field:   "ConfigFilePath",
		}
	}
	return Parse(string(raw), path)
}

// Parse decodes and validates a profile. profilePath is used to fill in
// ConfigFilePath and to resolve relative input paths.
func Parse(raw string, profilePath string) (*Profile, error) {
	if opt := DeprecatedOptionFound(raw); opt != "" {
		return nil, &ValidationError{
			Summary: "json文件版本太老了",
			Detail:  opt + "已不再支持",
			Field:   opt,
		}
	}

	p := &Profile{}
	// Real profiles contain trailing commas; the frozen format must keep
	// accepting them (see tolerant_json.go).
	if err := json.Unmarshal([]byte(stripTrailingCommas(raw)), p); err != nil {
		return nil, &ValidationError{
			Summary: "json文件写错了诶",
			Detail:  err.Error(),
		}
	}

	if profilePath != "" {
		abs, err := filepath.Abs(profilePath)
		if err == nil {
			p.ConfigFilePath = abs
		} else {
			p.ConfigFilePath = profilePath
		}
	}
	return p, nil
}

// LoadEpisodeConfig reads the separate per-episode configuration file used by
// re-encode tasks.
func LoadEpisodeConfig(path string) (*EpisodeConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, &ValidationError{
			Summary: "无法读取json文件",
			Detail:  err.Error(),
			Field:   "Config",
		}
	}
	cfg, err := parseEpisodeConfigRaw(raw)
	if err != nil {
		return nil, err
	}
	return cfg, nil
}

// parseEpisodeConfigRaw decodes an episode config without touching the
// filesystem, so callers that already have the bytes (and tests) can use it.
func parseEpisodeConfigRaw(raw []byte) (*EpisodeConfig, error) {
	cfg := &EpisodeConfig{}
	if err := json.Unmarshal([]byte(stripTrailingCommas(string(raw))), cfg); err != nil {
		return nil, &ValidationError{
			Summary: "json文件写错了诶",
			Detail:  err.Error(),
			Field:   "Config",
		}
	}
	if err := ValidateEpisodeConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ValidateEpisodeConfig applies the re-encode checks from
// AddEpProfileService.ProcessJsonProfile. resolveOldFile maps the profile's
// relative ReEncodeOldFile to an absolute path.
func ValidateEpisodeConfig(cfg *EpisodeConfig) error {
	if !cfg.EnableReEncode {
		return nil
	}
	if cfg.ReEncodeOldFile == "" {
		return invalid("ReEncodeOldFile", "参数不完整", "ReEncode模式下必须指定旧版成品文件。")
	}
	if len(cfg.ReEncodeSliceArray) == 0 {
		return invalid("ReEncodeSliceArray", "参数不完整", "ReEncode模式下必须指定切片数组。")
	}
	for _, s := range cfg.ReEncodeSliceArray {
		if s.IsIllegal() {
			return invalid("ReEncodeSliceArray", "切片不合法", "切片%s不合法", s)
		}
	}
	sortSlices(cfg.ReEncodeSliceArray)
	merged, err := model.CheckAndMerge(cfg.ReEncodeSliceArray)
	if err != nil {
		return invalid("ReEncodeSliceArray", "切片重叠", "切片之间有重叠，请总监复查。")
	}
	cfg.ReEncodeSliceArray = merged
	return nil
}

func sortSlices(s []model.SliceInfo) {
	// Insertion sort: slice arrays are tiny (a handful of entries) and this
	// keeps the comparison identical to SliceInfoArray.Sorted.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Compare(s[j-1]) < 0; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ToModel converts a validated profile plus its episode config into the runtime
// task structure used by the engine.
func ToModel(p *Profile, cfg *EpisodeConfig) *model.Task {
	t := &model.Task{
		ID:      model.NewTaskID(),
		Name:    p.ProjectName,
		Profile: p,
		Config:  cfg,
		Status: model.TaskStatus{
			ID:       model.NewTaskID(),
			Name:     p.ProjectName,
			Enabled:  true,
			Progress: model.TaskWaiting,
			Status:   "等待中",
			Speed:    "0.0 fps",
		},
	}
	if cfg != nil {
		t.Config = cfg
	}
	for _, in := range p.InputFiles {
		t.Inputs = append(t.Inputs, model.NewFileRef(in))
	}
	if len(t.Inputs) > 0 {
		t.Status.Input = t.Inputs[0]
	}
	for _, a := range p.AudioTracks {
		t.AudioTracks = append(t.AudioTracks, AudioSpecToModel(a))
	}
	for _, s := range p.SubtitleTracks {
		t.SubtitleTracks = append(t.SubtitleTracks, TrackSpecToModel(s))
	}
	return t
}
