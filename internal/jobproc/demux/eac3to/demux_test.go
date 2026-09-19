package eac3to

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// --- track enumeration -----------------------------------------------------

// wantTrack is the expectation for one listing line. Length is the runtime the
// detector had seen by the time the line arrived.
type wantTrack struct {
	index int
	codec TrackCodec
	typ   model.TrackType
	ext   string
	info  string
}

// TestParseListings drives the detector over the fixtures in testdata/ and
// asserts the full track enumeration, including the runtime each track was
// stamped with.
func TestParseListings(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture string
		length  int
		// trackLength is what each track records. The reference assigns the
		// length seen so far when the track line is parsed, so a later line
		// that looks like a timestamp can raise the container length above what
		// the tracks captured. Zero means "same as length".
		trackLength int
		tracks      []wantTrack
	}{
		{
			fixture: "m2ts_truehd_dtsma_ac3_pgs.log",
			length:  1*3600 + 38*60 + 47,
			tracks: []wantTrack{
				{1, CodecChapter, model.TrackTypeChapter, ".txt", "24 chapters"},
				{2, CodecH264AVC, model.TrackTypeVideo, ".264", "1080p24 /1.001 (16:9)"},
				{3, CodecTrueHDAC3, model.TrackTypeAudio, ".flac", "English, 5.1 channels, 48kHz, dialnorm: -27dB"},
				{4, CodecDTSMA, model.TrackTypeAudio, ".flac", "Japanese, 5.1 channels, 24 bits, 48kHz"},
				{5, CodecAC3, model.TrackTypeAudio, ".ac3", "English, 2.0 channels, 192kbps, 48kHz, dialnorm: -27dB"},
				{6, CodecPGS, model.TrackTypeSubtitle, ".sup", "English"},
				{7, CodecPGS, model.TrackTypeSubtitle, ".sup", "Japanese"},
			},
		},
		{
			// The header runtime is "1:02:05" and the PGS line carries no
			// language, which must become ", Japanese".
			fixture: "m2ts_pcm_dtsma_eac3_pgs_nolang.log",
			length:  1*3600 + 2*60 + 5,
			tracks: []wantTrack{
				{1, CodecChapter, model.TrackTypeChapter, ".txt", "12 chapters"},
				{2, CodecH265HEVC, model.TrackTypeVideo, ".265", "2160p24 /1.001 (16:9)"},
				{3, CodecRAWPCM, model.TrackTypeAudio, ".flac", "Japanese, 2.0 channels, 24 bits, 48kHz"},
				{4, CodecDTSMA, model.TrackTypeAudio, ".flac", "Japanese, 5.1 channels, 24 bits, 48kHz"},
				{5, CodecEAC3, model.TrackTypeAudio, ".eac3", "English, 5.1 channels, 768kbps, 48kHz, dialnorm: -27dB"},
				{6, CodecPGS, model.TrackTypeSubtitle, ".sup", "Japanese"},
			},
		},
		{
			fixture: "m2ts_dts_ac3_truehd_aac_mpeg2.log",
			length:  1*3600 + 12*60 + 33,
			tracks: []wantTrack{
				{1, CodecChapter, model.TrackTypeChapter, ".txt", "1 chapter"},
				{2, CodecMPEG2, model.TrackTypeVideo, ".m2v", "1080i60 /1.001 (16:9)"},
				{3, CodecDTS, model.TrackTypeAudio, ".dts", "Japanese, 5.1 channels, 24 bits, 1509kbps, 48kHz"},
				{4, CodecAC3, model.TrackTypeAudio, ".ac3", "Japanese, 2.0 channels, 448kbps, 48kHz, dialnorm: -27dB"},
				// Bare "TrueHD" (no "/AC3") must map to TRUEHD_AC3 as well.
				{5, CodecTrueHDAC3, model.TrackTypeAudio, ".flac", "Japanese, 5.1 channels, 48kHz, dialnorm: -27dB"},
				{6, CodecAAC, model.TrackTypeAudio, ".aac", "Japanese, 2.0 channels, 192kbps, 48kHz"},
			},
		},
		{
			fixture: "mkv_flac_opus_ac3_vobsub.log",
			length:  1*3600 + 31*60 + 19,
			tracks: []wantTrack{
				{1, CodecChapter, model.TrackTypeChapter, ".txt", "8 chapters"},
				{2, CodecH264AVC, model.TrackTypeVideo, ".264", "1080p24 /1.001 (16:9)"},
				{3, CodecFLAC, model.TrackTypeAudio, ".flac", "Japanese, 2.0 channels, 24 bits, 48kHz"},
				{4, CodecOPUS, model.TrackTypeAudio, ".opus", "Japanese, 2.0 channels, 48kHz"},
				{5, CodecAC3, model.TrackTypeAudio, ".ac3", "English, 5.1 channels, 640kbps, 48kHz"},
				{6, CodecPGS, model.TrackTypeSubtitle, ".sup", "Japanese"},
				// VobSub is listed but never extracted.
				{7, CodecVobSub, model.TrackTypeSubtitle, ".sub", "English"},
			},
		},
		{
			// The reference scans every line with an unanchored \d*:\d*:\d*
			// and keeps the last match. The header sets 292, then the gap notice
			// at "playtime 0:00:00." resets it to zero, which is the value
			// Length() reports. The tracks were parsed in between and keep 292.
			fixture:     "evo_truehd_eac3_delay.log",
			length:      0,
			trackLength: 4*60 + 52,
			tracks: []wantTrack{
				{1, CodecH264AVC, model.TrackTypeVideo, ".264", "1080p24 /1.001 (16:9)"},
				{2, CodecTrueHDAC3, model.TrackTypeAudio, ".flac", "English, 5.1 channels, 48kHz, dialnorm: -27dB"},
				{3, CodecEAC3, model.TrackTypeAudio, ".eac3", "English, 2.0 channels, 192kbps, 48kHz"},
			},
		},
		{
			// The reference scans every line for an unanchored \d*:\d*:\d* and
			// keeps the LAST match, so the overlap notice's "1:14:09" playtime
			// raises the container length above the header's 0:22:04. The
			// tracks were parsed earlier, so they keep the header's value.
			fixture:     "m2ts_dtsma_overlap.log",
			length:      1*3600 + 14*60 + 9,
			trackLength: 22*60 + 4,
			tracks: []wantTrack{
				{1, CodecChapter, model.TrackTypeChapter, ".txt", "6 chapters with names"},
				{2, CodecH264AVC, model.TrackTypeVideo, ".264", "1080p24 /1.001 (16:9)"},
				{3, CodecDTSMA, model.TrackTypeAudio, ".flac", "Japanese, 5.1 channels, 24 bits, 48kHz"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			d := NewDetector(`D:\src\00001.m2ts`, `D:\work\00001`)
			if err := d.FeedAll(readFixture(t, tc.fixture)); err != nil {
				t.Fatalf("FeedAll() error = %v", err)
			}
			if got := d.Length(); got != tc.length {
				t.Errorf("Length() = %d, want %d", got, tc.length)
			}
			wantLength := tc.trackLength
			if wantLength == 0 {
				wantLength = tc.length
			}
			tracks := d.Tracks()
			if len(tracks) != len(tc.tracks) {
				t.Fatalf("parsed %d tracks, want %d: %v", len(tracks), len(tc.tracks), tracks)
			}
			for i, want := range tc.tracks {
				got := tracks[i]
				if got.Index != want.index {
					t.Errorf("track[%d].Index = %d, want %d", i, got.Index, want.index)
				}
				if got.Codec != want.codec {
					t.Errorf("track[%d].Codec = %s, want %s", i, got.Codec, want.codec)
				}
				if got.Type != want.typ {
					t.Errorf("track[%d].Type = %s, want %s", i, got.Type, want.typ)
				}
				if got.FileExtension() != want.ext {
					t.Errorf("track[%d].FileExtension() = %q, want %q", i, got.FileExtension(), want.ext)
				}
				if got.Information != want.info {
					t.Errorf("track[%d].Information = %q, want %q", i, got.Information, want.info)
				}
				if got.Length != wantLength {
					t.Errorf("track[%d].Length = %d, want %d", i, got.Length, wantLength)
				}
			}
		})
	}
}

