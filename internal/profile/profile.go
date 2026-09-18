// Package profile parses and validates OKEGui task profiles (the .json files a
// technical director writes next to a .vpy script).
//
// The on-disk format is FROZEN. Existing profiles must keep working unchanged,
// which is why field names, casing and the tolerant number handling below
// mirror the legacy Newtonsoft/YamlDotNet deserialization exactly.
package profile

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// Supported schema versions. Version 2 has no VSVersion field; version 3
// requires it and it must match the installed VapourSynth build.
const (
	VersionMin = 2
	VersionMax = 3
)

// EncoderType is the video encoder selected by the profile.
type EncoderType string

// Supported encoders. Values are lowercase because the legacy code lowercased
// the field before switching on it.
const (
	EncoderX264   EncoderType = "x264"
	EncoderX265   EncoderType = "x265"
	EncoderSVTAV1 EncoderType = "svtav1"
)

// VideoFormat returns the format name used in output file names and muxer
// metadata for this encoder.
func (e EncoderType) VideoFormat() string {
	switch e {
	case EncoderX264:
		return "AVC"
	case EncoderX265:
		return "HEVC"
	case EncoderSVTAV1:
		return "AV1"
	default:
		return ""
	}
}

// Container is the output container format.
type Container string

// Supported containers.
const (
	ContainerMKV Container = "MKV"
	ContainerMP4 Container = "MP4"
)

// AudioFormat is the codec of the primary audio track.
type AudioFormat string

// Supported audio output codecs.
const (
	AudioFLAC AudioFormat = "FLAC"
	AudioAAC  AudioFormat = "AAC"
	AudioAC3  AudioFormat = "AC3"
	AudioDTS  AudioFormat = "DTS"
	AudioEAC3 AudioFormat = "EAC3"
)

// Profile is a task profile. Field names follow the frozen JSON format, not Go
// style; see TaskProfile.cs in the legacy code for the source of truth.
type Profile struct {
	Version     int    `json:"Version"`
	VSVersion   string `json:"VSVersion"`
	ProjectName string `json:"ProjectName"`
	EncoderType string `json:"EncoderType"`
	// Encoder is an optional explicit encoder path. When empty the toolchain
	// resolves the platform default.
	Encoder               string           `json:"Encoder"`
	EncoderParam          string           `json:"EncoderParam"`
	ContainerFormat       string           `json:"ContainerFormat"`
	Fps                   float64          `json:"Fps"`
	FpsNum                int64            `json:"FpsNum"`
	FpsDen                int64            `json:"FpsDen"`
	AudioTracks           []AudioTrackSpec `json:"AudioTracks"`
	InputScript           string           `json:"InputScript"`
	SubtitleTracks        []TrackSpec      `json:"SubtitleTracks"`
	InputFiles            []string         `json:"InputFiles"`
	Config                *EpisodeConfig   `json:"Config"`
	Rpc                   bool             `json:"Rpc"`
	TimeCode              bool             `json:"TimeCode"`
	RenumberChapters      bool             `json:"RenumberChapters"`
	SkipAllAudioTracks    bool             `json:"SkipAllAudioTracks"`
	SkipAllSubtitleTracks bool             `json:"SkipAllSubtitleTracks"`

	// Fields filled in during processing; not read from the profile file.
	VideoFormat       string `json:"VideoFormat"`
	AudioFormat       string `json:"AudioFormat"`
	WorkingPathPrefix string `json:"WorkingPathPrefix"`
	OutputPathPrefix  string `json:"OutputPathPrefix"`
	ConfigFilePath    string `json:"ConfigFilePath"`
	IsReEncode        bool   `json:"IsReEncode"`
}

// AudioTrackSpec is one entry of AudioTracks.
type AudioTrackSpec struct {
	OutputCodec string          `json:"OutputCodec"`
	Bitrate     int             `json:"Bitrate"`
	Quality     *int            `json:"Quality"`
	MuxOption   model.MuxOption `json:"MuxOption"`
	Language    string          `json:"Language"`
	Name        string          `json:"Name"`
	Optional    bool            `json:"Optional"`
	Order       int             `json:"Order"`
}

