package simple

import (
	"strconv"
	"strings"
)

// NameAppendVideo is the processor name used in logs and status events.
const NameAppendVideo = "append_video"

// AppendVideoOptions configures an AppendVideoMuxer run.
type AppendVideoOptions struct {
	Options
	// Slices are the part files, in playback order.
	Slices []string
	// TimecodeFile is the VFR timecode file; empty means CFR.
	TimecodeFile string
}

// AppendVideoArgs builds the mkvmerge argv that joins the parts.
//
// The legacy code emitted "--append-to" even when there was nothing to append
// to; the option then has no value and mkvmerge rejects the command line. That
// quirk is preserved rather than silently fixed.
func AppendVideoArgs(opts AppendVideoOptions) []string {
	args := baseArgs(opts.Options)
	if opts.TimecodeFile != "" {
		args = append(args, "--timestamps", "0:"+opts.TimecodeFile)
	}
	args = append(args,
		"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
		"--no-chapters", "--no-attachments", "--no-global-tags",
		"--default-track", "0:1", "--language", "0:und")
	for i, slice := range opts.Slices {
		if i > 0 {
			args = append(args, "+")
		}
		args = append(args, "(", slice, ")")
	}

	specs := make([]string, 0, len(opts.Slices))
	for i := 1; i < len(opts.Slices); i++ {
		specs = append(specs, strconv.Itoa(i)+":0:"+strconv.Itoa(i-1)+":0")
	}
	if len(specs) == 0 {
		args = append(args, "--append-to")
		return args
	}
	return append(args, "--append-to", strings.Join(specs, ","))
}

// AppendVideo joins the parts of a re-encode into one video.
type AppendVideo struct{ *runner }

// NewAppendVideo returns a processor for the append step.
func NewAppendVideo(opts AppendVideoOptions) *AppendVideo {
	return &AppendVideo{newRunner(NameAppendVideo, opts.Options, AppendVideoArgs(opts))}
}