// TestCodecPrefixOrdering pins the two prefix collisions that the table order
// exists to resolve.
func TestCodecPrefixOrdering(t *testing.T) {
	t.Parallel()
	cases := []struct {
		description string
		wantCodec   TrackCodec
		wantType    model.TrackType
		wantExt     string
	}{
		{"DTS Master Audio", CodecDTSMA, model.TrackTypeAudio, ".flac"},
		{"DTS-HD Master Audio", CodecDTS, model.TrackTypeAudio, ".dts"}, // not a table prefix
		{"DTS", CodecDTS, model.TrackTypeAudio, ".dts"},
		{"DTS-ES", CodecDTS, model.TrackTypeAudio, ".dts"},
		{"TrueHD/AC3", CodecTrueHDAC3, model.TrackTypeAudio, ".flac"},
		{"TrueHD", CodecTrueHDAC3, model.TrackTypeAudio, ".flac"},
		{"EAC3", CodecEAC3, model.TrackTypeAudio, ".eac3"},
		{"E-AC3", CodecEAC3, model.TrackTypeAudio, ".eac3"},
		{"AC3", CodecAC3, model.TrackTypeAudio, ".ac3"},
		{"AC3 Surround", CodecAC3, model.TrackTypeAudio, ".ac3"},
		{"Subtitle (PGS)", CodecPGS, model.TrackTypeSubtitle, ".sup"},
		{"Subtitle (ASS)", CodecASS, model.TrackTypeSubtitle, ".ass"},
		{"Subtitle (SRT)", CodecSRT, model.TrackTypeSubtitle, ".srt"},
		{"Subtitle (VobSub)", CodecVobSub, model.TrackTypeSubtitle, ".sub"},
		{"Chapters", CodecChapter, model.TrackTypeChapter, ".txt"},
		{"h264/AVC", CodecH264AVC, model.TrackTypeVideo, ".264"},
		{"h265/HEVC", CodecH265HEVC, model.TrackTypeVideo, ".265"},
		{"MPEG2", CodecMPEG2, model.TrackTypeVideo, ".m2v"},
		{"AV1", CodecAV1, model.TrackTypeVideo, ".av1"},
		{"RAW/PCM", CodecRAWPCM, model.TrackTypeAudio, ".flac"},
		{"FLAC", CodecFLAC, model.TrackTypeAudio, ".flac"},
		{"AAC", CodecAAC, model.TrackTypeAudio, ".aac"},
		{"OPUS", CodecOPUS, model.TrackTypeAudio, ".opus"},
		{"VC-1", CodecUnknown, model.TrackTypeDefault, ""},
		{"MPEG2 Surround", CodecMPEG2, model.TrackTypeVideo, ".m2v"},
	}
	for _, tc := range cases {
		t.Run(tc.description, func(t *testing.T) {
			t.Parallel()
			ot, ok := lookupOutput(tc.description)
			if tc.wantCodec == CodecUnknown {
				if ok {
					t.Fatalf("lookupOutput(%q) matched %v, want no match", tc.description, ot)
				}
				return
			}
			if !ok {
				t.Fatalf("lookupOutput(%q) found nothing", tc.description)
			}
			if ot.Codec != tc.wantCodec {
				t.Errorf("codec = %s, want %s", ot.Codec, tc.wantCodec)
			}
			if ot.Type != tc.wantType {
				t.Errorf("type = %s, want %s", ot.Type, tc.wantType)
			}
			if "."+ot.Extension != tc.wantExt {
				t.Errorf("extension = .%s, want %s", ot.Extension, tc.wantExt)
			}
		})
	}
}

// TestDetectorRejectsUnknownCodec mirrors the ArgumentException the legacy code
// threw for a listing line naming a codec the table does not know.
func TestDetectorRejectsUnknownCodec(t *testing.T) {
	t.Parallel()
	d := NewDetector("a.m2ts", "w")
	err := d.FeedAll("M2TS, 1 video track, 0:10:00\n2: VC-1, 1080p24 /1.001 (16:9)\n")
	if err == nil {
		t.Fatal("FeedAll() = nil, want an error for the VC-1 track")
	}
	e := okerr.AsError(err)
	if e.Summary != "不明类型" {
		t.Errorf("summary = %q, want %q", e.Summary, "不明类型")
	}
	if !strings.Contains(e.Detail, "VC-1") {
		t.Errorf("detail = %q, want it to name the offending line", e.Detail)
	}
}

