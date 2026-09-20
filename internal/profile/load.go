package profile

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/textfile"
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
	raw, err := textfile.Read(path)
	if err != nil {
		return nil, &ValidationError{
			Summary: "无法读取json文件",
			Detail:  err.Error(),
			Field:   "ConfigFilePath",
		}
	}
	return parseDecoded(string(raw), path)
}

// Parse decodes and validates a profile that is already in memory. The API
// accepts a profile body directly, so this is a second entry point alongside
// Load and it applies the same byte order mark handling: a client that posts a
// file it read from disk posts the mark too, and the legacy service accepted
// that. profilePath is used to fill in ConfigFilePath and to resolve relative
// input paths.
func Parse(raw string, profilePath string) (*Profile, error) {
	return parseDecoded(string(textfile.Decode([]byte(raw))), profilePath)
}

// parseDecoded is Parse's body, split out so the mark is consumed exactly once
// and Load can hand over bytes it already decoded.
func parseDecoded(raw string, profilePath string) (*Profile, error) {
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
	raw, err := textfile.Read(path)
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
// AddEpProfileService.ProcessJsonProfile, in the legacy order. The two checks
// that need the file system (does ReEncodeOldFile exist, and resolving it to an
// absolute path) belong to the layer that knows the profile's directory; the
// checks here are all expressible on the config alone.
func ValidateEpisodeConfig(cfg *EpisodeConfig) error {
	if !cfg.EnableReEncode {
		return nil
	}
	if cfg.ReEncodeOldFile == "" {
		return invalid("ReEncodeOldFile", "参数不完整", "ReEncode模式下必须指定旧版成品文件。")
	}
	// `if (!json.ReExtractSource && oldFileExtension != ".mkv")`: the old
	// deliverable is where the non-video tracks come from, and only an mkv can
	// supply them. ReExtractSource says to take those tracks from the original
	// source instead, which is why the check does not apply then.
	//
	// This must stay on the server. The wizard checks it too, but a request
	// that skips the page would otherwise reach the muxer with a file it
	// cannot read tracks from.
	if !cfg.ReExtractSource && strings.ToLower(filepath.Ext(cfg.ReEncodeOldFile)) != ".mkv" {
		return invalid("ReEncodeOldFile", "旧版压制成品格式不支持",
			"需要从旧版压制成品获取非视频轨道，但旧版压制成品不为mkv格式")
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
	// The inputs are echoed as the profile wrote them, which is usually a path
	// relative to the profile's own directory. This package cannot resolve
	// them: it never sees a file system, and the legacy code resolved against
	// the json directory in AddTaskService.LoadInputFiles, one layer up. Every
	// caller must therefore replace Inputs and Status.Input with resolved
	// absolute paths before the task reaches the queue, which is what the API's
	// selectInput and the CLI's loadTasks do. A relative value left here would
	// resolve against the process working directory instead.
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
