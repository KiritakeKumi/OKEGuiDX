package simple

import (
	"strconv"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// NameSingleVideo is the processor name used in logs and status events.
const NameSingleVideo = "single_video"

// SingleVideoOptions configures a SingleVideoMuxer run.
type SingleVideoOptions struct {
	Options
	// Input is the file the video track is taken from: an encoded part, or the
	// previous release for a part that is muxed through unchanged.
	Input string
	// Partial mirrors MuxJob.IsPartialMux. When false the whole input is kept
	// and FrameRange is ignored.
	Partial bool
	// FrameRange is the part's half-open frame range; End == -1 means the end
	// of the video. It is only used when Partial is set.
	FrameRange model.SliceInfo
}

// SingleVideoArgs builds the mkvmerge argv for a single-part mux.
//
// Both ends of the range are incremented: the profile's end frame is exclusive,
// and mkvmerge stops before the first key frame at or after the end frame, so
// end+1 guarantees that the last wanted frame is included (SingleVideoMuxer.cs).
func SingleVideoArgs(opts SingleVideoOptions) []string {
	args := baseArgs(opts.Options)
	args = append(args,
		"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
		"--no-chapters", "--no-attachments", "--no-global-tags",
		"--default-track", "0:1", "--language", "0:und",
		"(", opts.Input, ")")
	if opts.Partial {
		args = append(args, "--split", "parts-frames:"+
			strconv.FormatInt(opts.FrameRange.Begin+1, 10)+"-"+
			strconv.FormatInt(opts.FrameRange.End+1, 10))
	}
	return args
}

// SingleVideo muxes one encoded part.
type SingleVideo struct{ *runner }

// NewSingleVideo returns a processor for one part.
func NewSingleVideo(opts SingleVideoOptions) *SingleVideo {
	return &SingleVideo{newRunner(NameSingleVideo, opts.Options, SingleVideoArgs(opts))}
}
