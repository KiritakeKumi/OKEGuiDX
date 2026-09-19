package ffmpeg

import (
	"math"
	"strconv"
)

// The delay model.
//
// eac3to aligns every extracted track to video t=0. It reads each track's
// container timestamp, subtracts the video's, and materialises the difference in
// the output file: its log says so ("Applying (E-)AC3 delay of -167ms"), and a
// positive value is filled with silence ("Audio has a gap of 1001ms at playtime
// 0:00:00. The audio gap was filled with silence."). The muxer downstream
// therefore never needs a sync offset.
//
// ffmpeg does not do this. Its raw muxers drop the container timestamp base and
// write each stream's content from its own t=0, so a track that started late
// comes out early — the opposite of what the pipeline expects. The functions
// below compute the same number eac3to computed and turn it into the arguments
// that reproduce the alignment.
//
// Everything here was established against ffmpeg 5.x on real streams; the
// comments record what was measured, because the behaviour is not documented.

// delayReference returns the container time that extracted tracks are aligned
// to, i.e. video t=0.
//
// When the container has no video stream the earliest stream becomes the
// reference instead. That matters for an M2TS: every PID of one starts at the
// same 0x90000-based timestamp, and aligning against literal zero would turn
// that shared base offset into a bogus multi-second delay for every track.
//
// A negative start is kept as-is rather than clamped: ffmpeg rebases a container
// onto its earliest stream, so the reference has to be able to move left.
func (p *Probe) delayReference() float64 {
	if v, ok := p.videoStart(); ok {
		return v
	}
	first := true
	ref := 0.0
	for _, s := range p.Streams {
		if s.Disposition.AttachedPic == 1 {
			continue
		}
		if first || s.startTime() < ref {
			ref = s.startTime()
			first = false
		}
	}
	return ref
}

// delayFrom returns the stream's delay against the alignment reference, in
// whole milliseconds. Only audio carries a delay: video is never extracted and
// chapters come from ChapterService.
func (s *stream) delayFrom(ref float64) int {
	if s.CodecType != "audio" {
		return 0
	}
	return roundMS(s.startTime() - ref)
}

// Subtitle alignment.
//
// Audio and subtitles need different treatment, because ffmpeg's raw muxers
// handle the two differently. Measured on real streams:
//
//   - A raw audio muxer writes the packets' content from its own first frame and
//     discards the container offset entirely. A track that started one second
//     late comes out one second early, so the delay must be materialised — see
//     audioFilter and the copy path in extract.go.
//
//   - The raw subtitle muxers subtract the container's start offset, so a plain
//     stream copy already yields `cue_pts - format_start`. Movie time is
//     `cue_pts - video_start`, so the two agree exactly when the video is the
//     earliest stream — true for every MKV whose video starts at zero and every
//     M2TS, where all PIDs share the same timestamp base.
//
// The remaining case, a stream that starts before the video, needs a negative
// correction that no raw subtitle container can carry: the leading cues would
// have to be dropped, which a stream copy cannot do. Nothing is approximated
// there; SubtitleShiftMS records the figure for the log so the case is at least
// visible.

// subtitleShift returns how far a subtitle stream's timestamps sit from video
// t=0, in whole milliseconds. Negative means the stream begins before the video.
//
// It is informational. The extraction leaves subtitle timing to ffmpeg's own
// rebasing, which lands on movie time for every source shape that can be
// represented at all; a negative value is logged so the unrepresentable case is
// at least visible. See the note above.
func (s *stream) subtitleShift(ref float64) int {
	if s.CodecType != "subtitle" {
		return 0
	}
	return roundMS(s.startTime() - ref)
}

// subtitleWarning returns a log message for a subtitle stream whose cues start
// before the video, or "" when there is nothing to report.
func subtitleWarning(t *TrackInfo) string {
	if t.subtitleShiftMS >= 0 {
		return ""
	}
	return "字幕流早于视频开始，前导字幕无法由原始容器表达，未做位移"
}

// roundMS converts seconds to whole milliseconds, rounding half away from zero
// the way the legacy code rounded eac3to's own printed value.
func roundMS(seconds float64) int {
	return int(math.Round(seconds * 1000))
}

