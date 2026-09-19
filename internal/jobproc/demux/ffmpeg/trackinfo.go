package ffmpeg

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
)

// TrackCodec mirrors the legacy EACDemuxer.TrackCodec enum, so a track carries
// the same vocabulary whichever demuxer produced it. ffprobe names its codecs
// differently; codecTable translates.
//
// The zero value is CodecUnknown so an unrecognised codec stays detectable,
// exactly as TrackCodec.Unknown was.
type TrackCodec int

// Codecs, declared in the order of the legacy enum.
const (
	CodecUnknown TrackCodec = iota
	CodecMPEG2
	CodecH264AVC
	CodecH265HEVC
	CodecAV1
	CodecRAWPCM
	CodecFLAC
	CodecAAC
	CodecDTSMA
	CodecTrueHDAC3
	CodecAC3
	CodecDTS
	CodecEAC3
	CodecOPUS
	CodecPGS
	CodecChapter
	CodecVobSub
	CodecASS
	CodecSRT
)

// String implements fmt.Stringer with the legacy enum names, so log lines stay
// comparable with the .NET version and with internal/jobproc/demux/eac3to.
func (c TrackCodec) String() string {
	switch c {
	case CodecMPEG2:
		return "MPEG2"
	case CodecH264AVC:
		return "H264_AVC"
	case CodecH265HEVC:
		return "H265_HEVC"
	case CodecAV1:
		return "AV1"
	case CodecRAWPCM:
		return "RAW_PCM"
	case CodecFLAC:
		return "FLAC"
	case CodecAAC:
		return "AAC"
	case CodecDTSMA:
		return "DTSMA"
	case CodecTrueHDAC3:
		return "TRUEHD_AC3"
	case CodecAC3:
		return "AC3"
	case CodecDTS:
		return "DTS"
	case CodecEAC3:
		return "EAC3"
	case CodecOPUS:
		return "OPUS"
	case CodecPGS:
		return "PGS"
	case CodecChapter:
		return "Chapter"
	case CodecVobSub:
		return "VobSub"
	case CodecASS:
		return "ASS"
	case CodecSRT:
		return "SRT"
	default:
		return "Unknown"
	}
}

// outputType mirrors the legacy EacOutputTrackType table. The extension and the
// Extract flag are keyed by codec, and FileExtension takes the first entry with
// a matching codec, so the order matters for the two codecs that appear twice.
type outputType struct {
	Codec TrackCodec
	// Extension is the output file extension without the dot.
	Extension string
	// Extract reports whether the legacy code asked the demuxer to write this
	// track. Video, chapters and VobSub are listed but never extracted: the
	// video comes from the .vpy script and the chapters from ChapterService.
	Extract bool
	Type    model.TrackType
}

// outputTypes reproduces s_eacOutputs by codec, with the eac3to description
// strings dropped: those exist to parse eac3to's listing, and ffprobe needs no
// such translation.
var outputTypes = []outputType{
	{CodecRAWPCM, "flac", true, model.TrackTypeAudio},
	{CodecDTSMA, "flac", true, model.TrackTypeAudio},
	{CodecTrueHDAC3, "flac", true, model.TrackTypeAudio},
	{CodecAC3, "ac3", true, model.TrackTypeAudio},
	{CodecFLAC, "flac", true, model.TrackTypeAudio},
	{CodecAAC, "aac", true, model.TrackTypeAudio},
	{CodecDTS, "dts", true, model.TrackTypeAudio},
	{CodecEAC3, "eac3", true, model.TrackTypeAudio},
	{CodecOPUS, "opus", true, model.TrackTypeAudio},
	{CodecMPEG2, "m2v", false, model.TrackTypeVideo},
	{CodecH264AVC, "264", false, model.TrackTypeVideo},
	{CodecH265HEVC, "265", false, model.TrackTypeVideo},
	{CodecAV1, "av1", false, model.TrackTypeVideo},
	{CodecPGS, "sup", true, model.TrackTypeSubtitle},
	{CodecASS, "ass", true, model.TrackTypeSubtitle},
	{CodecSRT, "srt", true, model.TrackTypeSubtitle},
	{CodecChapter, "txt", false, model.TrackTypeChapter},
	{CodecVobSub, "sub", false, model.TrackTypeSubtitle},
}

// nativeExtensions maps a codec to the container it is normally stored in, i.e.
// the extension a bit-exact stream copy produces. Extraction compares it with
// the requested output extension: when the two agree the track is copied, when
// they differ it is decoded and re-encoded, which is exactly the split eac3to
// makes (AC3 stays AC3, TrueHD and DTS-HD become FLAC, PCM becomes FLAC).
//
// A codec missing from this table has no copy form and is always decoded. Video
// codecs are absent even though ffmpeg can copy them: the legacy code never
// extracted video, the .vpy script is the video source.
var nativeExtensions = map[TrackCodec]string{
	CodecAC3:  "ac3",
	CodecEAC3: "eac3",
	CodecDTS:  "dts",
	CodecFLAC: "flac",
	CodecAAC:  "aac",
	CodecOPUS: "opus",
	CodecPGS:  "sup",
	CodecASS:  "ass",
	CodecSRT:  "srt",
}