// TestDetectorSkipsLinesWithoutInformation pins the `(\d*?): (.*?), (.*?)`
// requirement: a line with no comma is not a track.
func TestDetectorSkipsLinesWithoutInformation(t *testing.T) {
	t.Parallel()
	d := NewDetector("a.m2ts", "w")
	lines := []string{
		"M2TS, 1 video track, 1 audio track, 1:00:00",
		"1: Chapters, 4 chapters",   // has a comma -> track
		"2: h264/AVC",               // no comma, no PGS -> skipped
		"3: Subtitle (PGS)",         // no comma, PGS -> fixed up to ", Japanese"
		"Analyzing 1, 2, 3...",      // no leading index
		"[a03] Extracting audio...", // no leading index
		"",
		"   ",
	}
	if err := d.FeedAll(strings.Join(lines, "\n")); err != nil {
		t.Fatalf("FeedAll() error = %v", err)
	}
	tracks := d.Tracks()
	if len(tracks) != 2 {
		t.Fatalf("parsed %d tracks, want 2: %v", len(tracks), tracks)
	}
	if tracks[0].Index != 1 || tracks[0].Codec != CodecChapter {
		t.Errorf("track[0] = %v, want 1:Chapter", tracks[0])
	}
	if tracks[1].Index != 3 || tracks[1].Codec != CodecPGS {
		t.Errorf("track[1] = %v, want 3:PGS", tracks[1])
	}
	if tracks[1].Information != "Japanese" {
		t.Errorf("Information = %q, want %q", tracks[1].Information, "Japanese")
	}
	if tracks[1].RawOutput != "3: Subtitle (PGS), Japanese" {
		t.Errorf("RawOutput = %q, want the fixed-up line", tracks[1].RawOutput)
	}
}

// TestDetectorRuntimeTracking pins the unanchored h:mm:ss probe, which is how
// eac3to header lines of every container shape were handled.
func TestDetectorRuntimeTracking(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		lines []string
		want  int
	}{
		{"m2ts header", []string{"M2TS, 1 video track, 1 audio track, 2:17:57, 24p /1.001"}, 2*3600 + 17*60 + 57},
		{"evo header", []string{"EVO, 1 video track, 2 audio tracks, 0:04:52"}, 4*60 + 52},
		{"no runtime", []string{"MKV, 1 video track, 1 audio track"}, 0},
		{"bare h:mm:ss", []string{"0:00:00"}, 0},
		{"later line wins", []string{"M2TS, 1 video track, 1:00:00", "M2TS, 1 video track, 1:00:01"}, 3601},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := NewDetector("a", "w")
			if err := d.FeedAll(strings.Join(tc.lines, "\n")); err != nil {
				t.Fatalf("FeedAll() error = %v", err)
			}
			if got := d.Length(); got != tc.want {
				t.Errorf("Length() = %d, want %d", got, tc.want)
			}
		})
	}
}

// --- progress parsing ------------------------------------------------------

func TestParseProgress(t *testing.T) {
	t.Parallel()
	cases := []struct {
		line      string
		ok        bool
		percent   float64
		analyze   bool
		completed bool
	}{
		{"analyze: 34%", true, 34, true, false},
		{"[a03] analyze: 100%", true, 100, true, false},
		{"process: 55%", true, 55, false, false},
		{"process: 1%", false, 0, false, false}, // the legacy p > 1 filter
		{"process: 0%", false, 0, false, false}, // ditto
		{"analyze: 1%", false, 0, false, false}, // ditto
		{"Done.", true, 100, false, true},
		{"eac3to processing took 9 minutes, 12 seconds.", false, 0, false, false},
		{"", false, 0, false, false},
		{"[a03] Creating file \"00001_3.flac\"...", false, 0, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			t.Parallel()
			got, ok := ParseProgress(tc.line)
			if ok != tc.ok {
				t.Fatalf("ParseProgress(%q) ok = %v, want %v", tc.line, ok, tc.ok)
			}
			if !ok {
				return
			}
			if got.Percent != tc.percent {
				t.Errorf("Percent = %v, want %v", got.Percent, tc.percent)
			}
			if got.Analyze != tc.analyze {
				t.Errorf("Analyze = %v, want %v", got.Analyze, tc.analyze)
			}
			if got.Completed != tc.completed {
				t.Errorf("Completed = %v, want %v", got.Completed, tc.completed)
			}
		})
	}
}

// --- output file names -----------------------------------------------------

func TestOutFileName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		source string
		prefix string
		index  int
		codec  TrackCodec
		want   string
	}{
		{
			name:   "windows paths",
			source: `D:\BDMV\STREAM\00001.m2ts`,
			prefix: `D:\work\00001`,
			index:  3,
			codec:  CodecTrueHDAC3,
			want:   `D:\work\00001_3.flac`,
		},
		{
			name:   "the working prefix contributes only its directory",
			source: `D:\BDMV\STREAM\00001.m2ts`,
			prefix: `D:\work\00001` + "_part0",
			index:  5,
			codec:  CodecAC3,
			want:   `D:\work\00001_5.ac3`,
		},
		{
			name:   "source stem is used, not the prefix name",
			source: `E:\in\FEATURE_1.evo`,
			prefix: `E:\out\whatever`,
			index:  2,
			codec:  CodecDTSMA,
			want:   `E:\out\FEATURE_1_2.flac`,
		},
		{
			name:   "two-digit index",
			source: `D:\in\movie.mkv`,
			prefix: `D:\work\movie`,
			index:  12,
			codec:  CodecPGS,
			want:   `D:\work\movie_12.sup`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			track := &TrackInfo{
				Codec:             tc.codec,
				Index:             tc.index,
				SourceFile:        tc.source,
				WorkingPathPrefix: tc.prefix,
			}
			got := filepath.ToSlash(track.OutFileName())
			if got != filepath.ToSlash(tc.want) {
				t.Errorf("OutFileName() = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- empty and duplicate detection ----------------------------------------

func TestIsEmpty(t *testing.T) {
	t.Parallel()
	const threshold = 873 // 3 * 1024 * 1024 / 3600, truncated
	cases := []struct {
		name string
		t    TrackInfo
		want bool
	}{
		// Audio: silent only when *both* levels are below the thresholds.
		{"audio silent", TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -71, MaxVolume: -31}, true},
		{"audio mean silent, peak loud", TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -71, MaxVolume: -29}, false},
		{"audio mean loud", TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -69, MaxVolume: -31}, false},
		{"audio exactly at the mean threshold", TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -70, MaxVolume: -31}, false},
		{"audio exactly at the peak threshold", TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -71, MaxVolume: -30}, false},
		{"audio negative infinity", TrackInfo{Type: model.TrackTypeAudio, MeanVolume: negInf(), MaxVolume: negInf()}, true},
		{"audio not measured", TrackInfo{Type: model.TrackTypeAudio}, false},

		// Subtitle: FileSize/Length, integer division.
		{"subtitle at the threshold", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: threshold, Length: 1}, false},
		{"subtitle one byte under", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: threshold - 1, Length: 1}, true},
		{"subtitle empty", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 0, Length: 3600}, true},
		{"subtitle normal", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 3 << 20, Length: 3600}, false},
		{"subtitle exactly 3 MiB per hour", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 3 << 20, Length: 3600}, false},
		// The threshold is per second, so a long track needs proportionally more.
		{"subtitle 2 MiB over two hours", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 2 << 20, Length: 7200}, true},
		{"subtitle unknown runtime", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 10, Length: 0}, false},

		// Everything else: 64 bytes.
		{"video 64 bytes", TrackInfo{Type: model.TrackTypeVideo, FileSize: 64}, false},
		{"video 63 bytes", TrackInfo{Type: model.TrackTypeVideo, FileSize: 63}, true},
		{"chapter empty", TrackInfo{Type: model.TrackTypeChapter, FileSize: 0}, true},
		{"default type 0 bytes", TrackInfo{Type: model.TrackTypeDefault, FileSize: 0}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			track := tc.t
			if got := track.IsEmpty(); got != tc.want {
				t.Errorf("IsEmpty() = %v, want %v (track %+v)", got, tc.want, track)
			}
		})
	}
}

