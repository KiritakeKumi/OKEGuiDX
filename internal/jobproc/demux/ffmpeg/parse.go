package ffmpeg

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// ProbeArgs is the ffprobe command line used for the analysis pass.
//
// -show_streams and -show_chapters are asked for in one run because the chapter
// entry is what makes the track numbering line up with eac3to's: eac3to lists
// "1: Chapters, ..." first, and every other track's index is one higher than it
// would otherwise be.
//
// -v error keeps ffmpeg's banner and statistics off stdout; -of json is the
// machine-readable form the parser below reads.
func ProbeArgs(source string) []string {
	return []string{
		"-v", "error",
		"-show_streams",
		"-show_chapters",
		"-of", "json",
		source,
	}
}

// stream is the subset of ffprobe's per-stream JSON this package reads. Fields
// ffprobe may print as a string or a number are captured as json.RawMessage and
// coerced by the accessors below, because that changed between releases.
type stream struct {
	Index         int             `json:"index"`
	CodecName     string          `json:"codec_name"`
	CodecType     string          `json:"codec_type"`
	Profile       string          `json:"profile"`
	ID            string          `json:"id"`
	Channels      int             `json:"channels"`
	ChannelLayout string          `json:"channel_layout"`
	SampleRate    json.RawMessage `json:"sample_rate"`
	BitRate       json.RawMessage `json:"bit_rate"`
	StartTime     json.RawMessage `json:"start_time"`
	Duration      json.RawMessage `json:"duration"`
	Disposition   struct {
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
	Tags map[string]string `json:"tags"`
}

// chapter is one entry of ffprobe's chapter list. Only the count matters: the
// chapter track exists to occupy eac3to's track 1 and is never extracted, the
// chapters themselves come from ChapterService.
type chapter struct {
	ID    int    `json:"id"`
	Start string `json:"start_time"`
	End   string `json:"end_time"`
}

// probeFormat carries the container-level runtime and start offset.
type probeFormat struct {
	Duration   json.RawMessage `json:"duration"`
	StartTime  json.RawMessage `json:"start_time"`
	FormatName string          `json:"format_name"`
}

// probeJSON is the document ffprobe prints.
type probeJSON struct {
	Streams  []stream    `json:"streams"`
	Chapters []chapter   `json:"chapters"`
	Format   probeFormat `json:"format"`
}

// Probe is a parsed ffprobe report.
type Probe struct {
	Streams  []stream
	Chapters []chapter
	Format   probeFormat
}

// ParseProbe parses ffprobe's JSON output. An empty or unparseable document is
// an error: the caller then reports the exit code and the tool's own message,
// which is far more useful than a silent empty track list.
func ParseProbe(raw string) (*Probe, error) {
	// A byte-order mark is not part of ffprobe's output, but it survives a
	// round trip through a capture tool that writes UTF-8 with a BOM, and it
	// would otherwise fail the whole parse.
	trimmed := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if trimmed == "" {
		return nil, okerr.New(okerr.KindTool, "ffprobe输出为空", "ffprobe 没有输出可解析的 JSON")
	}
	var doc probeJSON
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return nil, okerr.Wrap(err, okerr.KindTool, "ffprobe输出无法解析",
			"ffprobe 的 JSON 解析失败: %v", err)
	}
	return &Probe{Streams: doc.Streams, Chapters: doc.Chapters, Format: doc.Format}, nil
}

// Length returns the container runtime in whole seconds, mirroring the value
// eac3to printed in its header and the legacy code reduced to
// hour*3600 + minute*60 + second.
func (p *Probe) Length() int {
	seconds, ok := secondsOf(p.Format.Duration)
	if !ok {
		return 0
	}
	return int(seconds)
}

// videoStart returns the start time of the first video stream in seconds. ok is
// false when the container has no video track.
func (p *Probe) videoStart() (float64, bool) {
	for _, s := range p.Streams {
		if s.CodecType != "video" || s.Disposition.AttachedPic == 1 {
			continue
		}
		return s.startTime(), true
	}
	return 0, false
}