// audioFilter returns the -af expression that materialises a delay on a decoded
// audio track, or "" when no compensation is needed.
//
// Both directions are exact to the sample:
//
//   - a positive delay is silence prepended with adelay, which takes a sample
//     count when the rate is known (the "S" suffix) and milliseconds otherwise;
//   - a negative delay is the head removed with atrim, which counts decoded
//     samples and therefore never snaps to a compressed frame boundary the way
//     the copy path's -ss does.
//
// atrim is paired with asetpts so the output starts at t=0; without it the
// remaining samples keep their original timestamps and the raw muxers would
// carry a bogus offset.
func audioFilter(delayMS, sampleRate int) string {
	switch {
	case delayMS > 0:
		if sampleRate > 0 {
			return "adelay=" + strconv.Itoa(samplesOf(delayMS, sampleRate)) + "S:all=1"
		}
		return "adelay=" + strconv.Itoa(delayMS) + ":all=1"
	case delayMS < 0:
		if sampleRate > 0 {
			return "atrim=start_sample=" + strconv.Itoa(samplesOf(-delayMS, sampleRate)) +
				",asetpts=PTS-STARTPTS"
		}
		return "atrim=start=" + formatSeconds(float64(-delayMS)/1000) +
			",asetpts=PTS-STARTPTS"
	default:
		return ""
	}
}

// samplesOf converts milliseconds to samples at the given rate.
func samplesOf(ms, sampleRate int) int {
	return int(math.Round(float64(ms) * float64(sampleRate) / 1000))
}

// formatSeconds renders a duration for ffmpeg's option parser. A plain decimal
// is accepted everywhere a timestamp is, and avoids the rounding traps of the
// h:mm:ss form.
func formatSeconds(v float64) string {
	return strconv.FormatFloat(v, 'f', 6, 64)
}

// padSpec describes the silent lead-in the copy path needs for a positive
// delay.
//
// A raw stream copy cannot take a filter (-af and -c copy are mutually
// exclusive: "Filtergraph ... was defined for audio output stream 0:0 but codec
// copy was selected"), and a raw muxer has nowhere to store an offset, so the
// silence has to exist as real packets. It is generated with the same codec and
// the same parameters as the body and the two are joined with the concat
// demuxer.
//
// Matching the parameters is not optional: an AC3 pad generated at 192 kbit/s in
// front of a 448 kbit/s body made the concat demuxer produce 8.4 s of
// undecodable output instead of the expected 6.0 s.
type padSpec struct {
	// encoder is the ffmpeg -c:a value.
	encoder string
	// muxer is the ffmpeg -f value for both the pad and the joined result.
	muxer string
	// sampleRate, channels and channelLayout describe the body.
	sampleRate    int
	channels      int
	channelLayout string
	// bitRate is the body's bitrate in bit/s. Zero means the codec is
	// variable-rate and the encoder's own default is used.
	bitRate int
	// ms is the amount of silence to generate.
	ms int
	// strict adds -strict -2, which ffmpeg's dca encoder requires.
	strict bool
}

// padFor returns the pad description for a track, or ok=false when this codec
// has no silent form.
//
// Only the lossy raw codecs need one: FLAC, Opus, ASS and SRT are either
// variable-rate or not audio at all, and a positive delay on those is handled by
// the decoded path instead.
func padFor(t *TrackInfo, delayMS int) (padSpec, bool) {
	enc, muxer, ok := padCodec(t.Codec)
	if !ok {
		return padSpec{}, false
	}
	spec := padSpec{
		encoder:       enc,
		muxer:         muxer,
		sampleRate:    t.sampleRate,
		channels:      t.channels,
		channelLayout: t.channelLayout,
		bitRate:       t.bitRate,
		ms:            delayMS,
		strict:        t.Codec == CodecDTS,
	}
	// A lossy encoder that is not told its bitrate picks a default that will
	// not match the body, and a mismatched pad corrupts the join. Refusing is
	// the only safe answer; the caller then decodes instead.
	if spec.bitRate <= 0 {
		return spec, false
	}
	if spec.sampleRate <= 0 || spec.channels <= 0 {
		return spec, false
	}
	return spec, true
}