func TestIsDuplicate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		a, b TrackInfo
		want bool
	}{
		{
			name: "identical audio",
			a:    TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -23.45, MaxVolume: -1.02},
			b:    TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -23.45, MaxVolume: -1.02},
			want: true,
		},
		{
			name: "audio within 0.01",
			a:    TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -23.45, MaxVolume: -1.02},
			b:    TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -23.4599, MaxVolume: -1.011},
			want: true,
		},
		{
			name: "audio mean differs",
			a:    TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -23.45, MaxVolume: -1.02},
			b:    TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -23.47, MaxVolume: -1.02},
			want: false,
		},
		{
			name: "audio peak differs",
			a:    TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -23.45, MaxVolume: -1.02},
			b:    TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -23.45, MaxVolume: -1.04},
			want: false,
		},
		{
			name: "exactly 0.01 apart is not a duplicate",
			a:    TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -23.45, MaxVolume: -1.02},
			b:    TrackInfo{Type: model.TrackTypeAudio, MeanVolume: -23.46, MaxVolume: -1.02},
			want: false,
		},
		{
			name: "audio sizes are irrelevant",
			a:    TrackInfo{Type: model.TrackTypeAudio, FileSize: 100, MeanVolume: -20, MaxVolume: -1},
			b:    TrackInfo{Type: model.TrackTypeAudio, FileSize: 999, MeanVolume: -20, MaxVolume: -1},
			want: true,
		},
		{
			name: "subtitles compare size",
			a:    TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 12345},
			b:    TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 12345},
			want: true,
		},
		{
			name: "subtitles of different size",
			a:    TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 12345},
			b:    TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 12346},
			want: false,
		},
		{
			name: "different types are never duplicates",
			a:    TrackInfo{Type: model.TrackTypeAudio, FileSize: 7},
			b:    TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 7},
			want: false,
		},
		{
			name: "chapters compare size",
			a:    TrackInfo{Type: model.TrackTypeChapter, FileSize: 900},
			b:    TrackInfo{Type: model.TrackTypeChapter, FileSize: 900},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, b := tc.a, tc.b
			if got := a.IsDuplicate(&b); got != tc.want {
				t.Errorf("IsDuplicate() = %v, want %v", got, tc.want)
			}
			// The relation is symmetric and reflexive in the legacy code too.
			if got := b.IsDuplicate(&a); got != tc.want {
				t.Errorf("IsDuplicate() reversed = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMarkSkipping covers the two branches: a rename to ".bak{ext}", and a
// delete when the backup cannot be made (which is what a restarted task hits).
func TestMarkSkipping(t *testing.T) {
	t.Parallel()

	t.Run("renames to bak", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		track := &TrackInfo{
			Codec:             CodecAC3,
			Index:             3,
			SourceFile:        filepath.Join(dir, "00001.m2ts"),
			WorkingPathPrefix: filepath.Join(dir, "00001"),
		}
		if err := os.WriteFile(track.OutFileName(), []byte("audio"), 0o600); err != nil {
			t.Fatal(err)
		}
		track.MarkSkipping()
		if !track.DupOrEmpty {
			t.Error("DupOrEmpty = false, want true")
		}
		want := filepath.Join(dir, "00001_3.bak.ac3")
		if _, err := os.Stat(want); err != nil {
			t.Errorf("backup %s missing: %v", want, err)
		}
		if _, err := os.Stat(track.OutFileName()); !os.IsNotExist(err) {
			t.Errorf("original still present: %v", err)
		}
	})

	t.Run("deletes when the backup name is taken", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		track := &TrackInfo{
			Codec:             CodecPGS,
			Index:             6,
			SourceFile:        filepath.Join(dir, "00001.m2ts"),
			WorkingPathPrefix: filepath.Join(dir, "00001"),
		}
		if err := os.WriteFile(track.OutFileName(), []byte("sub"), 0o600); err != nil {
			t.Fatal(err)
		}
		// .NET File.Move fails when the destination exists; the legacy code
		// then deleted the file instead.
		if err := os.WriteFile(filepath.Join(dir, "00001_6.bak.sup"), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
		track.MarkSkipping()
		if !track.DupOrEmpty {
			t.Error("DupOrEmpty = false, want true")
		}
		if _, err := os.Stat(track.OutFileName()); !os.IsNotExist(err) {
			t.Errorf("original still present: %v", err)
		}
		if b, err := os.ReadFile(filepath.Join(dir, "00001_6.bak.sup")); err != nil || string(b) != "old" {
			t.Errorf("existing backup was modified: %q, %v", b, err)
		}
	})
}

// --- extraction planning ---------------------------------------------------

// TestPlanExtraction pins the positional mapping between source tracks and the
// profile lists, the MuxOption.Skip handling and the Lossy downgrade.
func TestPlanExtraction(t *testing.T) {
	t.Parallel()
	tracks := []*TrackInfo{
		{Codec: CodecH264AVC, Index: 2, Type: model.TrackTypeVideo},
		{Codec: CodecTrueHDAC3, Index: 3, Type: model.TrackTypeAudio},
		{Codec: CodecDTSMA, Index: 4, Type: model.TrackTypeAudio},
		{Codec: CodecAC3, Index: 5, Type: model.TrackTypeAudio},
		{Codec: CodecEAC3, Index: 6, Type: model.TrackTypeAudio},
		{Codec: CodecPGS, Index: 7, Type: model.TrackTypeSubtitle},
		{Codec: CodecVobSub, Index: 8, Type: model.TrackTypeSubtitle},
		{Codec: CodecChapter, Index: 1, Type: model.TrackTypeChapter},
	}
	for _, tr := range tracks {
		tr.SourceFile = `D:\src\00001.m2ts`
		tr.WorkingPathPrefix = `D:\work\00001`
	}

	cases := []struct {
		name      string
		audio     []model.AudioInfo
		subs      []model.Info
		wantArgs  []string
		wantCodec []TrackCodec
	}{
		{
			name: "every track extracted",
			audio: []model.AudioInfo{
				{Info: model.NewInfo()},
				{Info: model.NewInfo()},
				{Info: model.NewInfo()},
				{Info: model.NewInfo()},
			},
			subs:      []model.Info{model.NewInfo()},
			wantArgs:  []string{"3:", `D:\work\00001_3.flac`, "4:", `D:\work\00001_4.flac`, "5:", `D:\work\00001_5.ac3`, "6:", `D:\work\00001_6.eac3`, "7:", `D:\work\00001_7.sup`},
			wantCodec: []TrackCodec{CodecTrueHDAC3, CodecDTSMA, CodecAC3, CodecEAC3, CodecPGS},
		},
		{
			name: "skip still consumes a profile slot",
			audio: []model.AudioInfo{
				{Info: model.NewInfo()},
				{Info: model.Info{Mux: model.MuxOptionSkip}},
				{Info: model.NewInfo()},
				{Info: model.NewInfo()},
			},
			subs:      []model.Info{model.NewInfo()},
			wantArgs:  []string{"3:", `D:\work\00001_3.flac`, "5:", `D:\work\00001_5.ac3`, "6:", `D:\work\00001_6.eac3`, "7:", `D:\work\00001_7.sup`},
			wantCodec: []TrackCodec{CodecTrueHDAC3, CodecAC3, CodecEAC3, CodecPGS},
		},
		{
			name: "lossy downgrades everything but EAC3",
			audio: []model.AudioInfo{
				{Info: model.NewInfo(), Lossy: true},
				{Info: model.NewInfo(), Lossy: true},
				{Info: model.NewInfo(), Lossy: true},
				{Info: model.NewInfo(), Lossy: true},
			},
			subs:      []model.Info{model.NewInfo()},
			wantArgs:  []string{"3:", `D:\work\00001_3.flac`, "4:", `D:\work\00001_4.flac`, "5:", `D:\work\00001_5.flac`, "6:", `D:\work\00001_6.eac3`, "7:", `D:\work\00001_7.sup`},
			wantCodec: []TrackCodec{CodecFLAC, CodecFLAC, CodecFLAC, CodecEAC3, CodecPGS},
		},
		{
			name:      "vobsub and chapters are never extracted",
			audio:     []model.AudioInfo{{Info: model.NewInfo()}, {Info: model.NewInfo()}, {Info: model.NewInfo()}, {Info: model.NewInfo()}},
			subs:      []model.Info{model.NewInfo()},
			wantArgs:  []string{"3:", `D:\work\00001_3.flac`, "4:", `D:\work\00001_4.flac`, "5:", `D:\work\00001_5.ac3`, "6:", `D:\work\00001_6.eac3`, "7:", `D:\work\00001_7.sup`},
			wantCodec: []TrackCodec{CodecTrueHDAC3, CodecDTSMA, CodecAC3, CodecEAC3, CodecPGS},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// Rebuild the tracks for every case: planExtraction mutates Codec.
			fresh := make([]*TrackInfo, len(tracks))
			for i, tr := range tracks {
				cp := *tr
				fresh[i] = &cp
			}
			p := New(Options{})
			plan := p.planExtraction(fresh, tc.audio, tc.subs)
			if len(plan.args) != len(tc.wantArgs) {
				t.Fatalf("args = %v, want %v", plan.args, tc.wantArgs)
			}
			for i := range tc.wantArgs {
				if plan.args[i] != tc.wantArgs[i] {
					t.Fatalf("args[%d] = %q, want %q (full: %v)", i, plan.args[i], tc.wantArgs[i], plan.args)
				}
			}
			if len(plan.tracks) != len(tc.wantCodec) {
				t.Fatalf("extracted %d tracks, want %d", len(plan.tracks), len(tc.wantCodec))
			}
			for i, want := range tc.wantCodec {
				if plan.tracks[i].Codec != want {
					t.Errorf("track[%d].Codec = %s, want %s", i, plan.tracks[i].Codec, want)
				}
			}
		})
	}
}

// --- count reconciliation --------------------------------------------------

func TestReconcile(t *testing.T) {
	t.Parallel()
	allTracks := []*TrackInfo{
		{Type: model.TrackTypeAudio},
		{Type: model.TrackTypeAudio},
		{Type: model.TrackTypeSubtitle},
	}
	oneAudio := []*TrackInfo{
		{Type: model.TrackTypeAudio},
		{Type: model.TrackTypeSubtitle},
	}
	cases := []struct {
		name      string
		tracks    []*TrackInfo
		audio     []model.AudioInfo
		subs      []model.Info
		wantAudio int
		wantSubs  int
		wantErr   string
	}{
		{
			name:      "exact match",
			audio:     []model.AudioInfo{{Info: model.NewInfo()}, {Info: model.NewInfo()}},
			subs:      []model.Info{model.NewInfo()},
			wantAudio: 2,
			wantSubs:  1,
		},
		{
			name: "optional audio is kept when the source has every listed track",
			audio: []model.AudioInfo{
				{Info: model.NewInfo()},
				{Info: model.Info{Optional: true}},
			},
			subs:      []model.Info{model.NewInfo()},
			wantAudio: 2,
			wantSubs:  1,
		},
		{
			// The source has only the required tracks, so the optional entries
			// are dropped rather than mis-assigned to the tracks that exist.
			name:   "optional audio is dropped when the source lacks them",
			tracks: oneAudio,
			audio: []model.AudioInfo{
				{Info: model.NewInfo()},
				{Info: model.Info{Optional: true}},
			},
			subs:      []model.Info{model.NewInfo()},
			wantAudio: 1,
			wantSubs:  1,
		},
		{
			name:  "optional subtitle is dropped",
			audio: []model.AudioInfo{{Info: model.NewInfo()}, {Info: model.NewInfo()}},
			subs: []model.Info{
				model.NewInfo(),
				{Optional: true},
			},
			wantAudio: 2,
			wantSubs:  1,
		},
		{
			name: "too few source audio tracks",
			audio: []model.AudioInfo{
				{Info: model.NewInfo()},
				{Info: model.NewInfo()},
				{Info: model.NewInfo()},
			},
			subs:    []model.Info{model.NewInfo()},
			wantErr: okerr.ErrAudioNumMismatch.Summary,
		},
		{
			name: "too many source audio tracks",
			audio: []model.AudioInfo{
				{Info: model.NewInfo()},
				{Info: model.NewInfo()},
				{Info: model.NewInfo()},
				{Info: model.NewInfo()},
			},
			subs:    []model.Info{model.NewInfo()},
			wantErr: okerr.ErrAudioNumMismatch.Summary,
		},
		{
			name:    "subtitle count mismatch",
			audio:   []model.AudioInfo{{Info: model.NewInfo()}, {Info: model.NewInfo()}},
			subs:    []model.Info{model.NewInfo(), model.NewInfo()},
			wantErr: okerr.ErrSubNumMismatch.Summary,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := tc.tracks
			if src == nil {
				src = allTracks
			}
			p := New(Options{AudioTracks: tc.audio, SubtitleTracks: tc.subs, SourceFile: "x.m2ts"})
			audio, subs, err := p.reconcile(src)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("reconcile() = nil, want %q", tc.wantErr)
				}
				if got := okerr.AsError(err).Summary; got != tc.wantErr {
					t.Fatalf("summary = %q, want %q", got, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("reconcile() error = %v", err)
			}
			if len(audio) != tc.wantAudio {
				t.Errorf("audio = %d entries, want %d", len(audio), tc.wantAudio)
			}
			if len(subs) != tc.wantSubs {
				t.Errorf("subs = %d entries, want %d", len(subs), tc.wantSubs)
			}
			// The caller's slices must be untouched.
			if len(tc.audio) != len(p.opts.AudioTracks) {
				t.Errorf("Options.AudioTracks was mutated")
			}
			if len(tc.subs) != len(p.opts.SubtitleTracks) {
				t.Errorf("Options.SubtitleTracks was mutated")
			}
		})
	}
}

