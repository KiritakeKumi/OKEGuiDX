package ffmpeg

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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

// wantTrack is the expectation for one track. Length is the container runtime
// the probe reported, which every track of one source shares.
type wantTrack struct {
	index   int
	codec   TrackCodec
	typ     model.TrackType
	ext     string
	delayMS int
}

// TestTracks drives the probe parser over the fixtures in testdata/ and asserts
// the full track enumeration, including the index assignment, the codec mapping
// and the delay each track was stamped with.
func TestTracks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		fixture string
		length  int
		tracks  []wantTrack
	}{
		{
			// A Blu-ray m2ts. ffmpeg's MPEG-TS demuxer splits the HDMV TrueHD
			// PID into the TrueHD stream and a synthetic AC-3 view of the
			// compatibility core; both carry the same id, and eac3to lists the
			// PID once as "TrueHD/AC3". The second stream must be folded away.
			//
			// Every PID of the transport stream starts at the same 0x90000-based
			// timestamp, so no track has a delay once the video is the
			// reference.
			fixture: "m2ts_truehd_dts_ac3.json",
			length:  6,
			tracks: []wantTrack{
				{1, CodecH264AVC, model.TrackTypeVideo, ".264", 0},
				{2, CodecTrueHDAC3, model.TrackTypeAudio, ".flac", 0},
				{3, CodecDTS, model.TrackTypeAudio, ".dts", 0},
				{4, CodecAC3, model.TrackTypeAudio, ".ac3", 0},
			},
		},
		{
			// An MKV with chapters. The chapter entry takes index 1, exactly as
			// eac3to listed it, so the streams shift up by one.
			//
			// The opus stream starts 14 ms before the video in this container
			// (ffmpeg's matroska muxer wrote -0.007 s against the video's
			// +0.007 s), which is real container data and therefore a real
			// delay. Subtitles never carry one: their alignment is the
			// container's business, not the sample stream's.
			fixture: "mkv_flac_opus_ac3_ass.json",
			length:  6,
			tracks: []wantTrack{
				{1, CodecChapter, model.TrackTypeChapter, ".txt", 0},
				{2, CodecH264AVC, model.TrackTypeVideo, ".264", 0},
				{3, CodecFLAC, model.TrackTypeAudio, ".flac", 0},
				{4, CodecOPUS, model.TrackTypeAudio, ".opus", -14},
				{5, CodecAC3, model.TrackTypeAudio, ".ac3", 0},
				{6, CodecASS, model.TrackTypeSubtitle, ".ass", 0},
			},
		},
		{
			// Audio delayed by +1.0 s and +1.5 s relative to the video.
			fixture: "mkv_delayed_positive.json",
			length:  6,
			tracks: []wantTrack{
				{1, CodecH264AVC, model.TrackTypeVideo, ".264", 0},
				{2, CodecAC3, model.TrackTypeAudio, ".ac3", 1000},
				{3, CodecFLAC, model.TrackTypeAudio, ".flac", 1500},
			},
		},
		{
			// Audio running ahead of the video: -1500 ms and -1750 ms.
			fixture: "mkv_delayed_negative.json",
			length:  8,
			tracks: []wantTrack{
				{1, CodecH264AVC, model.TrackTypeVideo, ".264", 0},
				{2, CodecAC3, model.TrackTypeAudio, ".ac3", -1500},
				{3, CodecFLAC, model.TrackTypeAudio, ".flac", -1750},
			},
		},
		{
			// The shape no raw subtitle container can express: the PGS stream
			// begins before the video, so its first cue is negative on movie
			// time. The shift is recorded for the log and not acted on.
			fixture: "mkv_subtitle_before_video.json",
			length:  8,
			tracks: []wantTrack{
				{1, CodecH264AVC, model.TrackTypeVideo, ".264", 0},
				{2, CodecAC3, model.TrackTypeAudio, ".ac3", -2000},
				{3, CodecPGS, model.TrackTypeSubtitle, ".sup", 0},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			t.Parallel()
			probe := parseFixture(t, tc.fixture)
			tracks, err := probe.Tracks(Options{
				SourceFile:        `D:\src\00001.m2ts`,
				WorkingPathPrefix: `D:\work\00001`,
			})
			if err != nil {
				t.Fatalf("Tracks() error = %v", err)
			}
			if got := probe.Length(); got != tc.length {
				t.Errorf("Length() = %d, want %d", got, tc.length)
			}
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
				if got.delay != want.delayMS {
					t.Errorf("track[%d].delay = %d, want %d", i, got.delay, want.delayMS)
				}
				if got.Length != tc.length {
					t.Errorf("track[%d].Length = %d, want %d", i, got.Length, tc.length)
				}
			}
		})
	}
}

