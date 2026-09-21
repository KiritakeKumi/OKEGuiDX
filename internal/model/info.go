package model

// InfoType distinguishes the concrete kind of an Info value.
type InfoType int

// Info kinds.
const (
	InfoTypeDefault InfoType = iota
	InfoTypeVideo
	InfoTypeAudio
)

// String implements fmt.Stringer.
func (t InfoType) String() string {
	switch t {
	case InfoTypeVideo:
		return "Video"
	case InfoTypeAudio:
		return "Audio"
	default:
		return "Default"
	}
}

// MuxOption tells the muxer what to do with a track. The names match the JSON
// values accepted by the legacy profile format.
type MuxOption int

// Mux options.
const (
	MuxOptionDefault MuxOption = iota
	MuxOptionMka
	MuxOptionExternal
	MuxOptionExtractOnly
	MuxOptionSkip
)

// String implements fmt.Stringer.
func (m MuxOption) String() string {
	switch m {
	case MuxOptionMka:
		return "Mka"
	case MuxOptionExternal:
		return "External"
	case MuxOptionExtractOnly:
		return "ExtractOnly"
	case MuxOptionSkip:
		return "Skip"
	default:
		return "Default"
	}
}

// MarshalText implements encoding.TextMarshaler.
func (m MuxOption) MarshalText() ([]byte, error) { return []byte(m.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler. Only the string names are
// accepted; a bare number is rejected (encoding/json reports "JSON value must be
// string" for a TextUnmarshaler).
//
// That is deliberate, and it is not a compatibility defect. The frozen profile
// format uses the names: the shipped dist/windows/examples/demo_720p.json spells
// "MuxOption" : "Skip", and the legacy loader is YamlDotNet 11.1.1
// (AddTaskService.LoadJsonAsProfile ->
// new DeserializerBuilder().IgnoreUnmatchedProperties().Build()), whose
// ScalarNodeDeserializer does `Enum.Parse(underlyingType, scalar.Value, true)`.
// Enum.Parse also accepts the numeric index, so "MuxOption" : 4 would have
// loaded in C# - but that is a leniency of the library's scalar conversion, not
// part of the format. Newtonsoft.Json's enum converter has the same numeric
// leniency, and it is likewise not exercised by the profile path.
//
// Crucially, the legacy application never WROTE a profile: the only serializer
// in the whole legacy tree writes OKEGuiConfig.json (Utils/Initializer.cs
// WriteConfig) and the rpc result files (JobProcessor/RpChecker/RpChecker.cs);
// there is no JsonConvert.SerializeObject of TaskProfile anywhere, and MuxOption
// appears in no XAML, so it is not a wizard-editable field either. A number in
// MuxOption could therefore only ever be a hand-written input, never something
// the old GUI emitted - so refusing it cannot break an existing file.
func (m *MuxOption) UnmarshalText(b []byte) error {
	*m = ParseMuxOption(string(b))
	return nil
}

// ParseMuxOption parses a JSON MuxOption value, ignoring case as the legacy
// deserializer did. Only the names Default/Mka/External/ExtractOnly/Skip are
// accepted; see UnmarshalText for why a numeric index is not part of the frozen
// format.
func ParseMuxOption(s string) MuxOption {
	switch lowerASCII(s) {
	case "mka":
		return MuxOptionMka
	case "external":
		return MuxOptionExternal
	case "extractonly":
		return MuxOptionExtractOnly
	case "skip":
		return MuxOptionSkip
	default:
		return MuxOptionDefault
	}
}

// DefaultLanguage is the language assigned to tracks that do not specify one.
// Mirrors Constants.language in the legacy code.
const DefaultLanguage = "jpn"

// Info is the common metadata shared by every track.
type Info struct {
	InfoType InfoType  `json:"info_type"`
	Mux      MuxOption `json:"mux_option"`
	Language string    `json:"language"`
	Name     string    `json:"name"`
	Optional bool      `json:"optional"`
	// Order controls track placement; MaxInt means "append".
	Order int `json:"order"`
	// DupOrEmpty marks a track detected as a duplicate or as silent.
	// Setting it downgrades Default/Mka/External muxing to ExtractOnly,
	// exactly as the legacy DupOrEmpty setter did.
	DupOrEmpty bool `json:"dup_or_empty"`
}

// NewInfo returns an Info with the legacy defaults applied.
func NewInfo() Info {
	return Info{Language: DefaultLanguage, Order: MaxOrder}
}

// MaxOrder is the "append at the end" sentinel for Info.Order.
const MaxOrder = int(^uint(0) >> 1) // math.MaxInt

// SetDupOrEmpty applies the legacy side effect: a duplicate or empty track is
// never muxed into the main container.
func (i *Info) SetDupOrEmpty(v bool) {
	i.DupOrEmpty = v
	if !v {
		return
	}
	switch i.Mux {
	case MuxOptionDefault, MuxOptionMka, MuxOptionExternal:
		i.Mux = MuxOptionExtractOnly
	}
}

// AudioInfo describes an audio track to be produced.
type AudioInfo struct {
	Info
	OutputCodec string `json:"output_codec"`
	Bitrate     int    `json:"bitrate"`
	// Quality is the QAAC VBR quality (0..127). nil means "use Bitrate".
	Quality *int `json:"quality"`
	Lossy   bool `json:"lossy"`
	// Length is the container runtime in whole seconds, taken from the demuxer's
	// header line. The reference computes it as hour*3600 + minute*60 + second,
	// so it is seconds, not milliseconds.
	Length int `json:"length"`
}

// VideoInfo describes a video track.
type VideoInfo struct {
	Info
	FpsNum       int64   `json:"fps_num"`
	FpsDen       int64   `json:"fps_den"`
	TimeCodeFile FileRef `json:"timecode_file"`
	QpFile       FileRef `json:"qp_file"`
	// IFrames lists the source frame indices that are I-frames, used by
	// re-encode slicing.
	IFrames []int64 `json:"iframes"`
}

// NewVideoInfo returns a VideoInfo with the legacy default denominator of 1.
func NewVideoInfo() VideoInfo {
	return VideoInfo{Info: NewInfo(), FpsDen: 1}
}

// FPS returns the frame rate as a float.
func (v VideoInfo) FPS() float64 {
	if v.FpsDen == 0 {
		return 0
	}
	return float64(v.FpsNum) / float64(v.FpsDen)
}

// VideoSliceInfo describes one re-encode slice.
type VideoSliceInfo struct {
	VideoInfo
	IsReEncode bool      `json:"is_reencode"`
	FrameRange SliceInfo `json:"frame_range"`
	PartID     int       `json:"part_id"`
}
