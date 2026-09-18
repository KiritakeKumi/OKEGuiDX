package model

// TrackType classifies a media track. Mirrors OKEGui's TrackType enum.
type TrackType int

// Track types, in the order they are muxed.
const (
	TrackTypeDefault TrackType = iota
	TrackTypeAudio
	TrackTypeSubtitle
	TrackTypeVideo
	TrackTypeChapter
)

// String implements fmt.Stringer, using the names of the legacy enum so that
// logs and persisted state stay comparable with the .NET version.
func (t TrackType) String() string {
	switch t {
	case TrackTypeAudio:
		return "Audio"
	case TrackTypeSubtitle:
		return "Subtitle"
	case TrackTypeVideo:
		return "Video"
	case TrackTypeChapter:
		return "Chapter"
	default:
		return "Default"
	}
}

// MarshalText implements encoding.TextMarshaler.
func (t TrackType) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *TrackType) UnmarshalText(b []byte) error {
	*t = ParseTrackType(string(b))
	return nil
}

// ParseTrackType converts a legacy enum name to a TrackType. Unknown values map
// to TrackTypeDefault.
func ParseTrackType(s string) TrackType {
	switch s {
	case "Audio":
		return TrackTypeAudio
	case "Subtitle":
		return TrackTypeSubtitle
	case "Video":
		return TrackTypeVideo
	case "Chapter":
		return TrackTypeChapter
	default:
		return TrackTypeDefault
	}
}

// Track is one media track: a file plus the metadata that describes it.
type Track struct {
	File      FileRef   `json:"file"`
	Info      Info      `json:"info"`
	TrackType TrackType `json:"track_type"`
}

// VideoTrack is a video track.
type VideoTrack struct {
	Track
	Video VideoInfo `json:"video"`
}

// AudioTrack is an audio track.
type AudioTrack struct {
	Track
	Audio AudioInfo `json:"audio"`
}

// SubtitleTrack is a subtitle track.
type SubtitleTrack struct {
	Track
}

// ChapterTrack is a chapter track.
type ChapterTrack struct {
	Track
}

// VideoSliceTrack is a re-encode slice's video track.
type VideoSliceTrack struct {
	VideoTrack
	Slice VideoSliceInfo `json:"slice"`
}