// Tracks maps the probe onto TrackInfo values, in listing order.
//
// The numbering reproduces eac3to's: a chapter entry takes index 1 whenever the
// container carries chapters, and the streams follow in their own order. eac3to
// derives the chapter list of a Blu-ray from its MPLS playlist, which a raw
// .m2ts does not carry; for those sources no chapter entry is synthesised and
// every index is therefore one lower than eac3to's (see the package report).
//
// A codec the legacy table cannot name is rejected with eac3to's own wording
// rather than dropped, because dropping it would surface later as an
// inexplicable track-count mismatch.
func (p *Probe) Tracks(opts Options) ([]*TrackInfo, error) {
	length := p.Length()
	ref := p.delayReference()

	var tracks []*TrackInfo
	next := 1
	if len(p.Chapters) > 0 {
		tracks = append(tracks, &TrackInfo{
			Codec:             CodecChapter,
			Index:             next,
			Information:       fmt.Sprintf("%d chapters", len(p.Chapters)),
			RawOutput:         fmt.Sprintf("Chapters, %d chapters", len(p.Chapters)),
			SourceFile:        opts.SourceFile,
			WorkingPathPrefix: opts.WorkingPathPrefix,
			Type:              model.TrackTypeChapter,
			Length:            length,
		})
		next++
	}

	seenIDs := make(map[string]bool)
	for _, s := range p.Streams {
		if s.Disposition.AttachedPic == 1 {
			continue
		}
		// ffmpeg's MPEG-TS demuxer splits an HDMV TrueHD PID into two streams:
		// the TrueHD itself and a synthetic AC-3 view of the compatibility
		// core it carries (libavformat/mpegts.c, "HDMV TrueHD streams also
		// contain an AC3 coded version of the audio track"). Both report the
		// PID as their id. eac3to lists that PID as a single "TrueHD/AC3"
		// track, so the second stream is a duplicate of the first and must not
		// become a track of its own.
		if id := strings.TrimSpace(s.ID); id != "" && id != "N/A" {
			if seenIDs[id] {
				continue
			}
			seenIDs[id] = true
		}
		codec, ok := codecFromProbe(s.CodecName, s.Profile)
		if !ok {
			return nil, unknownCodecError(s)
		}
		ot, ok := lookupCodec(codec)
		if !ok {
			return nil, unknownCodecError(s)
		}
		tracks = append(tracks, &TrackInfo{
			Codec:             codec,
			Index:             next,
			Information:       describeStream(s),
			RawOutput:         rawStream(s),
			SourceFile:        opts.SourceFile,
			WorkingPathPrefix: opts.WorkingPathPrefix,
			Type:              ot.Type,
			Length:            length,
			stream:            s.Index,
			delay:             s.delayFrom(ref),
			subtitleShiftMS:   s.subtitleShift(ref),
			sampleRate:        s.sampleRateValue(),
			channels:          s.Channels,
			channelLayout:     s.ChannelLayout,
			bitRate:           s.bitRateValue(),
		})
		next++
	}
	return tracks, nil
}

// unknownCodecError mirrors the ArgumentException the legacy code threw when a
// listing line named a codec the table does not know.
func unknownCodecError(s stream) error {
	line := s.CodecName
	if s.Profile != "" && s.Profile != "unknown" {
		line += " (" + s.Profile + ")"
	}
	return okerr.New(okerr.KindConfig, "不明类型", "不明类型: %s", line).WithOutput(line)
}

// describeStream renders the probe's stream as eac3to's listing tail:
// "{language}, {channels} channels, {rate}". eac3to's exact wording cannot be
// reproduced from ffprobe alone (it names dialnorm, for one); nothing downstream
// reads this, it is for the log and the UI.
func describeStream(s stream) string {
	var parts []string
	if lang := languageOf(s.Tags); lang != "" {
		parts = append(parts, lang)
	}
	if s.Channels > 0 {
		parts = append(parts, channelDescription(s.Channels))
	}
	if rate, ok := intOf(s.SampleRate); ok && rate > 0 {
		parts = append(parts, strconv.Itoa(rate/1000)+"kHz")
	}
	return strings.Join(parts, ", ")
}

// rawStream is a compact one-line description of the stream for RawOutput.
func rawStream(s stream) string {
	name := s.CodecName
	if s.Profile != "" && s.Profile != "unknown" {
		name += " (" + s.Profile + ")"
	}
	if id := strings.TrimSpace(s.ID); id != "" && id != "N/A" {
		return fmt.Sprintf("%s, id %s", name, id)
	}
	return name
}

// languageOf picks the three-letter language eac3to printed. Matroska stores it
// in the "language" tag; a missing tag becomes empty rather than a guess, so the
// description never claims a language the container did not carry.
func languageOf(tags map[string]string) string {
	for _, key := range []string{"language", "LANGUAGE", "lang"} {
		if v := strings.TrimSpace(tags[key]); v != "" && v != "und" {
			return v
		}
	}
	return ""
}

// channelDescription names a channel count the way eac3to did.
func channelDescription(n int) string {
	switch n {
	case 1:
		return "1.0 channels"
	case 2:
		return "2.0 channels"
	case 3:
		return "3.0 channels"
	case 4:
		return "4.0 channels"
	case 5:
		return "5.0 channels"
	case 6:
		return "5.1 channels"
	case 7:
		return "6.1 channels"
	case 8:
		return "7.1 channels"
	default:
		return strconv.Itoa(n) + " channels"
	}
}

// secondsOf coerces ffprobe's string-or-number duration into seconds.
func secondsOf(raw json.RawMessage) (float64, bool) {
	v, ok := floatOf(raw)
	if !ok || v <= 0 {
		return 0, false
	}
	return v, true
}

// floatOf coerces a JSON string or number into a float.
func floatOf(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	s := strings.TrimSpace(string(raw))
	if s == "null" || s == `""` || s == "N/A" {
		return 0, false
	}
	s = strings.Trim(s, `"`)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// intOf coerces a JSON string or number into an int.
func intOf(raw json.RawMessage) (int, bool) {
	v, ok := floatOf(raw)
	if !ok {
		return 0, false
	}
	return int(v), true
}

// sampleRateValue returns the stream's sample rate, or 0 when absent.
func (s *stream) sampleRateValue() int {
	v, _ := intOf(s.SampleRate)
	return v
}

// bitRateValue returns the stream's bitrate in bits per second, or 0 when
// absent. A pad generated for the concat path must match it or the spliced
// stream decodes as garbage.
func (s *stream) bitRateValue() int {
	v, _ := intOf(s.BitRate)
	return v
}

// startTime returns the stream's container start time in seconds. ffprobe omits
// it for some formats; a missing value means zero.
func (s *stream) startTime() float64 {
	v, _ := floatOf(s.StartTime)
	return v
}