// TestCodecMapping pins the ffprobe codec_name to TrackCodec translation,
// including the two profiles that decide between codecs.
func TestCodecMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		profile string
		want    TrackCodec
		ok      bool
	}{
		{"truehd", "", CodecTrueHDAC3, true},
		{"mlp", "", CodecTrueHDAC3, true},
		{"ac3", "", CodecAC3, true},
		{"eac3", "", CodecEAC3, true},
		{"flac", "", CodecFLAC, true},
		{"aac", "", CodecAAC, true},
		{"opus", "", CodecOPUS, true},
		// DTS is resolved by profile: the lossless extensions become FLAC,
		// the bare core keeps its .dts extension.
		{"dts", "DTS", CodecDTS, true},
		{"dts", "DTS-ES", CodecDTS, true},
		{"dts", "DTS-HD MA", CodecDTSMA, true},
		{"dts", "DTS-HD HRA", CodecDTSMA, true},
		{"dts", "DTS 96/24", CodecDTS, true},
		{"dts", "", CodecDTS, true},
		// Blu-ray LPCM.
		{"pcm_bluray", "", CodecRAWPCM, true},
		{"pcm_s16be", "", CodecRAWPCM, true},
		{"pcm_s24le", "", CodecRAWPCM, true},
		// Video.
		{"h264", "High", CodecH264AVC, true},
		{"hevc", "Main 10", CodecH265HEVC, true},
		{"mpeg2video", "Main", CodecMPEG2, true},
		{"av1", "Main", CodecAV1, true},
		// Subtitles.
		{"hdmv_pgs_subtitle", "", CodecPGS, true},
		{"ass", "", CodecASS, true},
		{"ssa", "", CodecASS, true},
		{"subrip", "", CodecSRT, true},
		{"dvd_subtitle", "", CodecVobSub, true},
		// Unknown, which must be rejected with eac3to's wording.
		{"vc1", "", CodecUnknown, false},
		{"mpeg4", "", CodecUnknown, false},
		{"text", "", CodecUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/"+tc.profile, func(t *testing.T) {
			t.Parallel()
			got, ok := codecFromProbe(tc.name, tc.profile)
			if ok != tc.ok {
				t.Fatalf("codecFromProbe(%q, %q) ok = %v, want %v", tc.name, tc.profile, ok, tc.ok)
			}
			if !ok {
				return
			}
			if got != tc.want {
				t.Errorf("codec = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestUnknownCodecIsRejected mirrors the ArgumentException the legacy code threw
// for a listing line naming a codec the table does not know.
func TestUnknownCodecIsRejected(t *testing.T) {
	t.Parallel()
	raw := `{"streams":[{"index":0,"codec_name":"vc1","codec_type":"video"}]}`
	probe, err := ParseProbe(raw)
	if err != nil {
		t.Fatalf("ParseProbe() error = %v", err)
	}
	_, err = probe.Tracks(Options{SourceFile: "a.m2ts"})
	if err == nil {
		t.Fatal("Tracks() = nil, want an error for the VC-1 stream")
	}
	e := okerr.AsError(err)
	if e.Summary != "不明类型" {
		t.Errorf("summary = %q, want %q", e.Summary, "不明类型")
	}
	if !strings.Contains(e.Detail, "vc1") {
		t.Errorf("detail = %q, want it to name the offending codec", e.Detail)
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
		{
			name:   "a VobSub track keeps its extension even though it is never written",
			source: `D:\in\movie.mkv`,
			prefix: `D:\work\movie`,
			index:  7,
			codec:  CodecVobSub,
			want:   `D:\work\movie_7.sub`,
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

// TestExtractFlag pins which codecs the plan writes. Video, chapters and VobSub
// are listed but never extracted: the video comes from the .vpy script, the
// chapters from ChapterService and eac3to cannot write VobSub at all.
func TestExtractFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		codec TrackCodec
		want  bool
	}{
		{CodecRAWPCM, true},
		{CodecDTSMA, true},
		{CodecTrueHDAC3, true},
		{CodecAC3, true},
		{CodecFLAC, true},
		{CodecAAC, true},
		{CodecDTS, true},
		{CodecEAC3, true},
		{CodecOPUS, true},
		{CodecPGS, true},
		{CodecASS, true},
		{CodecSRT, true},
		{CodecMPEG2, false},
		{CodecH264AVC, false},
		{CodecH265HEVC, false},
		{CodecAV1, false},
		{CodecChapter, false},
		{CodecVobSub, false},
		{CodecUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.codec.String(), func(t *testing.T) {
			t.Parallel()
			track := &TrackInfo{Codec: tc.codec}
			if got := track.extract(); got != tc.want {
				t.Errorf("extract() = %v, want %v", got, tc.want)
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
		{"audio negative infinity", TrackInfo{Type: model.TrackTypeAudio, MeanVolume: math.Inf(-1), MaxVolume: math.Inf(-1)}, true},
		{"audio not measured", TrackInfo{Type: model.TrackTypeAudio}, false},

		// Subtitle: FileSize/Length, integer division.
		{"subtitle at the threshold", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: threshold, Length: 1}, false},
		{"subtitle one byte under", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: threshold - 1, Length: 1}, true},
		{"subtitle empty", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 0, Length: 3600}, true},
		{"subtitle normal", TrackInfo{Type: model.TrackTypeSubtitle, FileSize: 3 << 20, Length: 3600}, false},
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, b := tc.a, tc.b
			if got := a.IsDuplicate(&b); got != tc.want {
				t.Errorf("IsDuplicate() = %v, want %v", got, tc.want)
			}
			if got := b.IsDuplicate(&a); got != tc.want {
				t.Errorf("IsDuplicate() reversed = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMarkSkipping covers the two branches: a rename to ".bak{ext}", and a
// delete when the backup cannot be made.
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

// --- the delay model -------------------------------------------------------

// TestAudioFilter pins the -af expression for every delay shape. The sample
// count form ("S") is preferred whenever the rate is known, because it is exact;
// the millisecond form is the fallback for a stream whose rate the probe did not
// report.
func TestAudioFilter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		delayMS    int
		sampleRate int
		want       string
	}{
		{"no delay", 0, 48000, ""},
		{"positive, rate known", 167, 48000, "adelay=8016S:all=1"},
		{"positive, 1001 ms", 1001, 48000, "adelay=48048S:all=1"},
		{"positive, rate unknown", 167, 0, "adelay=167:all=1"},
		{"negative, rate known", -167, 48000, "atrim=start_sample=8016,asetpts=PTS-STARTPTS"},
		{"negative, 1500 ms", -1500, 48000, "atrim=start_sample=72000,asetpts=PTS-STARTPTS"},
		{"negative, rate unknown", -1500, 0, "atrim=start=1.500000,asetpts=PTS-STARTPTS"},
		// 44100 Hz is not a multiple of 1000, so the rounding shows.
		{"negative at 44100", -1000, 44100, "atrim=start_sample=44100,asetpts=PTS-STARTPTS"},
		{"positive at 44100", 10, 44100, "adelay=441S:all=1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := audioFilter(tc.delayMS, tc.sampleRate); got != tc.want {
				t.Errorf("audioFilter(%d, %d) = %q, want %q", tc.delayMS, tc.sampleRate, got, tc.want)
			}
		})
	}
}

func TestSamplesOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ms   int
		rate int
		want int
	}{
		{0, 48000, 0},
		{1000, 48000, 48000},
		{167, 48000, 8016},
		{1, 48000, 48},
		{-167, 48000, -8016},
		{1000, 44100, 44100},
		{10, 44100, 441},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%dms/%d", tc.ms, tc.rate), func(t *testing.T) {
			t.Parallel()
			if got := samplesOf(tc.ms, tc.rate); got != tc.want {
				t.Errorf("samplesOf(%d, %d) = %d, want %d", tc.ms, tc.rate, got, tc.want)
			}
		})
	}
}

// TestDelayReference pins the alignment base. The video's start time wins when
// there is one, because that is what eac3to aligned against; without video the
// earliest stream becomes the base, which keeps an M2TS's shared transport
// timestamp from turning into a bogus delay for every track.
func TestDelayReference(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
		want float64
	}{
		{
			name: "video at zero",
			raw:  `{"streams":[{"index":0,"codec_type":"video","start_time":"0.000000"},{"index":1,"codec_type":"audio","start_time":"0.000000"}]}`,
			want: 0,
		},
		{
			name: "video late, audio early",
			raw:  `{"streams":[{"index":0,"codec_type":"video","start_time":"2.000000"},{"index":1,"codec_type":"audio","start_time":"0.000000"}]}`,
			want: 2,
		},
		{
			name: "m2ts shared base",
			raw:  `{"streams":[{"index":0,"codec_type":"video","start_time":"1.480000"},{"index":1,"codec_type":"audio","start_time":"1.480000"}]}`,
			want: 1.48,
		},
		{
			name: "no video, earliest wins",
			raw:  `{"streams":[{"index":0,"codec_type":"audio","start_time":"3.000000"},{"index":1,"codec_type":"audio","start_time":"1.000000"}]}`,
			want: 1,
		},
		{
			// With no video and no negative start the container's own start is
			// the reference, which is what ffmpeg rebases onto.
			name: "no video, positive starts",
			raw:  `{"streams":[{"index":0,"codec_type":"audio","start_time":"5.000000"}]}`,
			want: 5,
		},
		{
			name: "an attached picture is not video",
			raw:  `{"streams":[{"index":0,"codec_type":"video","disposition":{"attached_pic":1},"start_time":"9.000000"},{"index":1,"codec_type":"audio","start_time":"2.000000"}]}`,
			want: 2,
		},
		{
			name: "a negative start is kept",
			raw:  `{"streams":[{"index":0,"codec_type":"audio","start_time":"-0.007000"},{"index":1,"codec_type":"audio","start_time":"0.007000"}]}`,
			want: -0.007,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			probe, err := ParseProbe(tc.raw)
			if err != nil {
				t.Fatalf("ParseProbe() error = %v", err)
			}
			if got := probe.delayReference(); got != tc.want {
				t.Errorf("delayReference() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestPadFor pins when the copy path can synthesise a silent lead-in. A lossy
// codec whose bitrate the probe did not report is refused: a pad at the wrong
// bitrate corrupts the concat join, so the caller has to decode instead.
func TestPadFor(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		track   TrackInfo
		wantOK  bool
		wantEnc string
		wantMux string
	}{
		{
			name:    "ac3 with a bitrate",
			track:   TrackInfo{Codec: CodecAC3, sampleRate: 48000, channels: 6, channelLayout: "5.1(side)", bitRate: 448000},
			wantOK:  true,
			wantEnc: "ac3",
			wantMux: "ac3",
		},
		{
			name:    "eac3 with a bitrate",
			track:   TrackInfo{Codec: CodecEAC3, sampleRate: 48000, channels: 6, bitRate: 640000},
			wantOK:  true,
			wantEnc: "eac3",
			wantMux: "eac3",
		},
		{
			name:    "dts needs -strict",
			track:   TrackInfo{Codec: CodecDTS, sampleRate: 48000, channels: 6, bitRate: 768000},
			wantOK:  true,
			wantEnc: "dca",
			wantMux: "dts",
		},
		{
			name:   "ac3 without a bitrate is refused",
			track:  TrackInfo{Codec: CodecAC3, sampleRate: 48000, channels: 6},
			wantOK: false,
		},
		{
			name:   "ac3 without a sample rate is refused",
			track:  TrackInfo{Codec: CodecAC3, channels: 6, bitRate: 448000},
			wantOK: false,
		},
		{
			name:   "flac has no silent form",
			track:  TrackInfo{Codec: CodecFLAC, sampleRate: 48000, channels: 2, bitRate: 900000},
			wantOK: false,
		},
		{
			name:   "opus has no silent form",
			track:  TrackInfo{Codec: CodecOPUS, sampleRate: 48000, channels: 2},
			wantOK: false,
		},
		{
			name:   "pgs is not audio",
			track:  TrackInfo{Codec: CodecPGS, sampleRate: 48000, channels: 2},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			track := tc.track
			spec, ok := padFor(&track, 1000)
			if ok != tc.wantOK {
				t.Fatalf("padFor() ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if spec.encoder != tc.wantEnc {
				t.Errorf("encoder = %q, want %q", spec.encoder, tc.wantEnc)
			}
			if spec.muxer != tc.wantMux {
				t.Errorf("muxer = %q, want %q", spec.muxer, tc.wantMux)
			}
			if spec.ms != 1000 {
				t.Errorf("ms = %d, want 1000", spec.ms)
			}
		})
	}
}

// TestPadArgs pins the pad command line, including the -strict the experimental
// dca encoder requires and the parameter echo that keeps the pad joinable.
func TestPadArgs(t *testing.T) {
	t.Parallel()
	spec := padSpec{
		encoder:       "ac3",
		muxer:         "ac3",
		sampleRate:    48000,
		channels:      6,
		channelLayout: "5.1(side)",
		bitRate:       448000,
		ms:            1000,
	}
	want := []string{
		"-y", "-nostdin", "-v", "warning",
		"-f", "lavfi",
		"-i", "anullsrc=r=48000:cl=5.1(side)",
		"-t", "1.000000",
		"-c:a", "ac3",
		"-b:a", "448000",
		"-ar", "48000",
		"-ac", "6",
		"-f", "ac3",
	}
	assertArgs(t, padArgs(spec), want)

	// The output path and the progress options are appended by withOutput, so
	// the destination is last.
	full := withOutput(padArgs(spec), "pad.ac3")
	if full[len(full)-1] != "pad.ac3" {
		t.Errorf("withOutput() = %v, want the destination last", full)
	}

	dts := padSpec{encoder: "dca", muxer: "dts", sampleRate: 48000, channels: 6, bitRate: 768000, ms: 500, strict: true}
	got := padArgs(dts)
	if !contains(got, "-strict") || !contains(got, "-2") {
		t.Errorf("padArgs(dts) = %v, want -strict -2", got)
	}
	if !contains(got, "0.500000") {
		t.Errorf("padArgs(dts) = %v, want the 0.5 s duration", got)
	}

	// A pad with no channel layout falls back to the channel count, which
	// ffmpeg accepts as a layout name.
	plain := padSpec{encoder: "ac3", muxer: "ac3", sampleRate: 48000, channels: 2, bitRate: 192000, ms: 100}
	if got := padArgs(plain); !contains(got, "anullsrc=r=48000:cl=2") {
		t.Errorf("padArgs(plain) = %v, want a channel-count layout", got)
	}
}

// --- extraction planning ---------------------------------------------------

// TestPlanExtraction pins the positional mapping between source tracks and the
// profile lists, the MuxOption.Skip handling and the Lossy downgrade. It also
// pins which ffmpeg shape each track gets.
func TestPlanExtraction(t *testing.T) {
	t.Parallel()

	newTracks := func() []*TrackInfo {
		return []*TrackInfo{
			{Codec: CodecH264AVC, Index: 2, Type: model.TrackTypeVideo, stream: 0},
			{Codec: CodecTrueHDAC3, Index: 3, Type: model.TrackTypeAudio, stream: 1, sampleRate: 48000},
			{Codec: CodecDTSMA, Index: 4, Type: model.TrackTypeAudio, stream: 2, sampleRate: 48000},
			{Codec: CodecAC3, Index: 5, Type: model.TrackTypeAudio, stream: 3, sampleRate: 48000, channels: 6, bitRate: 448000},
			{Codec: CodecEAC3, Index: 6, Type: model.TrackTypeAudio, stream: 4, sampleRate: 48000, channels: 6, bitRate: 640000},
			{Codec: CodecPGS, Index: 7, Type: model.TrackTypeSubtitle, stream: 5},
			{Codec: CodecVobSub, Index: 8, Type: model.TrackTypeSubtitle, stream: 6},
			{Codec: CodecChapter, Index: 1, Type: model.TrackTypeChapter},
		}
	}

	cases := []struct {
		name      string
		audio     []model.AudioInfo
		subs      []model.Info
		wantFiles []string
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
			wantFiles: []string{"00001_3.flac", "00001_4.flac", "00001_5.ac3", "00001_6.eac3", "00001_7.sup"},
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
			wantFiles: []string{"00001_3.flac", "00001_5.ac3", "00001_6.eac3", "00001_7.sup"},
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
			wantFiles: []string{"00001_3.flac", "00001_4.flac", "00001_5.flac", "00001_6.eac3", "00001_7.sup"},
			wantCodec: []TrackCodec{CodecFLAC, CodecFLAC, CodecFLAC, CodecEAC3, CodecPGS},
		},
		{
			name:      "vobsub and chapters are never extracted",
			audio:     []model.AudioInfo{{Info: model.NewInfo()}, {Info: model.NewInfo()}, {Info: model.NewInfo()}, {Info: model.NewInfo()}},
			subs:      []model.Info{model.NewInfo()},
			wantFiles: []string{"00001_3.flac", "00001_4.flac", "00001_5.ac3", "00001_6.eac3", "00001_7.sup"},
			wantCodec: []TrackCodec{CodecTrueHDAC3, CodecDTSMA, CodecAC3, CodecEAC3, CodecPGS},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tracks := newTracks()
			for _, tr := range tracks {
				tr.SourceFile = `D:\src\00001.m2ts`
				tr.WorkingPathPrefix = `D:\work\00001`
			}
			p := New(Options{})
			plan := p.planExtraction(tracks, tc.audio, tc.subs)
			if len(plan.tracks) != len(tc.wantCodec) {
				t.Fatalf("planned %d tracks, want %d", len(plan.tracks), len(tc.wantCodec))
			}
			for i, want := range tc.wantCodec {
				if plan.tracks[i].Codec != want {
					t.Errorf("track[%d].Codec = %s, want %s", i, plan.tracks[i].Codec, want)
				}
				got := filepath.Base(plan.tracks[i].OutFileName())
				if got != tc.wantFiles[i] {
					t.Errorf("track[%d] output = %q, want %q", i, got, tc.wantFiles[i])
				}
				if len(plan.jobs[i].steps) == 0 {
					t.Errorf("track[%d] has no steps", i)
				}
			}
		})
	}
}

// TestBuildExtractArgsShapes pins the four command-line shapes the plan can
// produce. The delay decides which one a track gets.
func TestBuildExtractArgsShapes(t *testing.T) {
	t.Parallel()
	base := func() *TrackInfo {
		return &TrackInfo{
			SourceFile:        `D:\src\00001.m2ts`,
			WorkingPathPrefix: `D:\work\00001`,
		}
	}

	t.Run("copy, no delay", func(t *testing.T) {
		t.Parallel()
		track := base()
		track.Codec = CodecAC3
		track.Index = 5
		track.Type = model.TrackTypeAudio
		track.stream = 3
		p := New(Options{SourceFile: track.SourceFile})
		steps := p.buildExtractArgs(track)
		if len(steps) != 1 {
			t.Fatalf("steps = %d, want 1", len(steps))
		}
		assertArgs(t, steps[0].args, []string{
			"-y", "-nostdin", "-v", "warning",
			"-i", `D:\src\00001.m2ts`,
			"-map", "0:3",
			"-c", "copy",
			"-f", "ac3",
			"-progress", "pipe:1", "-nostats",
			track.OutFileName(),
		})
	})
	t.Run("copy, positive delay needs a pad and a join", func(t *testing.T) {
		t.Parallel()
		track := base()
		track.Codec = CodecAC3
		track.Index = 5
		track.Type = model.TrackTypeAudio
		track.stream = 3
		track.delay = 1000
		track.sampleRate = 48000
		track.channels = 6
		track.bitRate = 448000
		p := New(Options{SourceFile: track.SourceFile})
		steps := p.buildExtractArgs(track)
		if len(steps) != 3 {
			t.Fatalf("steps = %d, want 3 (body, pad, join)", len(steps))
		}
		out := track.OutFileName()
		// 1: body, a stream copy of the source.
		if !contains(steps[0].args, "copy") || !contains(steps[0].args, out+".body") {
			t.Errorf("step 0 = %v, want a stream copy to the body file", steps[0].args)
		}
		// 2: pad, generated from lavfi.
		if !contains(steps[1].args, "lavfi") || !contains(steps[1].args, out+".pad") {
			t.Errorf("step 1 = %v, want a generated pad", steps[1].args)
		}
		if !contains(steps[1].args, "448000") {
			t.Errorf("step 1 = %v, want the body's bitrate echoed", steps[1].args)
		}
		// 3: join, via the concat demuxer.
		if steps[2].list == nil {
			t.Fatal("step 2 has no concat list")
		}
		if steps[2].list.path != out+".concat" {
			t.Errorf("list path = %q, want %q", steps[2].list.path, out+".concat")
		}
		if len(steps[2].list.entries) != 2 {
			t.Fatalf("list entries = %v, want the pad and the body", steps[2].list.entries)
		}
		if steps[2].list.entries[0] != out+".pad" || steps[2].list.entries[1] != out+".body" {
			t.Errorf("list entries = %v, want [pad body]", steps[2].list.entries)
		}
	})

	t.Run("copy, negative delay trims with output -ss", func(t *testing.T) {
		t.Parallel()
		track := base()
		track.Codec = CodecAC3
		track.Index = 5
		track.Type = model.TrackTypeAudio
		track.stream = 3
		track.delay = -1500
		p := New(Options{SourceFile: track.SourceFile})
		steps := p.buildExtractArgs(track)
		if len(steps) != 1 {
			t.Fatalf("steps = %d, want 1", len(steps))
		}
		args := steps[0].args
		if !contains(args, "-ss") || !contains(args, "1.500000") {
			t.Errorf("args = %v, want -ss 1.500000", args)
		}
		// -ss must come after -i: an input-side seek followed the video's
		// start rather than the audio's when measured.
		if indexOf(args, "-ss") < indexOf(args, "-i") {
			t.Errorf("args = %v, want -ss after -i", args)
		}
	})

	t.Run("decoded, positive delay uses adelay", func(t *testing.T) {
		t.Parallel()
		track := base()
		track.Codec = CodecTrueHDAC3
		track.Index = 3
		track.Type = model.TrackTypeAudio
		track.stream = 1
		track.delay = 167
		track.sampleRate = 48000
		p := New(Options{SourceFile: track.SourceFile})
		steps := p.buildExtractArgs(track)
		if len(steps) != 1 {
			t.Fatalf("steps = %d, want 1", len(steps))
		}
		args := steps[0].args
		if !contains(args, "-c:a") || !contains(args, "flac") {
			t.Errorf("args = %v, want -c:a flac", args)
		}
		if !contains(args, "adelay=8016S:all=1") {
			t.Errorf("args = %v, want the sample-exact adelay", args)
		}
		if !contains(args, "-f") || !contains(args, "flac") {
			t.Errorf("args = %v, want -f flac", args)
		}
	})

	t.Run("decoded, negative delay uses atrim", func(t *testing.T) {
		t.Parallel()
		track := base()
		track.Codec = CodecDTSMA
		track.Index = 4
		track.Type = model.TrackTypeAudio
		track.stream = 2
		track.delay = -167
		track.sampleRate = 48000
		p := New(Options{SourceFile: track.SourceFile})
		steps := p.buildExtractArgs(track)
		if !contains(steps[0].args, "atrim=start_sample=8016,asetpts=PTS-STARTPTS") {
			t.Errorf("args = %v, want the sample-exact atrim", steps[0].args)
		}
	})

	t.Run("subtitle is always a plain copy", func(t *testing.T) {
		t.Parallel()
		track := base()
		track.Codec = CodecPGS
		track.Index = 7
		track.Type = model.TrackTypeSubtitle
		track.stream = 5
		p := New(Options{SourceFile: track.SourceFile})
		steps := p.buildExtractArgs(track)
		if len(steps) != 1 {
			t.Fatalf("steps = %d, want 1", len(steps))
		}
		assertArgs(t, steps[0].args, []string{
			"-y", "-nostdin", "-v", "warning",
			"-i", `D:\src\00001.m2ts`,
			"-map", "0:5",
			"-c:s", "copy",
			"-f", "sup",
			"-progress", "pipe:1", "-nostats",
			track.OutFileName(),
		})
	})

	t.Run("a lossy ac3 with no bitrate falls back to decoding", func(t *testing.T) {
		t.Parallel()
		track := base()
		track.Codec = CodecAC3
		track.Index = 5
		track.Type = model.TrackTypeAudio
		track.stream = 3
		track.delay = 1000
		track.sampleRate = 48000
		track.channels = 6
		// No bitRate: the pad cannot be made to match, so the decoded path is
		// the only correct answer.
		p := New(Options{SourceFile: track.SourceFile})
		steps := p.buildExtractArgs(track)
		if len(steps) != 1 {
			t.Fatalf("steps = %d, want 1 (the decoded fallback)", len(steps))
		}
		if !contains(steps[0].args, "-c:a") || !contains(steps[0].args, "ac3") {
			t.Errorf("args = %v, want -c:a ac3", steps[0].args)
		}
		if !contains(steps[0].args, "adelay=48000S:all=1") {
			t.Errorf("args = %v, want adelay", steps[0].args)
		}
	})
}

// TestConcatListEscaping pins the two escaping rules the concat demuxer needs:
// forward slashes (each entry is a URL, so a backslash is not a separator) and
// the single-quote form '\” (without it a path containing an apostrophe
// truncates the list).
func TestConcatListEscaping(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	list := filepath.Join(dir, "l.txt")
	entries := []string{
		filepath.Join(dir, "plain.ac3"),
		filepath.Join(dir, "it's.ac3"),
	}
	if err := writeConcatList(list, entries); err != nil {
		t.Fatalf("writeConcatList() error = %v", err)
	}
	b, err := os.ReadFile(list)
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.ToSlash(entries[0])
	quoted := filepath.ToSlash(entries[1])
	// The apostrophe is escaped the way ffmpeg's own parser expects.
	quoted = strings.ReplaceAll(quoted, "'", `'\''`)
	want := "file '" + plain + "'\nfile '" + quoted + "'\n"
	if got := string(b); got != want {
		t.Errorf("list = %q, want %q", got, want)
	}
}

// TestCopyable pins which codecs the copy path can carry unchanged. A codec
// whose native container differs from the requested extension must be decoded:
// TrueHD and DTS-HD become FLAC, PCM becomes FLAC.
func TestCopyable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		codec TrackCodec
		want  bool
	}{
		{CodecAC3, true},
		{CodecEAC3, true},
		{CodecDTS, true},
		{CodecFLAC, true},
		{CodecAAC, true},
		{CodecOPUS, true},
		{CodecPGS, true},
		{CodecASS, true},
		{CodecSRT, true},
		// No native container, or the requested one differs.
		{CodecTrueHDAC3, false},
		{CodecDTSMA, false},
		{CodecRAWPCM, false},
		{CodecMPEG2, false},
		{CodecH264AVC, false},
		{CodecVobSub, false},
		{CodecChapter, false},
		{CodecUnknown, false},
	}
	for _, tc := range cases {
		t.Run(tc.codec.String(), func(t *testing.T) {
			t.Parallel()
			track := &TrackInfo{Codec: tc.codec}
			if got := track.copyable(); got != tc.want {
				t.Errorf("copyable() = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- ffprobe parsing -------------------------------------------------------

func TestParseProbeErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"whitespace", "   \n\t "},
		{"not json", "this is not json"},
		{"truncated", `{"streams":[{"index":0`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseProbe(tc.raw); err == nil {
				t.Fatal("ParseProbe() = nil, want an error")
			}
		})
	}
}

// TestParseProbeToleratesStringOrNumber pins the coercion: ffprobe prints
// sample_rate and bit_rate as strings, but duration has been a number in some
// releases, and both spellings appear in the wild.
func TestParseProbeToleratesStringOrNumber(t *testing.T) {
	t.Parallel()
	raw := `{
		"streams":[
			{"index":0,"codec_name":"ac3","codec_type":"audio","sample_rate":"48000","bit_rate":"448000","start_time":"0.000000"},
			{"index":1,"codec_name":"ac3","codec_type":"audio","sample_rate":44100,"bit_rate":192000,"start_time":1.5}
		],
		"format":{"duration":"6.007000","start_time":"0.000000","format_name":"matroska,webm"}
	}`
	probe, err := ParseProbe(raw)
	if err != nil {
		t.Fatalf("ParseProbe() error = %v", err)
	}
	if got := probe.Length(); got != 6 {
		t.Errorf("Length() = %d, want 6", got)
	}
	if got := probe.Streams[0].sampleRateValue(); got != 48000 {
		t.Errorf("sampleRateValue() = %d, want 48000", got)
	}
	if got := probe.Streams[1].sampleRateValue(); got != 44100 {
		t.Errorf("sampleRateValue() = %d, want 44100", got)
	}
	if got := probe.Streams[1].startTime(); got != 1.5 {
		t.Errorf("startTime() = %v, want 1.5", got)
	}
	if got := probe.Streams[0].bitRateValue(); got != 448000 {
		t.Errorf("bitRateValue() = %d, want 448000", got)
	}
}

// TestParseProbeMissingFields pins that a sparse report degrades to zero rather
// than failing: ffprobe omits duration for a raw elementary stream and start_time
// for some containers.
func TestParseProbeMissingFields(t *testing.T) {
	t.Parallel()
	raw := `{"streams":[{"index":0,"codec_name":"ac3","codec_type":"audio"}],"format":{}}`
	probe, err := ParseProbe(raw)
	if err != nil {
		t.Fatalf("ParseProbe() error = %v", err)
	}
	if got := probe.Length(); got != 0 {
		t.Errorf("Length() = %d, want 0", got)
	}
	s := probe.Streams[0]
	if got := s.sampleRateValue(); got != 0 {
		t.Errorf("sampleRateValue() = %d, want 0", got)
	}
	if got := s.startTime(); got != 0 {
		t.Errorf("startTime() = %v, want 0", got)
	}
	if got := s.bitRateValue(); got != 0 {
		t.Errorf("bitRateValue() = %d, want 0", got)
	}
}

// TestProbeArgs pins the analysis command line. -show_chapters is what makes the
// chapter entry appear, which is what lines the track numbering up with eac3to's.
func TestProbeArgs(t *testing.T) {
	t.Parallel()
	got := ProbeArgs(`D:\src\00001.m2ts`)
	assertArgs(t, got, []string{
		"-v", "error",
		"-show_streams",
		"-show_chapters",
		"-of", "json",
		`D:\src\00001.m2ts`,
	})
}

// TestAttachedPictureIsSkipped pins that a cover art stream is not a track.
func TestAttachedPictureIsSkipped(t *testing.T) {
	t.Parallel()
	raw := `{"streams":[
		{"index":0,"codec_name":"mjpeg","codec_type":"video","disposition":{"attached_pic":1}},
		{"index":1,"codec_name":"ac3","codec_type":"audio"}
	]}`
	probe, err := ParseProbe(raw)
	if err != nil {
		t.Fatalf("ParseProbe() error = %v", err)
	}
	tracks, err := probe.Tracks(Options{SourceFile: "a.mkv"})
	if err != nil {
		t.Fatalf("Tracks() error = %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("parsed %d tracks, want 1", len(tracks))
	}
	if tracks[0].Codec != CodecAC3 {
		t.Errorf("track = %s, want AC3", tracks[0].Codec)
	}
	if tracks[0].Index != 1 {
		t.Errorf("Index = %d, want 1", tracks[0].Index)
	}
}

// TestTrueHDDependentAC3IsFolded pins the MPEG-TS demuxer's split of an HDMV
// TrueHD PID. ffmpeg reports the TrueHD stream and a synthetic AC-3 view of the
// compatibility core, both with the PID as their id; eac3to lists that PID once.
func TestTrueHDDependentAC3IsFolded(t *testing.T) {
	t.Parallel()
	probe := parseFixture(t, "m2ts_truehd_dts_ac3.json")
	tracks, err := probe.Tracks(Options{SourceFile: "a.m2ts"})
	if err != nil {
		t.Fatalf("Tracks() error = %v", err)
	}
	var audio []*TrackInfo
	for _, tr := range tracks {
		if tr.Type == model.TrackTypeAudio {
			audio = append(audio, tr)
		}
	}
	if len(audio) != 3 {
		t.Fatalf("parsed %d audio tracks, want 3 (TrueHD, DTS, AC3): %v", len(audio), audio)
	}
	if audio[0].Codec != CodecTrueHDAC3 {
		t.Errorf("first audio track = %s, want TRUEHD_AC3", audio[0].Codec)
	}
	// The dependent AC-3 view must be gone, so the DTS track takes index 3.
	if audio[1].Codec != CodecDTS || audio[1].Index != 3 {
		t.Errorf("second audio track = %s at index %d, want DTS at 3", audio[1].Codec, audio[1].Index)
	}
	if audio[2].Codec != CodecAC3 || audio[2].Index != 4 {
		t.Errorf("third audio track = %s at index %d, want AC3 at 4", audio[2].Codec, audio[2].Index)
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

// --- progress --------------------------------------------------------------

func TestProgressHandler(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		duration float64
		lines    []string
		want     []float64
	}{
		{
			name:     "a full run reports its position",
			duration: 10,
			lines: []string{
				"bitrate=N/A",
				"out_time_us=0",
				"progress=continue",
				"out_time_us=5000000",
				"progress=continue",
				"out_time_us=10000000",
				"progress=end",
			},
			want: []float64{0, 50, 100, 100},
		},
		{
			name:     "no duration means no progress",
			duration: 0,
			lines:    []string{"out_time_us=5000000", "progress=continue"},
			want:     nil,
		},
		{
			name:     "a position past the end is clamped",
			duration: 10,
			lines:    []string{"out_time_us=20000000", "progress=continue"},
			want:     []float64{100},
		},
		{
			name:     "unrelated keys are ignored",
			duration: 10,
			lines:    []string{"frame=1", "fps=0.00", "speed=1x", "not a pair"},
			want:     nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &recordingSink{}
			p := New(Options{})
			handler := p.progressHandler(sink, 0, 100, tc.duration, true)
			if handler == nil {
				t.Fatal("progressHandler() = nil, want a handler")
			}
			for _, line := range tc.lines {
				if err := handler(line); err != nil {
					t.Fatalf("handler(%q) error = %v", line, err)
				}
			}
			if len(sink.percents) != len(tc.want) {
				t.Fatalf("percents = %v, want %v", sink.percents, tc.want)
			}
			for i, want := range tc.want {
				if sink.percents[i] != want {
					t.Errorf("percents[%d] = %v, want %v", i, sink.percents[i], want)
				}
			}
		})
	}

	t.Run("a disabled handler reports nothing", func(t *testing.T) {
		t.Parallel()
		p := New(Options{})
		if got := p.progressHandler(&recordingSink{}, 0, 100, 10, false); got != nil {
			t.Error("progressHandler(enabled=false) != nil")
		}
	})
}

// --- end to end ------------------------------------------------------------

// TestRunEndToEnd drives both passes against a fake ffmpeg and ffprobe. The test
// binary re-executes itself as each helper: the ffprobe stand-in prints a real
// report, the ffmpeg stand-in writes the files the plan asked for. This exercises
// the argument shape, the concat list, the empty/duplicate sweep and the
// MediaFile assembly without needing either binary.
func TestRunEndToEnd(t *testing.T) {
	// Serial: helperSpec sets environment variables for the child, which
	// t.Parallel would forbid.
	dir := t.TempDir()
	source := filepath.Join(dir, "00001.m2ts")
	if err := os.WriteFile(source, []byte("not really a transport stream"), 0o600); err != nil {
		t.Fatal(err)
	}
	prefix := filepath.Join(dir, "00001")

	report := readFixture(t, "m2ts_truehd_dts_ac3.json")
	sizes := map[string]int{
		"00001_2.flac": 4 << 20, // 4 MiB of audio: not empty
		"00001_3.dts":  4 << 20,
		"00001_4.ac3":  1 << 20,
	}
	spec := helperSpec(t, report, sizes, "")

	p := New(Options{
		FFmpeg:            spec.Path,
		FFprobe:           spec.Path,
		SourceFile:        source,
		WorkingPathPrefix: prefix,
		AudioTracks: []model.AudioInfo{
			{Info: model.NewInfo()},
			{Info: model.NewInfo()},
			{Info: model.NewInfo()},
		},
		Volume: VolumeFunc(func(_ context.Context, file string) (float64, float64, error) {
			// Track 2 is silent, tracks 3 and 4 are loud but distinct.
			switch {
			case strings.Contains(file, "_2."):
				return -80, -40, nil
			case strings.Contains(file, "_3."):
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

	if res.Length != 6 {
		t.Errorf("Length = %d, want 6", res.Length)
	}
	if len(res.Tracks) != 4 {
		t.Errorf("parsed %d tracks, want 4", len(res.Tracks))
	}
	if len(res.Extracted) != 3 {
		t.Fatalf("extracted %d tracks, want 3", len(res.Extracted))
	}

	// Track 2 was silent: its file is moved aside and it is not muxed.
	if !res.Extracted[0].DupOrEmpty {
		t.Error("track 2 was not flagged as empty")
	}
	if _, err := os.Stat(filepath.Join(dir, "00001_2.bak.flac")); err != nil {
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
	if media.Video != nil {
		t.Error("MediaFile gained a video track; the video comes from the .vpy script")
	}
	if media.Chapter != nil {
		t.Error("MediaFile gained a chapter track; chapters come from ChapterService")
	}

	for i, a := range media.AudioTracks {
		if a.Audio.Length != 6 {
			t.Errorf("audio[%d].Audio.Length = %d, want 6", i, a.Audio.Length)
		}
	}

	if res.LogPath != "" {
		t.Errorf("LogPath = %q, want empty: ffmpeg writes no demuxer log", res.LogPath)
	}

	// No byproduct of the copy path may survive.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".body") || strings.HasSuffix(e.Name(), ".pad") ||
			strings.HasSuffix(e.Name(), ".concat") {
			t.Errorf("stray intermediate left behind: %s", e.Name())
		}
	}

	if got := sink.lastPercent(); got != 100 {
		t.Errorf("last reported percent = %v, want 100", got)
	}
	if sink.statuses[StatusExtracted] == 0 {
		t.Error("the completion status was never reported")
	}
	if sink.statuses[StatusAnalyze] == 0 {
		t.Error("the analysis status was never reported")
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
	report := strings.Join([]string{
		`{"streams":[`,
		`{"index":0,"codec_name":"h264","codec_type":"video","start_time":"0.000000"},`,
		`{"index":1,"codec_name":"ac3","codec_type":"audio","sample_rate":"48000","bit_rate":"640000","channels":6,"start_time":"0.000000"},`,
		`{"index":2,"codec_name":"ac3","codec_type":"audio","sample_rate":"48000","bit_rate":"640000","channels":6,"start_time":"0.000000"},`,
		`{"index":3,"codec_name":"ac3","codec_type":"audio","sample_rate":"48000","bit_rate":"192000","channels":2,"start_time":"0.000000"}`,
		`],"format":{"duration":"600.000000"}}`,
	}, "\n")
	sizes := map[string]int{"00001_2.ac3": 1 << 20, "00001_3.ac3": 1 << 20, "00001_4.ac3": 1 << 20}

	p := New(Options{
		FFmpeg:            helperSpec(t, report, sizes, "").Path,
		FFprobe:           os.Args[0],
		SourceFile:        source,
		WorkingPathPrefix: filepath.Join(dir, "00001"),
		AudioTracks: []model.AudioInfo{
			{Info: model.NewInfo()},
			{Info: model.NewInfo()},
			{Info: model.NewInfo()},
		},
		Volume: VolumeFunc(func(_ context.Context, file string) (float64, float64, error) {
			if strings.Contains(file, "_4.") {
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
	if _, err := os.Stat(filepath.Join(dir, "00001_3.bak.ac3")); err != nil {
		t.Errorf("duplicate was not backed up: %v", err)
	}
}

// TestRunExitCodeIsReported pins the okerr.ErrEac3to path and the exit code
// carried on the error. The summary is shared with the eac3to demuxer so that
// okerr.Render produces the same operator-facing template either way.
func TestRunExitCodeIsReported(t *testing.T) {
	// Serial: helperSpec sets environment variables for the child, which
	// t.Parallel would forbid.
	dir := t.TempDir()
	source := filepath.Join(dir, "00001.m2ts")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec := helperSpec(t, "", nil, "3")
	p := New(Options{
		FFmpeg:            spec.Path,
		FFprobe:           spec.Path,
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

// TestRunMissingSource pins the early return when the input file does not exist.
func TestRunMissingSource(t *testing.T) {
	t.Parallel()
	p := New(Options{
		FFmpeg:     os.Args[0],
		FFprobe:    os.Args[0],
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

// TestRunMissingTools pins the early configuration errors.
func TestRunMissingTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	source := filepath.Join(dir, "a.m2ts")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		opts Options
	}{
		{"no ffmpeg", Options{FFprobe: os.Args[0], SourceFile: source}},
		{"no ffprobe", Options{FFmpeg: os.Args[0], SourceFile: source}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := New(tc.opts)
			err := p.Run(helperCtx(t), nil)
			if err == nil {
				t.Fatal("Run() = nil, want an error")
			}
			if got := okerr.AsError(err).Kind; got != okerr.KindConfig {
				t.Errorf("kind = %q, want %q", got, okerr.KindConfig)
			}
		})
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
	report := readFixture(t, "mkv_flac_opus_ac3_ass.json")
	sizes := map[string]int{"00001_2.flac": 1 << 20, "00001_3.opus": 1 << 20, "00001_4.ac3": 1 << 20, "00001_5.ass": 1 << 20}

	p := New(Options{
		FFmpeg:                helperSpec(t, report, sizes, "").Path,
		FFprobe:               os.Args[0],
		SourceFile:            source,
		WorkingPathPrefix:     filepath.Join(dir, "00001"),
		SkipAllAudioTracks:    true,
		SkipAllSubtitleTracks: true,
		Volume: VolumeFunc(func(context.Context, string) (float64, float64, error) {
			return -20, -1, nil
		}),
	})
	if err := p.Run(helperCtx(t), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	res := p.Result()
	// Only the chapter and video tracks survive the filters.
	if len(res.Tracks) != 2 {
		t.Errorf("kept %d tracks, want 2: %v", len(res.Tracks), res.Tracks)
	}
	if len(res.Extracted) != 0 {
		t.Errorf("extracted %d tracks, want 0", len(res.Extracted))
	}
}

// TestRunSurvivesAConcatJob drives the copy-with-pad path end to end: the fake
// ffmpeg records every argument list it was handed, so the three invocations and
// the concat list can be inspected.
func TestRunSurvivesAConcatJob(t *testing.T) {
	// Serial: helperSpec sets environment variables for the child, which
	// t.Parallel would forbid.
	dir := t.TempDir()
	source := filepath.Join(dir, "00001.m2ts")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := strings.Join([]string{
		`{"streams":[`,
		`{"index":0,"codec_name":"h264","codec_type":"video","start_time":"0.000000"},`,
		`{"index":1,"codec_name":"ac3","codec_type":"audio","sample_rate":"48000","bit_rate":"448000","channels":6,"channel_layout":"5.1(side)","start_time":"1.000000"}`,
		`],"format":{"duration":"600.000000"}}`,
	}, "\n")
	// The fake tool writes the file named by the last argument; for the concat
	// step that is the final output, which is what the demuxer stats.
	sizes := map[string]int{"00001_2.ac3": 1 << 20}
	argsLog := filepath.Join(dir, "args.log")
	spec := helperSpecArgs(t, report, sizes, argsLog, "", "")

	p := New(Options{
		FFmpeg:            spec.Path,
		FFprobe:           spec.Path,
		SourceFile:        source,
		WorkingPathPrefix: filepath.Join(dir, "00001"),
		AudioTracks:       []model.AudioInfo{{Info: model.NewInfo()}},
		Volume: VolumeFunc(func(context.Context, string) (float64, float64, error) {
			return -23.45, -1.02, nil
		}),
	})
	if err := p.Run(helperCtx(t), nil); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	raw, err := os.ReadFile(argsLog)
	if err != nil {
		t.Fatalf("read args log: %v", err)
	}
	// The probe run is logged too, so the three extraction invocations are the
	// lines that carry -i (the probe's is the report request).
	var runs []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.Contains(line, "-show_streams") {
			continue
		}
		runs = append(runs, line)
	}
	if len(runs) != 3 {
		t.Fatalf("ffmpeg ran %d extraction times, want 3 (body, pad, join):\n%s", len(runs), raw)
	}
	if !strings.Contains(runs[0], "00001_2.ac3.body") || !strings.Contains(runs[0], "copy") {
		t.Errorf("step 0 = %q, want a stream copy to the body", runs[0])
	}
	if !strings.Contains(runs[1], "anullsrc") || !strings.Contains(runs[1], "00001_2.ac3.pad") {
		t.Errorf("step 1 = %q, want a generated pad", runs[1])
	}
	if !strings.Contains(runs[1], "448000") {
		t.Errorf("step 1 = %q, want the body's bitrate echoed", runs[1])
	}
	if !strings.Contains(runs[2], "-f concat") || !strings.Contains(runs[2], "00001_2.ac3.concat") {
		t.Errorf("step 2 = %q, want a concat join", runs[2])
	}

	// The list must name the pad first and the body second. It is a byproduct
	// and therefore already removed, so the fake tool's argument log is what
	// proves the join read it; the final output proves the join ran.
	if _, err := os.Stat(filepath.Join(dir, "00001_2.ac3")); err != nil {
		t.Errorf("final output missing: %v", err)
	}
	for _, name := range []string{"00001_2.ac3.body", "00001_2.ac3.pad", "00001_2.ac3.concat"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("intermediate %s was not cleaned up (err = %v)", name, err)
		}
	}
}

// TestRunPropagatesExtractionFailure pins that a failing ffmpeg run on the
// extraction pass becomes a structured tool error naming ffmpeg.
func TestRunPropagatesExtractionFailure(t *testing.T) {
	// Serial: helperSpec sets environment variables for the child, which
	// t.Parallel would forbid.
	dir := t.TempDir()
	source := filepath.Join(dir, "00001.m2ts")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := strings.Join([]string{
		`{"streams":[`,
		`{"index":0,"codec_name":"h264","codec_type":"video","start_time":"0.000000"},`,
		`{"index":1,"codec_name":"flac","codec_type":"audio","sample_rate":"48000","start_time":"0.000000"},`,
		`{"index":2,"codec_name":"hdmv_pgs_subtitle","codec_type":"subtitle","start_time":"0.000000"}`,
		`],"format":{"duration":"600.000000"}}`,
	}, "\n")
	// The probe succeeds, then the extraction run exits non-zero.
	spec := helperSpecArgs(t, report, nil, "", "", "7")

	p := New(Options{
		FFmpeg:            spec.Path,
		FFprobe:           spec.Path,
		SourceFile:        source,
		WorkingPathPrefix: filepath.Join(dir, "00001"),
		AudioTracks:       []model.AudioInfo{{Info: model.NewInfo()}},
		SubtitleTracks:    []model.Info{model.NewInfo()},
		Volume: VolumeFunc(func(context.Context, string) (float64, float64, error) {
			return -23.45, -1.02, nil
		}),
	})
	err := p.Run(helperCtx(t), nil)
	if err == nil {
		t.Fatal("Run() = nil, want an error")
	}
	e := okerr.AsError(err)
	if e.Summary != okerr.ErrEac3to.Summary {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrEac3to.Summary)
	}
	if e.Tool != Name {
		t.Errorf("tool = %q, want %q", e.Tool, Name)
	}
}

// --- helpers ---------------------------------------------------------------

func parseFixture(t *testing.T, name string) *Probe {
	t.Helper()
	probe, err := ParseProbe(readFixture(t, name))
	if err != nil {
		t.Fatalf("ParseProbe(%s) error = %v", name, err)
	}
	return probe
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

// assertArgs compares two argument lists element by element so a failure names
// the position that differs.
func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("args[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func contains(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func indexOf(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

// helperCtx bounds every end-to-end test that drives a child process. Without a
// deadline, a bug in the demuxer's wait logic would leave the fake tool running
// forever; with one, the test fails instead of leaking a process.
func helperCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

const (
	helperEnvReport      = "OKEGUIDX_FFMPEG_REPORT"
	helperEnvSizes       = "OKEGUIDX_FFMPEG_SIZES"
	helperEnvArgsLog     = "OKEGUIDX_FFMPEG_ARGS_LOG"
	helperEnvFailAt      = "OKEGUIDX_FFMPEG_FAIL_AT"
	helperEnvExtractExit = "OKEGUIDX_FFMPEG_EXTRACT_EXIT"
)

// helperSpec builds a proc.Spec that re-executes the test binary as the fake
// ffmpeg and ffprobe, with no argument log.
//
// failAt makes the probe fail; extractExit makes the extraction fail after a
// successful probe. The two are separate because the demuxer has to react to
// each differently.
func helperSpec(t *testing.T, report string, sizes map[string]int, failAt string) proc.Spec {
	t.Helper()
	return helperSpecArgs(t, report, sizes, "", failAt, "")
}

// helperSpecArgs is helperSpec plus an argument log, which lets a test inspect
// every command line the demuxer built.
//
// The marker travels in the process environment rather than in the argument
// list, because the demuxer builds its own arguments and would overwrite them.
// TestMain reads the marker and turns the binary into the fake tool.
func helperSpecArgs(t *testing.T, report string, sizes map[string]int, argsLog, failAt, extractExit string) proc.Spec {
	t.Helper()
	// The fail-at case must not also set the report marker, or the child would
	// answer the probe instead of exiting with the requested status.
	if failAt != "" {
		t.Setenv(helperEnvFailAt, failAt)
	} else {
		t.Setenv(helperEnvReport, "1")
		if report != "" {
			path := filepath.Join(t.TempDir(), "report.json")
			if err := os.WriteFile(path, []byte(report), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(helperEnvReport, path)
		}
	}
	if len(sizes) > 0 {
		raw, err := json.Marshal(sizes)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(helperEnvSizes, string(raw))
	}
	if argsLog != "" {
		t.Setenv(helperEnvArgsLog, argsLog)
	}
	if extractExit != "" {
		t.Setenv(helperEnvExtractExit, extractExit)
	}
	return proc.Spec{
		Path: os.Args[0],
		Name: Name,
	}
}

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
	s.percents = append(s.percents, p.Percent)
}

func (s *recordingSink) lastPercent() float64 {
	if len(s.percents) == 0 {
		return 0
	}
	return s.percents[len(s.percents)-1]
}