// lookupCodec resolves an entry by codec, the way FileExtension and the Extract
// flag were resolved.
func lookupCodec(c TrackCodec) (outputType, bool) {
	for _, ot := range outputTypes {
		if ot.Codec == c {
			return ot, true
		}
	}
	return outputType{}, false
}

// codecName maps an ffprobe codec_name onto the legacy vocabulary. A codec that
// eac3to could not name is not in the table, which is what turns it into the
// same "不明类型" rejection eac3to produced.
var codecName = map[string]TrackCodec{
	"truehd": CodecTrueHDAC3,
	"mlp":    CodecTrueHDAC3,

	"ac3":  CodecAC3,
	"eac3": CodecEAC3,
	"flac": CodecFLAC,
	"aac":  CodecAAC,
	"opus": CodecOPUS,
	// "dts" is resolved by profile: see codecFromProbe.

	"h264":       CodecH264AVC,
	"hevc":       CodecH265HEVC,
	"mpeg2video": CodecMPEG2,
	"av1":        CodecAV1,

	"hdmv_pgs_subtitle": CodecPGS,
	"ass":               CodecASS,
	"ssa":               CodecASS,
	"subrip":            CodecSRT,
	"dvd_subtitle":      CodecVobSub,
	"vobsub":            CodecVobSub,
}

// codecFromProbe resolves a stream's codec, upgrading DTS to DTS-HD Master
// Audio when the profile says so: eac3to reports the two as different codecs
// ("DTS" keeps its .dts extension, "DTS Master Audio" becomes FLAC), and
// ffprobe distinguishes them only through the profile field.
func codecFromProbe(name, profile string) (TrackCodec, bool) {
	switch name {
	case "dts":
		if isDTSExtension(profile) {
			return CodecDTSMA, true
		}
		return CodecDTS, true
	case "pcm_bluray", "pcm_dvd", "pcm_s16be", "pcm_s24be", "pcm_s32be",
		"pcm_s16le", "pcm_s24le", "pcm_s32le", "pcm_f32le", "pcm_f64le",
		"pcm_u8", "pcm_s8":
		return CodecRAWPCM, true
	}
	c, ok := codecName[name]
	return c, ok
}

// isDTSExtension reports whether an ffprobe DTS profile is the lossless or
// high-resolution extension, which eac3to lists as "DTS Master Audio".
//
// The decoder-side profiles are "DTS-HD MA" and "DTS-HD HRA"; the bare core is
// "DTS" or "DTS-ES". Anything else stays a plain DTS track, which is the safe
// direction: an unrecognised extension is copied as DTS rather than silently
// turned into FLAC.
func isDTSExtension(profile string) bool {
	p := strings.ToUpper(strings.TrimSpace(profile))
	return strings.Contains(p, "MA") || strings.Contains(p, "HRA")
}

// TrackInfo is one track the probe reported, plus the measurements taken after
// it was extracted. The field set mirrors
// internal/jobproc/demux/eac3to.TrackInfo so the pipeline can read either.
type TrackInfo struct {
	Codec TrackCodec
	// Index is the track's position in the listing, one-based and counting the
	// chapter entry. It reproduces eac3to's track numbering, which is what
	// OutFileName is built from.
	Index int
	// Information is a human description assembled from the probe, shaped like
	// eac3to's listing tail: "English, 5.1 channels, 48kHz".
	Information string
	// RawOutput is the probe's one-line description of the stream.
	RawOutput string
	// SourceFile is the input file the track was found in.
	SourceFile string
	// WorkingPathPrefix mirrors TaskProfile.WorkingPathPrefix; only its
	// directory is used.
	WorkingPathPrefix string
	Type              model.TrackType
	// DupOrEmpty marks a track detected as silent or as a duplicate.
	DupOrEmpty bool
	// Length is the container runtime in whole seconds. Zero means the probe
	// reported none.
	Length int
	// FileSize is the extracted file size in bytes.
	FileSize int64
	// MeanVolume and MaxVolume are the RMS and peak levels in dB reported by
	// ffmpeg's astats filter. Both are zero when no volume checker is wired up.
	MeanVolume float64
	MaxVolume  float64

	// stream is the ffprobe stream index, used for -map. It is not part of the
	// eac3to-compatible surface: eac3to has no such concept because its own
	// listing indices are what it extracts by.
	stream int
	// delay is the track's container delay relative to video t=0, in
	// milliseconds. Zero means the track is already aligned.
	delay int
	// subtitleShiftMS is the timestamp shift a subtitle stream would need, in
	// milliseconds. It is recorded for the log: a negative value is the one
	// shape the raw subtitle containers cannot express. See delay.go.
	subtitleShiftMS int
	// sampleRate is the stream's sample rate in Hz, used to turn a delay into
	// a sample count. Zero when the probe reported none.
	sampleRate int
	// channels, channelLayout and bitRate describe the stream so that a silent
	// pad can be generated to match it exactly. They are only consulted on the
	// stream-copy path.
	channels      int
	channelLayout string
	bitRate       int
}