// TrackSpec is one entry of SubtitleTracks.
type TrackSpec struct {
	MuxOption model.MuxOption `json:"MuxOption"`
	Language  string          `json:"Language"`
	Name      string          `json:"Name"`
	Optional  bool            `json:"Optional"`
	Order     int             `json:"Order"`
}

// EpisodeConfig is the per-episode configuration, stored in a separate json file
// referenced from the profile's Config field and used by re-encode tasks.
type EpisodeConfig struct {
	VspipeArgs         []string          `json:"VspipeArgs"`
	EnableReEncode     bool              `json:"EnableReEncode"`
	ReExtractSource    bool              `json:"ReExtractSource"`
	ReEncodeOldFile    string            `json:"ReEncodeOldFile"`
	ReEncodeSliceArray []model.SliceInfo `json:"ReEncodeSliceArray"`
}

// DefaultBitrate is the audio bitrate used when the profile does not specify
// one. Mirrors Constants.QAACBitrate.
const DefaultBitrate = 192

// QAAC quality bounds, mirroring Constants.QAACQualityMin/Max.
const (
	QualityMin = 0
	QualityMax = 127
)

// DeprecatedOptions are option names that must not appear anywhere in a profile
// file. Mirrors Constants.deprecatedOptions.
var DeprecatedOptions = []string{"SkipMuxing", "IncludeSub", "SubtitleLanguage"}

// UnmarshalJSON implements a tolerant decoder for Profile.
//
// The legacy profile format is JSON, but early profiles were also accepted as
// YAML by the old loader (YamlDotNet was wired in). To keep every historical
// file working, parsing falls back to a permissive path when strict JSON
// decoding fails.
func (p *Profile) UnmarshalJSON(data []byte) error {
	type alias Profile
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*p = Profile(a)
	return nil
}

// DeprecatedOptionFound returns the first deprecated option name present in the
// raw profile text, or "" when the text is clean.
func DeprecatedOptionFound(raw string) string {
	for _, opt := range DeprecatedOptions {
		if strings.Contains(strings.ToLower(raw), strings.ToLower(opt)) {
			return opt
		}
	}
	return ""
}

// KnownFps maps the friendly frame rates accepted in Fps to exact rationals.
// Mirrors the switch in AddTaskService.ProcessJsonProfile.
var KnownFps = []struct {
	Fps float64
	Num int64
	Den int64
}{
	{1.0, 1, 1},
	{23.976, 24000, 1001},
	{24.000, 24, 1},
	{25.000, 25, 1},
	{29.970, 30000, 1001},
	{30.000, 30, 1},
	{50.000, 50, 1},
	{59.940, 60000, 1001},
	{60.000, 60, 1},
}

// FpsExact returns the exact numerator/denominator for a friendly frame rate.
// The second result is false when the rate is not one of the known values.
func FpsExact(fps float64) (num, den int64, ok bool) {
	for _, k := range KnownFps {
		if fps == k.Fps {
			return k.Num, k.Den, true
		}
	}
	return 0, 0, false
}

// String implements fmt.Stringer with the same shape as the legacy ToString.
func (p *Profile) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "项目名字: %s", p.ProjectName)
	fmt.Fprintf(&b, "\n\n编码器类型: %s", p.EncoderType)
	fmt.Fprintf(&b, "\n编码器路径: %s", p.Encoder)
	param := p.EncoderParam
	if len(param) > 30 {
		param = param[:30]
	}
	fmt.Fprintf(&b, "\n编码参数: %s......", param)
	fmt.Fprintf(&b, "\n\n封装格式: %s", p.ContainerFormat)
	fmt.Fprintf(&b, "\n视频编码: %s", p.VideoFormat)
	if p.TimeCode {
		b.WriteString("\n视频帧率: VFR")
	} else {
		fmt.Fprintf(&b, "\n视频帧率: %.3f fps", p.Fps)
	}
	fmt.Fprintf(&b, "\n音频编码(主音轨): %s", p.AudioFormat)
	if p.RenumberChapters {
		b.WriteString("\n章节名重编号: YES")
	}
	fmt.Fprintf(&b, "\n输入文件数量: %d", len(p.InputFiles))
	return b.String()
}
