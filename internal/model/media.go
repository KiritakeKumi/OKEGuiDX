package model

// VSFormat is the pixel format reported by `vspipe --info`.
type VSFormat struct {
	Name            string `json:"name"`
	ColorFamily     int    `json:"color_family"`
	ColorFamilyName string `json:"color_family_name"`
	BitsPerSample   int    `json:"bits_per_sample"`
	SubSamplingW    int    `json:"sub_sampling_w"`
	SubSamplingH    int    `json:"sub_sampling_h"`
	NumPlanes       int    `json:"num_planes"`
}

// VSVideoInfo is the parsed output of `vspipe --info`.
type VSVideoInfo struct {
	Format    VSFormat `json:"format"`
	FpsNum    int64    `json:"fps_num"`
	FpsDen    int64    `json:"fps_den"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	NumFrames int64    `json:"num_frames"`
	// VFR is true when the caller asked for a VFR job; the frame rate then
	// comes from the source rather than from the profile.
	VFR bool    `json:"vfr"`
	FPS float64 `json:"fps"`
}

// MediaFile is the in-memory description of the files a task produces: one main
// output plus, optionally, an external mka. Tracks are ordered video, audio,
// subtitle, chapter — the order the muxers expect.
type MediaFile struct {
	Video          *VideoTrack      `json:"video"`
	AudioTracks    []*AudioTrack    `json:"audio_tracks"`
	SubtitleTracks []*SubtitleTrack `json:"subtitle_tracks"`
	Chapter        *ChapterTrack    `json:"chapter"`
}

// NewMediaFile returns an empty MediaFile.
func NewMediaFile() *MediaFile {
	return &MediaFile{
		AudioTracks:    []*AudioTrack{},
		SubtitleTracks: []*SubtitleTrack{},
	}
}

// Tracks returns every track in muxing order.
func (m *MediaFile) Tracks() []*Track {
	out := make([]*Track, 0, 1+len(m.AudioTracks)+len(m.SubtitleTracks)+1)
	if m.Video != nil {
		out = append(out, &m.Video.Track)
	}
	for _, a := range m.AudioTracks {
		out = append(out, &a.Track)
	}
	for _, s := range m.SubtitleTracks {
		out = append(out, &s.Track)
	}
	if m.Chapter != nil {
		out = append(out, &m.Chapter.Track)
	}
	return out
}

// AddTrack inserts a track in the slot implied by its concrete type. Adding a
// second video or chapter track is an error, matching the legacy behaviour.
func (m *MediaFile) AddTrack(t *Track) error {
	switch t.TrackType {
	case TrackTypeVideo:
		if m.Video != nil {
			return ErrMultipleVideoTracks
		}
		m.Video = &VideoTrack{Track: *t, Video: NewVideoInfo()}
	case TrackTypeAudio:
		m.AudioTracks = append(m.AudioTracks, &AudioTrack{Track: *t})
	case TrackTypeSubtitle:
		m.SubtitleTracks = append(m.SubtitleTracks, &SubtitleTrack{Track: *t})
	case TrackTypeChapter:
		if m.Chapter != nil {
			return ErrMultipleChapterTracks
		}
		m.Chapter = &ChapterTrack{Track: *t}
	default:
		return ErrUnknownTrackType
	}
	return nil
}

// TotalFileSize sums the sizes of the video and audio tracks. A zero size is
// returned for tracks whose files do not exist yet; callers that need exact
// sizes must stat the files themselves.
func (m *MediaFile) TotalFileSize(sizeOf func(FileRef) int64) int64 {
	var total int64
	if m.Video != nil {
		total += sizeOf(m.Video.File)
	}
	for _, a := range m.AudioTracks {
		total += sizeOf(a.File)
	}
	return total
}
