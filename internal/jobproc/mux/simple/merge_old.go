package simple

// NameMergeOld is the processor name used in logs and status events.
const NameMergeOld = "merge_old"

// MergeOldOptions configures a MergeOldRemuxer run.
type MergeOldOptions struct {
	Options
	// Input is the newly muxed video, i.e. the appended parts.
	Input string
	// OldFile is the previous release. Its video is dropped and its remaining
	// tracks are merged behind Input.
	OldFile string
	// TimecodeFile is the VFR timecode file; empty means CFR.
	TimecodeFile string
}

// MergeOldArgs builds the mkvmerge argv for the final merge.
//
// The old release is added with --no-video, so only its audio, subtitle and
// chapter tracks survive, and --track-order 1:0 puts the new video first.
func MergeOldArgs(opts MergeOldOptions) []string {
	args := baseArgs(opts.Options)
	args = append(args, "--no-video", "(", opts.OldFile, ")")
	if opts.TimecodeFile != "" {
		args = append(args, "--timestamps", "0:"+opts.TimecodeFile)
	}
	args = append(args,
		"--default-track", "0:1", "--language", "0:und",
		"(", opts.Input, ")",
		"--track-order", "1:0")
	return args
}

// MergeOld merges the new video with the old release's remaining tracks.
type MergeOld struct{ *runner }

// NewMergeOld returns a processor for the final merge.
func NewMergeOld(opts MergeOldOptions) *MergeOld {
	return &MergeOld{newRunner(NameMergeOld, opts.Options, MergeOldArgs(opts))}
}