// --- log naming ------------------------------------------------------------

// TestLogNameIsCRC32 pins the hash scheme that keeps eac3to from choking on
// spaces in the log path.
func TestLogNameIsCRC32(t *testing.T) {
	t.Parallel()
	// The standard CRC-32 check value, proving the polynomial and the
	// init/final xor match SafeProxy.Append(0, ...).
	if got := crc32.ChecksumIEEE([]byte("123456789")); got != 0xCBF43926 {
		t.Fatalf("crc32(\"123456789\") = %08X, want CBF43926", got)
	}
	p := New(Options{
		SourceFile:        `D:\BDMV\STREAM\00001.m2ts`,
		WorkingPathPrefix: `D:\work\00001`,
	})
	workDir := filepath.Dir(p.opts.WorkingPathPrefix)
	logPath := filepath.Join(workDir, "00001.eac3to.log")
	want := fmt.Sprintf("%08X.eac3to.log", crc32.ChecksumIEEE([]byte(logPath)))
	got := fmt.Sprintf("%08X.eac3to.log", crc32.ChecksumIEEE([]byte(`D:\work\00001.eac3to.log`)))
	if got != want {
		t.Fatalf("hash = %q, want %q", got, want)
	}
}

// --- end to end ------------------------------------------------------------

// TestRunEndToEnd drives both passes against a fake eac3to: the test binary
// re-executes itself as the helper, prints a real listing on the analysis pass
// and writes the extracted files on the extraction pass. This exercises the
// argument shape, the log rename, the empty/duplicate sweep and the MediaFile
// assembly without needing the closed-source binary.
func TestRunEndToEnd(t *testing.T) {
	// Serial: helperSpec sets environment variables for the child, which
	// t.Parallel would forbid.
	dir := t.TempDir()
	source := filepath.Join(dir, "00001.m2ts")
	if err := os.WriteFile(source, []byte("not really a transport stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	// eac3to writes its log next to the working prefix, i.e. in dir.
	prefix := filepath.Join(dir, "00001")

	listing := readFixture(t, "m2ts_truehd_dtsma_ac3_pgs.log")
	sizes := map[string]int{
		"3": 4 << 20, // 4 MiB of audio: not empty
		"4": 4 << 20,
		"5": 1 << 20,
		"6": 2 << 20, // PGS over 5927 s: well above the 873 B/s threshold
		"7": 2 << 20,
	}
	spec := helperSpec(t, listing, sizes, "")

	p := New(Options{
		EacPath:           spec.Path,
		SourceFile:        source,
		WorkingPathPrefix: prefix,
		AudioTracks: []model.AudioInfo{
			{Info: model.NewInfo()},
			{Info: model.NewInfo()},
			{Info: model.NewInfo()},
		},
		SubtitleTracks: []model.Info{model.NewInfo(), model.NewInfo()},
		Volume: VolumeFunc(func(_ context.Context, file string) (float64, float64, error) {
			// Track 3 is silent, track 4 is loud, track 5 is quiet but
			// audible. The demuxer must flag only track 3 as empty.
			switch {
			case strings.Contains(file, "_3."):
				return -80, -40, nil
			case strings.Contains(file, "_4."):
				return -23.5, -1.2, nil
			default:
				return -45, -20, nil
			}
		}),
	})

	sink := &recordingSink{}
	if err := p.Run(helperCtx(t), sink); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	res := p.Result()
	if res == nil {
		t.Fatal("Result() = nil after a successful Run")
	}

	if res.Length != 1*3600+38*60+47 {
		t.Errorf("Length = %d, want 5927", res.Length)
	}
	if len(res.Tracks) != 7 {
		t.Errorf("parsed %d tracks, want 7", len(res.Tracks))
	}
	if len(res.Extracted) != 5 {
		t.Fatalf("extracted %d tracks, want 5", len(res.Extracted))
	}

	// Track 3 was silent: its file is moved aside and it is not muxed.
	if !res.Extracted[0].DupOrEmpty {
		t.Error("track 3 was not flagged as empty")
	}
	if _, err := os.Stat(filepath.Join(dir, "00001_3.bak.flac")); err != nil {
		t.Errorf("empty track was not backed up: %v", err)
	}
	media := res.MediaFile
	if len(media.AudioTracks) != 3 {
		t.Fatalf("MediaFile has %d audio tracks, want 3", len(media.AudioTracks))
	}
	if media.AudioTracks[0].Info.Mux != model.MuxOptionExtractOnly {
		t.Errorf("empty track mux option = %s, want ExtractOnly", media.AudioTracks[0].Info.Mux)
	}
	if media.AudioTracks[1].Info.Mux != model.MuxOptionDefault {
		t.Errorf("healthy track mux option = %s, want Default", media.AudioTracks[1].Info.Mux)
	}
	if len(media.SubtitleTracks) != 2 {
		t.Fatalf("MediaFile has %d subtitle tracks, want 2", len(media.SubtitleTracks))
	}
	if media.Video != nil {
		t.Error("MediaFile gained a video track; the video comes from the .vpy script")
	}
	if media.Chapter != nil {
		t.Error("MediaFile gained a chapter track; chapters come from ChapterService")
	}

	// audioInfo.Length is the container runtime in whole seconds. The fixture
	// header carries 1:38:47, so every audio track must have picked it up.
	for i, a := range media.AudioTracks {
		if a.Audio.Length != 1*3600+38*60+47 {
			t.Errorf("audio[%d].Audio.Length = %d, want 5927", i, a.Audio.Length)
		}
	}

	// The hash-named log must have been renamed to its final name.
	finalLog := filepath.Join(dir, "00001.eac3to.log")
	if _, err := os.Stat(finalLog); err != nil {
		t.Errorf("final log %s missing: %v", finalLog, err)
	}
	if res.LogPath != finalLog {
		t.Errorf("LogPath = %q, want %q", res.LogPath, finalLog)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".eac3to.log") && e.Name() != "00001.eac3to.log" {
			t.Errorf("stray hash log left behind: %s", e.Name())
		}
	}

	// Progress must have reached 100.
	if got := sink.lastPercent(); got != 100 {
		t.Errorf("last reported percent = %v, want 100", got)
	}
	if sink.statuses[StatusExtracted] == 0 {
		t.Error("the completion status was never reported")
	}
}

// TestRunDuplicateAudioIsFlagged drives the duplicate branch: two tracks with
// identical levels, which is what a disc with a duplicated audio stream looks
// like.
func TestRunDuplicateAudioIsFlagged(t *testing.T) {
	// Serial: helperSpec sets environment variables for the child, which
	// t.Parallel would forbid.
	dir := t.TempDir()
	source := filepath.Join(dir, "00001.m2ts")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	listing := strings.Join([]string{
		"M2TS, 1 video track, 3 audio tracks, 0:10:00",
		"2: h264/AVC, 1080p24 /1.001 (16:9)",
		"3: AC3, English, 5.1 channels, 640kbps, 48kHz",
		"4: AC3, English, 5.1 channels, 640kbps, 48kHz",
		"5: AC3, Japanese, 2.0 channels, 192kbps, 48kHz",
		"",
		"Done.",
		"",
	}, "\n")
	sizes := map[string]int{"3": 1 << 20, "4": 1 << 20, "5": 1 << 20}

	p := New(Options{
		EacPath:           helperSpec(t, listing, sizes, "").Path,
		SourceFile:        source,
		WorkingPathPrefix: filepath.Join(dir, "00001"),
		AudioTracks: []model.AudioInfo{
			{Info: model.NewInfo()},
			{Info: model.NewInfo()},
			{Info: model.NewInfo()},
		},
		Volume: VolumeFunc(func(_ context.Context, file string) (float64, float64, error) {
			if strings.Contains(file, "_5.") {
				return -30, -5, nil
			}
			return -23.45, -1.02, nil
		}),
	})
	if err := p.Run(helperCtx(t), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	res := p.Result()
	if len(res.Extracted) != 3 {
		t.Fatalf("extracted %d tracks, want 3", len(res.Extracted))
	}
	if res.Extracted[0].DupOrEmpty {
		t.Error("the first of two identical tracks must be kept")
	}
	if !res.Extracted[1].DupOrEmpty {
		t.Error("the second of two identical tracks must be flagged")
	}
	if res.Extracted[2].DupOrEmpty {
		t.Error("the distinct track must not be flagged")
	}
	if _, err := os.Stat(filepath.Join(dir, "00001_4.bak.ac3")); err != nil {
		t.Errorf("duplicate was not backed up: %v", err)
	}
}

// TestRunExitCodeIsReported pins the okerr.ErrEac3to path and the exit code
// carried on the error.
func TestRunExitCodeIsReported(t *testing.T) {
	// Serial: helperSpec sets environment variables for the child, which
	// t.Parallel would forbid.
	dir := t.TempDir()
	source := filepath.Join(dir, "00001.m2ts")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := helperSpec(t, "", nil, "3") // exit code 3 on the analysis pass
	p := New(Options{
		EacPath:           spec.Path,
		SourceFile:        source,
		WorkingPathPrefix: filepath.Join(dir, "00001"),
	})
	err := p.Run(helperCtx(t), nil)
	if err == nil {
		t.Fatal("Run() = nil, want an error")
	}
	e := okerr.AsError(err)
	if e.Summary != okerr.ErrEac3to.Summary {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrEac3to.Summary)
	}
	if e.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", e.ExitCode)
	}
	if e.Tool != Name {
		t.Errorf("tool = %q, want %q", e.Tool, Name)
	}
	if !strings.Contains(okerr.Render(e), "退出代码3") {
		t.Errorf("Render() = %q, want the legacy template with the exit code", okerr.Render(e))
	}
}

