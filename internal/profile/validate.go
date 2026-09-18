package profile

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// ValidationError is returned when a profile is syntactically valid JSON but
// semantically unusable. Summary is a short title suitable for a UI header,
// Detail explains what to fix. This replaces the ~50 MessageBox.Show calls in
// the legacy code (see INVENTORY.md §2 #16).
type ValidationError struct {
	// Summary is a short, stable title.
	Summary string
	// Detail is the human-readable explanation.
	Detail string
	// Field names the offending profile field, when known.
	Field string
}

// Error implements error.
func (e *ValidationError) Error() string {
	if e.Field == "" {
		return e.Summary + ": " + e.Detail
	}
	return fmt.Sprintf("%s (%s): %s", e.Summary, e.Field, e.Detail)
}

func invalid(field, summary, format string, args ...any) *ValidationError {
	return &ValidationError{Summary: summary, Detail: fmt.Sprintf(format, args...), Field: field}
}

// Inputs is everything validation needs from the outside world. Passing it in
// keeps this package free of filesystem and process access, which makes it
// trivially testable.
type Inputs struct {
	// InstalledVSVersion is the version string of the installed VapourSynth,
	// read from the VERSION file next to vspipe. Empty when unknown.
	InstalledVSVersion string
	// VpyText is the content of the .vpy script, when it could be read.
	VpyText string
	// VpyRead is true when VpyText reflects an existing, readable script.
	VpyRead bool
	// InputExists reports whether a profile-relative input path exists.
	InputExists func(rel string) bool
	// ResolveEncoder maps an explicit encoder path (relative to the profile
	// directory) to an absolute one. When nil, the path is left as-is.
	ResolveEncoder func(rel string) (string, bool)
	// ToolchainEncoder is the absolute path of the platform's default encoder
	// for the requested EncoderType, when the toolchain could resolve one.
	ToolchainEncoder func(EncoderType) (string, bool)
}

// Validate applies every check the legacy AddTaskService performed, returning a
// structured error instead of popping dialogs.
func Validate(p *Profile, in Inputs) error {
	if p.Version != 2 && p.Version != 3 {
		return invalid("Version", "版本不对",
			"你是不是把单个文件追加用的json当成一套任务用的json了？当前 Version=%d，只接受 2 或 3。", p.Version)
	}

	if p.Version >= 3 {
		if p.VSVersion == "" {
			return invalid("VSVersion", "JSON错误",
				"v3版的 JSON 中没有指定VS版本（VSVersion），请联系总监修改。")
		}
		if in.InstalledVSVersion != "" && p.VSVersion != in.InstalledVSVersion {
			return invalid("VSVersion", "VS版本不对",
				"总监指定的VS版本是%s，但是OKEGui使用的版本是%s", p.VSVersion, in.InstalledVSVersion)
		}
	}

	p.EncoderType = strings.ToLower(p.EncoderType)
	switch EncoderType(p.EncoderType) {
	case EncoderX264, EncoderX265, EncoderSVTAV1:
	default:
		return invalid("EncoderType", "编码器版本错误", "EncoderType请填写x264/x265/svtav1")
	}
	p.VideoFormat = EncoderType(p.EncoderType).VideoFormat()

	p.ContainerFormat = strings.ToUpper(p.ContainerFormat)
	switch Container(p.ContainerFormat) {
	case ContainerMKV, ContainerMP4:
	default:
		return invalid("ContainerFormat", "封装格式指定的有问题", "MKV/MP4，只能这两种")
	}

	if Container(p.ContainerFormat) == ContainerMP4 && p.TimeCode {
		return invalid("TimeCode", "MP4暂不支持VFR封装", "MP4暂不支持VFR封装，请联系技术总监。")
	}

	if err := normalizeFps(p); err != nil {
		return err
	}
	if err := validateAudio(p); err != nil {
		return err
	}
	if err := validateSubtitle(p); err != nil {
		return err
	}
	if err := resolveEncoder(p, in); err != nil {
		return err
	}
	if err := validateVpy(p, in); err != nil {
		return err
	}
	return validateInputFiles(p, in)
}

func normalizeFps(p *Profile) error {
	if p.Fps <= 0 && (p.FpsNum <= 0 || p.FpsDen <= 0) {
		if p.TimeCode {
			p.Fps = 1
		} else {
			return invalid("Fps", "帧率没有指定诶",
				"现在json文件中需要指定帧率，哪怕 Fps : 23.976")
		}
	}
	if p.FpsNum > 0 && p.FpsDen > 0 {
		p.Fps = float64(p.FpsNum) / float64(p.FpsDen)
		return nil
	}
	num, den, ok := FpsExact(p.Fps)
	if !ok {
		return invalid("Fps", "不知道的帧率诶",
			"请通过FpsNum和FpsDen来指定（当前 Fps=%v）", p.Fps)
	}
	p.FpsNum, p.FpsDen = num, den
	return nil
}