// String implements fmt.Stringer for diagnostics and test failures.
func (t *TrackInfo) String() string {
	return fmt.Sprintf("%d:%s (%s)", t.Index, t.Codec, t.Type)
}

// OutFileName reproduces TrackInfo.OutFileName:
//
//	{dir(WorkingPathPrefix)}/{stem(SourceFile)}_{Index}{FileExtension}
//
// Note that the working path *prefix* is only used for its directory; the file
// name comes from the source file, not from the prefix.
func (t *TrackInfo) OutFileName() string {
	dir := filepath.Dir(t.WorkingPathPrefix)
	return filepath.Join(dir, stem(t.SourceFile)+"_"+strconv.Itoa(t.Index)+t.FileExtension())
}

// FileExtension reproduces TrackInfo.FileExtension, including the leading dot.
func (t *TrackInfo) FileExtension() string {
	ot, ok := lookupCodec(t.Codec)
	if !ok {
		return ""
	}
	return "." + ot.Extension
}

// extract reports whether the track is written, the way the legacy code
// consulted EacOutputTrackType.Extract by codec.
func (t *TrackInfo) extract() bool {
	ot, ok := lookupCodec(t.Codec)
	return ok && ot.Extract
}

// copyable reports whether the track's bytes can be passed through unchanged,
// i.e. whether the requested output container is the one the codec already
// lives in. A codec with no native container (TrueHD, DTS-HD, PCM) is always
// decoded.
func (t *TrackInfo) copyable() bool {
	native, ok := nativeExtensions[t.Codec]
	if !ok {
		return false
	}
	return t.FileExtension() == "."+native
}

// emptySubtitleBytesPerSecond is the legacy threshold expression
// 3 * 1024 * 1024 / 3600 evaluated in integer arithmetic, which truncates to
// 873 bytes per second of runtime.
const emptySubtitleBytesPerSecond = 3 * 1024 * 1024 / 3600

// IsEmpty reproduces TrackInfo.IsEmpty with the original thresholds:
//
//   - audio: MeanVolume < -70 dB and MaxVolume < -30 dB
//   - subtitle: FileSize/Length < 3*1024*1024/3600 bytes per second
//   - everything else: FileSize < 64 bytes
//
// A subtitle track whose runtime is unknown (Length == 0) is reported as not
// empty, matching the eac3to demuxer.
func (t *TrackInfo) IsEmpty() bool {
	switch t.Type {
	case model.TrackTypeAudio:
		return t.MeanVolume < -70 && t.MaxVolume < -30
	case model.TrackTypeSubtitle:
		if t.Length <= 0 {
			return false
		}
		return t.FileSize/int64(t.Length) < emptySubtitleBytesPerSecond
	default:
		return t.FileSize < 64
	}
}

// IsDuplicate reproduces TrackInfo.IsDuplicate:
//
//   - tracks of different types are never duplicates
//   - audio: |ΔMeanVolume| < 0.01 dB and |ΔMaxVolume| < 0.01 dB
//   - everything else: equal file size
func (t *TrackInfo) IsDuplicate(other *TrackInfo) bool {
	if t.Type != other.Type {
		return false
	}
	switch t.Type {
	case model.TrackTypeAudio:
		return math.Abs(t.MeanVolume-other.MeanVolume) < 0.01 &&
			math.Abs(t.MaxVolume-other.MaxVolume) < 0.01
	default:
		return t.FileSize == other.FileSize
	}
}

// MarkSkipping reproduces TrackInfo.MarkSkipping: back the file up as
// ".bak{ext}" and, when that fails, delete it. The duplicate/empty flag is set
// either way.
func (t *TrackInfo) MarkSkipping() {
	out := t.OutFileName()
	bak := changeExt(out, ".bak") + t.FileExtension()
	if err := moveNoOverwrite(out, bak); err != nil {
		log.Warn("无法备份文件，直接删除。如果是重启的任务，这很正常。", "file", out, "err", err)
		if rmErr := os.Remove(out); rmErr != nil && !os.IsNotExist(rmErr) {
			log.Warn("无法删除文件", "file", out, "err", rmErr)
		}
	}
	t.DupOrEmpty = true
}

// moveNoOverwrite mirrors .NET File.Move, which fails when the destination
// already exists. os.Rename would silently replace it, which would turn a
// restarted task into a different outcome than the legacy code produced.
func moveNoOverwrite(from, to string) error {
	if _, err := os.Stat(to); err == nil {
		return fmt.Errorf("目标已存在: %s", to)
	}
	return os.Rename(from, to)
}

// stem returns the file name without directory or extension, mirroring
// Path.GetFileNameWithoutExtension.
func stem(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// changeExt mirrors Path.ChangeExtension. The extension must include the dot.
func changeExt(path, ext string) string {
	e := filepath.Ext(path)
	if e == "" {
		return path + ext
	}
	return strings.TrimSuffix(path, e) + ext
}