// TestRunMissingSource pins the early return the legacy code made when the
// input file did not exist.
func TestRunMissingSource(t *testing.T) {
	t.Parallel()
	p := New(Options{
		EacPath:    os.Args[0],
		SourceFile: filepath.Join(t.TempDir(), "nope.m2ts"),
	})
	err := p.Run(helperCtx(t), nil)
	if err == nil {
		t.Fatal("Run() = nil, want an error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindNotFound {
		t.Errorf("kind = %q, want %q", got, okerr.KindNotFound)
	}
}

// TestSkipAllTracks pins the SkipAllAudioTracks/SkipAllSubtitleTracks filters,
// which run before the count check.
func TestSkipAllTracks(t *testing.T) {
	// Serial: helperSpec sets environment variables for the child, which
	// t.Parallel would forbid.
	dir := t.TempDir()
	source := filepath.Join(dir, "00001.m2ts")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	listing := readFixture(t, "m2ts_truehd_dtsma_ac3_pgs.log")
	sizes := map[string]int{"3": 4 << 20, "4": 4 << 20, "5": 1 << 20, "6": 2 << 20, "7": 2 << 20}
	spec := helperSpec(t, listing, sizes, "")

	p := New(Options{
		EacPath:               spec.Path,
		SourceFile:            source,
		WorkingPathPrefix:     filepath.Join(dir, "00001"),
		SkipAllAudioTracks:    true,
		SkipAllSubtitleTracks: true,
		// No profile tracks at all: the legacy code removed the source tracks
		// first, so the count check is 0 == 0.
		Volume: VolumeFunc(func(context.Context, string) (float64, float64, error) {
			return -20, -1, nil
		}),
	})
	if err := p.Run(helperCtx(t), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	res := p.Result()
	if len(res.Tracks) != 2 {
		t.Errorf("kept %d tracks, want only the chapter and video tracks", len(res.Tracks))
	}
	if len(res.Extracted) != 0 {
		t.Errorf("extracted %d tracks, want 0", len(res.Extracted))
	}
}

// --- test helpers ----------------------------------------------------------

// TestMain turns the test binary into the fake eac3to when the demuxer spawns
// it, and otherwise runs the suite normally.
//
// The demuxer builds its own argument list, so the child cannot be told to run
// only TestHelperProcess through -test.run: Go's flag parser rejects eac3to's
// arguments outright. The child is instead identified by the control file the
// test writes, whose path travels in the environment the test process sets
// before spawning. Without this guard the child would run the whole suite, and
// TestRunEndToEnd inside it would spawn further children without bound.
func TestMain(m *testing.M) {
	// The fail-at case takes precedence: it simulates eac3to exiting with a
	// status before it produces any output.
	if failAt := os.Getenv(helperEnvFailAt); failAt != "" {
		os.Exit(atoi(failAt))
	}
	if listing := os.Getenv(helperEnvListing); listing != "" {
		runFakeEac3to(listing)
		return
	}
	os.Exit(m.Run())
}

// runFakeEac3to is the child process body: it prints the configured listing on
// the analysis pass and writes the requested output files on the extraction
// pass. It never returns.
func runFakeEac3to(listingPath string) {
	var (
		logName string
		out     []string
	)
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-log=") {
			logName = strings.TrimPrefix(a, "-log=")
			continue
		}
		// The demuxer passes "N:" and the path as two arguments, which is the
		// shape eac3to's own splitter expects.
		if strings.HasSuffix(a, ":") && a != ":" && isDigits(strings.TrimSuffix(a, ":")) {
			if i+1 < len(args) {
				out = append(out, strings.TrimSuffix(a, ":"), args[i+1])
				i++
			}
		}
	}

	if b, err := os.ReadFile(listingPath); err == nil {
		os.Stdout.Write(b)
	} else {
		fmt.Fprintln(os.Stderr, "helper: read listing:", err)
		os.Exit(9)
	}

	// The analysis pass stops here: no extraction arguments were given.
	if len(out) == 0 {
		if logName != "" {
			_ = os.WriteFile(logName, []byte("analysis log\n"), 0o600)
		}
		os.Exit(0)
	}

	fmt.Fprintln(os.Stdout, "Analyzing 1, 2, 3...")
	var sizes map[string]int
	if raw := os.Getenv(helperEnvSizes); raw != "" {
		if err := json.Unmarshal([]byte(raw), &sizes); err != nil {
			fmt.Fprintln(os.Stderr, "helper: bad sizes:", err)
			os.Exit(9)
		}
	}
	for i := 0; i+1 < len(out); i += 2 {
		id, path := out[i], out[i+1]
		n := sizes[id]
		if n == 0 {
			n = 1024
		}
		if err := os.WriteFile(path, make([]byte, n), 0o600); err != nil {
			fmt.Fprintln(os.Stderr, "helper: write:", err)
			os.Exit(9)
		}
		fmt.Fprintf(os.Stdout, "[a%02s] Creating file %q...\n", id, filepath.Base(path))
	}
	fmt.Fprintln(os.Stdout, "analyze: 50%")
	fmt.Fprintln(os.Stdout, "process: 100%")
	fmt.Fprintln(os.Stdout, "Done.")
	if logName != "" {
		_ = os.WriteFile(logName, []byte("extraction log\n"), 0o600)
	}
	os.Exit(0)
}