func validateAudio(p *Profile) error {
	if len(p.AudioTracks) == 0 {
		p.AudioFormat = string(AudioAAC)
	} else {
		for i := range p.AudioTracks {
			ai := &p.AudioTracks[i]
			if ai.MuxOption != model.MuxOptionSkip && strings.TrimSpace(ai.OutputCodec) == "" {
				return invalid(fmt.Sprintf("AudioTracks[%d].OutputCodec", i), "音轨编码错误",
					"音轨未设置 OutputCodec，请检查大小写")
			}
			if ai.Quality != nil {
				if ai.Bitrate != DefaultBitrate && ai.Bitrate != 0 {
					return invalid(fmt.Sprintf("AudioTracks[%d]", i), "音轨编码错误",
						"音轨不能同时指定 Bitrate 和 Quality，请只保留其中一个")
				}
				if *ai.Quality < QualityMin || *ai.Quality > QualityMax {
					return invalid(fmt.Sprintf("AudioTracks[%d].Quality", i), "音轨编码错误",
						"音轨 Quality 的值必须介于 %d-%d 之间（闭区间），请检查", QualityMin, QualityMax)
				}
			}
		}
		p.AudioFormat = strings.ToUpper(p.AudioTracks[0].OutputCodec)
		if p.AudioFormat == "" {
			p.AudioFormat = string(AudioAAC)
		}
	}

	switch AudioFormat(p.AudioFormat) {
	case AudioFLAC, AudioAAC, AudioAC3, AudioDTS, AudioEAC3:
	default:
		return invalid("AudioTracks[0].OutputCodec", "音轨格式不支持",
			"目标音轨只能是FLAC/AAC/AC3/DTS/EAC3（当前 %s）", p.AudioFormat)
	}
	if AudioFormat(p.AudioFormat) == AudioFLAC && Container(p.ContainerFormat) == ContainerMP4 {
		return invalid("AudioTracks[0].OutputCodec", "音轨格式不支持", "MP4格式没法封FLAC")
	}
	return nil
}

func validateSubtitle(p *Profile) error {
	for i := range p.SubtitleTracks {
		// The legacy code only used Language/Name/MuxOption for subtitles and
		// rejected nothing here; kept for parity of the validation order.
		_ = i
	}
	return nil
}

func resolveEncoder(p *Profile, in Inputs) error {
	if p.Encoder != "" {
		if in.ResolveEncoder == nil {
			return nil
		}
		abs, ok := in.ResolveEncoder(p.Encoder)
		if !ok {
			return invalid("Encoder", "找不到编码器啊",
				"编码器好像不在json指定的地方（文件名错误？还有记得放在json文件同目录下）：%s", p.Encoder)
		}
		p.Encoder = abs
		return nil
	}
	if in.ToolchainEncoder == nil {
		return nil
	}
	abs, ok := in.ToolchainEncoder(EncoderType(p.EncoderType))
	if !ok {
		return invalid("Encoder", "找不到编码器啊",
			"工具链里没有可用于 %s 的编码器，请更新 tools 包。", p.EncoderType)
	}
	p.Encoder = abs
	return nil
}

func validateVpy(p *Profile, in Inputs) error {
	if !in.VpyRead {
		return invalid("InputScript", "vpy文件找不到",
			"指定的vpy文件没有找到，检查下json文件和vpy文件是不是放一起了？")
	}
	if p.Rpc && !strings.Contains(in.VpyText, ".set_output(1)") {
		return invalid("Rpc", "vpy里没有准备rpc的输出", "请告诉技术总监给vpy里加上rpc的输出。")
	}
	if !InputTagPattern.MatchString(in.VpyText) {
		return invalid("InputScript", "vpy没有为OKEGui设计", "vpy里没有#OKE:INPUTFILE的标签。")
	}
	return nil
}

func validateInputFiles(p *Profile, in Inputs) error {
	if in.InputExists == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(p.InputFiles))
	for _, f := range p.InputFiles {
		if _, dup := seen[f]; dup {
			return invalid("InputFiles", "输入文件有重复",
				"指定的文件(%s)重复了，请总监复查下输入文件列表？", f)
		}
		seen[f] = struct{}{}
		if !in.InputExists(f) {
			return invalid("InputFiles", "找不到输入文件啊",
				"指定的文件(%s)不存在啊，跟总监确认下json应该放哪？", f)
		}
	}
	return nil
}

// AudioSpecToModel converts a profile audio track into a model.AudioInfo.
func AudioSpecToModel(s AudioTrackSpec) model.AudioInfo {
	info := model.NewInfo()
	info.Mux = s.MuxOption
	if s.Language != "" {
		info.Language = s.Language
	}
	info.Name = s.Name
	info.Optional = s.Optional
	if s.Order != 0 {
		info.Order = s.Order
	}
	bitrate := s.Bitrate
	if bitrate == 0 {
		bitrate = DefaultBitrate
	}
	return model.AudioInfo{
		Info:        info,
		OutputCodec: strings.ToUpper(s.OutputCodec),
		Bitrate:     bitrate,
		Quality:     s.Quality,
	}
}

// TrackSpecToModel converts a profile subtitle track into a model.Info.
func TrackSpecToModel(s TrackSpec) model.Info {
	info := model.NewInfo()
	info.Mux = s.MuxOption
	if s.Language != "" {
		info.Language = s.Language
	}
	info.Name = s.Name
	info.Optional = s.Optional
	if s.Order != 0 {
		info.Order = s.Order
	}
	return info
}

// ErrNoProfileDirectory is returned when a profile path has no parent
// directory, which makes relative input resolution impossible.
var ErrNoProfileDirectory = errors.New("profile: cannot determine the profile directory")

// DirOf returns the directory that relative paths in the profile are resolved
// against.
func DirOf(profilePath string) (string, error) {
	dir := filepath.Dir(profilePath)
	if dir == "" || dir == "." {
		return "", ErrNoProfileDirectory
	}
	return dir, nil
}
