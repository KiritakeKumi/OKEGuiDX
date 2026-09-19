package eac3to

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

// TrackCodec mirrors the legacy EACDemuxer.TrackCodec enum. The zero value is
// CodecUnknown so an unrecognised description stays detectable, exactly as
// TrackCodec.Unknown was.
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
// comparable with the .NET version.
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

// outputType mirrors the legacy EacOutputTrackType table.
//
// The order is significant twice over: codec detection takes the first entry
// whose RawOutput is a prefix of the track description (so "DTS Master Audio"
// must precede "DTS", and "TrueHD/AC3" must precede "TrueHD"), and FileExtension
// is looked up by codec, taking the first entry with a matching codec.
type outputType struct {
	Codec TrackCodec
	// RawOutput is the literal prefix eac3to prints.
	RawOutput string
	// Extension is the output file extension without the dot.
	Extension string
	// Extract reports whether the legacy code asked eac3to to write this
	// track. Video, chapters and VobSub are listed but never extracted: the
	// video comes from the .vpy script and the chapters from ChapterService.
	Extract bool
	Type    model.TrackType
}

// outputTypes is s_eacOutputs, verbatim and in order.
var outputTypes = []outputType{
	{CodecRAWPCM, "RAW/PCM", "flac", true, model.TrackTypeAudio},
	{CodecDTSMA, "DTS Master Audio", "flac", true, model.TrackTypeAudio},
	{CodecTrueHDAC3, "TrueHD/AC3", "flac", true, model.TrackTypeAudio},
	{CodecTrueHDAC3, "TrueHD", "flac", true, model.TrackTypeAudio},
	{CodecAC3, "AC3", "ac3", true, model.TrackTypeAudio},
	{CodecFLAC, "FLAC", "flac", true, model.TrackTypeAudio},
	{CodecAAC, "AAC", "aac", true, model.TrackTypeAudio},
	{CodecDTS, "DTS", "dts", true, model.TrackTypeAudio},
	{CodecEAC3, "EAC3", "eac3", true, model.TrackTypeAudio},
	{CodecEAC3, "E-AC3", "eac3", true, model.TrackTypeAudio},
	{CodecOPUS, "OPUS", "opus", true, model.TrackTypeAudio},
	{CodecMPEG2, "MPEG2", "m2v", false, model.TrackTypeVideo},
	{CodecH264AVC, "h264/AVC", "264", false, model.TrackTypeVideo},
	{CodecH265HEVC, "h265/HEVC", "265", false, model.TrackTypeVideo},
	{CodecAV1, "AV1", "av1", false, model.TrackTypeVideo},
	{CodecPGS, "Subtitle (PGS)", "sup", true, model.TrackTypeSubtitle},
	{CodecASS, "Subtitle (ASS)", "ass", true, model.TrackTypeSubtitle},
	{CodecSRT, "Subtitle (SRT)", "srt", true, model.TrackTypeSubtitle},
	{CodecChapter, "Chapters", "txt", false, model.TrackTypeChapter},
	{CodecVobSub, "Subtitle (VobSub)", "sub", false, model.TrackTypeSubtitle},
}

// lookupOutput resolves a track description the way
// EacOutputToTrackCodec/EacOutputToTrackType did: first prefix match wins.
func lookupOutput(description string) (outputType, bool) {
	d := strings.TrimSpace(description)
	for _, ot := range outputTypes {
		if strings.HasPrefix(d, ot.RawOutput) {
			return ot, true
		}
	}
	return outputType{}, false
}

// lookupCodec resolves an entry by codec, the way FileExtension and the
// Extract flag were resolved.
func lookupCodec(c TrackCodec) (outputType, bool) {
	for _, ot := range outputTypes {
		if ot.Codec == c {
			return ot, true
		}
	}
	return outputType{}, false
}

// TrackInfo is one track eac3to reported, plus the measurements taken after it
// was extracted. Field set and meaning mirror EACDemuxer.TrackInfo.
type TrackInfo struct {
	Codec TrackCodec
	Index int
	// Information is the part of the listing line after the codec, e.g.
	// "English, 5.1 channels, 48khz".
	Information string
	// RawOutput is the listing line as parsed, after the PGS language fixup.
	RawOutput string
	// SourceFile is the input file the track was found in.
	SourceFile string
	// WorkingPathPrefix mirrors TaskProfile.WorkingPathPrefix; only its
	// directory is used.
	WorkingPathPrefix string
	Type              model.TrackType
	// DupOrEmpty marks a track detected as silent or as a duplicate.
	DupOrEmpty bool
	// Length is the container runtime in whole seconds, as parsed from the
	// eac3to header line. Zero means the header carried no runtime.
	Length int
	// FileSize is the extracted file size in bytes.
	FileSize int64
	// MeanVolume and MaxVolume are the RMS and peak levels in dB reported by
	// ffmpeg's astats filter. Both are zero when no volume checker is wired up.
	MeanVolume float64
	MaxVolume  float64
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

// extract reports whether eac3to is asked to write this track, the way the
// legacy code consulted EacOutputTrackType.Extract by codec.
func (t *TrackInfo) extract() bool {
	ot, ok := lookupCodec(t.Codec)
	return ok && ot.Extract
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
// empty. The legacy code divided by zero and crashed; see SPEC.md §12.
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