// padCodec maps a codec onto the encoder and muxer that produce a silent stream
// of the same shape.
func padCodec(c TrackCodec) (encoder, muxer string, ok bool) {
	switch c {
	case CodecAC3:
		return "ac3", "ac3", true
	case CodecEAC3:
		return "eac3", "eac3", true
	case CodecDTS:
		// ffmpeg's dca encoder is marked experimental, hence -strict -2.
		return "dca", "dts", true
	case CodecAAC:
		return "aac", "adts", true
	default:
		return "", "", false
	}
}

// padArgs is the ffmpeg command line that writes the silent lead-in, minus the
// output path and the progress options, which withOutput appends.
//
// The duration is expressed with -t, which ffmpeg rounds up to the encoder's
// next frame boundary: a 1000 ms request for AC3 produced 1.024 s. The overshoot
// is bounded by one frame (32 ms for AC3 and E-AC3, 10.7 ms for DTS, 21.3 ms for
// AAC) and is the same granularity eac3to's own frame surgery had, so a pad is
// never short and the track is never truncated.
func padArgs(spec padSpec) []string {
	layout := spec.channelLayout
	if layout == "" {
		layout = strconv.Itoa(spec.channels)
	}
	args := []string{
		"-y", "-nostdin", "-v", "warning",
		"-f", "lavfi",
		"-i", "anullsrc=r=" + strconv.Itoa(spec.sampleRate) + ":cl=" + layout,
		"-t", formatSeconds(float64(spec.ms) / 1000),
		"-c:a", spec.encoder,
	}
	if spec.strict {
		args = append(args, "-strict", "-2")
	}
	if spec.bitRate > 0 {
		args = append(args, "-b:a", strconv.Itoa(spec.bitRate))
	}
	return append(args,
		"-ar", strconv.Itoa(spec.sampleRate),
		"-ac", strconv.Itoa(spec.channels),
		"-f", spec.muxer,
	)
}

// concatArgs is the ffmpeg command line that joins the pad and the body, minus
// the output path and the progress options, which withOutput appends.
func concatArgs(spec padSpec, listFile string) []string {
	return []string{
		"-y", "-nostdin", "-v", "warning",
		"-f", "concat", "-safe", "0",
		"-i", listFile,
		"-c", "copy",
		"-f", spec.muxer,
	}
}

// outputFor returns the ffmpeg muxer (-f) and audio encoder (-c:a) that produce
// a track's output file.
//
// The lookup is keyed by the output extension, not by the source codec, because
// those differ for the lossless formats: a TrueHD, DTS-HD or PCM track becomes
// FLAC, so the encoder is chosen by what the file must be rather than by what it
// was. encoder is empty for a codec that is only ever stream-copied.
func outputFor(t TrackInfo) (muxer, encoder string, ok bool) {
	switch t.FileExtension() {
	case ".flac":
		return "flac", "flac", true
	case ".ac3":
		return "ac3", "ac3", true
	case ".eac3":
		return "eac3", "eac3", true
	case ".dts":
		return "dts", "dca", true
	case ".aac":
		return "adts", "aac", true
	case ".opus":
		return "opus", "libopus", true
	case ".sup":
		return "sup", "", true
	case ".ass":
		return "ass", "", true
	case ".srt":
		return "srt", "", true
	default:
		return "", "", false
	}
}

// isSubtitleCodec reports whether the codec is carried by the subtitle stream
// path, which is copied rather than encoded and whose delay is a timestamp shift
// rather than a content edit.
func isSubtitleCodec(c TrackCodec) bool {
	switch c {
	case CodecPGS, CodecASS, CodecSRT:
		return true
	default:
		return false
	}
}

// delayStatus renders the delay the way eac3to's log did, for the log line.
func delayStatus(t *TrackInfo) string {
	if t.delay == 0 {
		return ""
	}
	return "Applying delay of " + strconv.Itoa(t.delay) + "ms"
}