// helperCtx bounds every end-to-end test that drives a child process. Without a
// deadline, a bug in the demuxer's wait logic would leave the fake eac3to
// running forever; with one, the test fails instead of leaking a process.
func helperCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

const (
	helperEnvListing = "OKEGUIDX_EAC3TO_LISTING"
	helperEnvSizes   = "OKEGUIDX_EAC3TO_SIZES"
	helperEnvFailAt  = "OKEGUIDX_EAC3TO_FAIL_AT"
)

// helperSpec builds a proc.Spec that re-executes the test binary as the fake
// eac3to.
//
// The marker travels in the process environment rather than in the argument
// list, because the demuxer builds its own arguments and would overwrite them.
// TestMain reads the marker and turns the binary into the fake tool.
func helperSpec(t *testing.T, listing string, sizes map[string]int, failAt string) proc.Spec {
	t.Helper()
	// The fail-at case must not also set the listing marker, or the child would
	// print a listing instead of exiting with the requested status.
	if failAt != "" {
		t.Setenv(helperEnvFailAt, failAt)
	} else {
		t.Setenv(helperEnvListing, "1")
		if listing != "" {
			path := filepath.Join(t.TempDir(), "listing.log")
			if err := os.WriteFile(path, []byte(listing), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(helperEnvListing, path)
		}
	}
	if len(sizes) > 0 {
		raw, err := json.Marshal(sizes)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(helperEnvSizes, string(raw))
	}
	return proc.Spec{
		Path: os.Args[0],
		Name: Name,
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

func negInf() float64 {
	return -1 / zero()
}

func zero() float64 { return 0 }

// recordingSink captures every progress update.
type recordingSink struct {
	percents []float64
	statuses map[string]int
}

func (s *recordingSink) Report(p jobproc.Progress) {
	if s.statuses == nil {
		s.statuses = make(map[string]int)
	}
	if p.Status != "" {
		s.statuses[p.Status]++
	}
	if p.Percent > 0 {
		s.percents = append(s.percents, p.Percent)
	}
}

func (s *recordingSink) lastPercent() float64 {
	if len(s.percents) == 0 {
		return 0
	}
	return s.percents[len(s.percents)-1]
}
