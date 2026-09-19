package ffmpeg

// This file is the cross-package half of the specification. A15 exists so the
// pipeline can pick a demuxer from node.Capabilities without knowing which one
// it got, which only works if the two packages agree on their public surface.
// The eac3to demuxer is Windows-only and imports this one's sibling; asserting
// the agreement here means a future edit to either side that breaks the switch
// fails in this package's test run.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// TestProcessorMethodSet pins the method set the engine switches on. It is the
// same set internal/jobproc/demux/eac3to.Processor implements.
func TestProcessorMethodSet(t *testing.T) {
	t.Parallel()
	p := New(Options{})
	var (
		_ jobproc.Processor     = p
		_ jobproc.Controllable  = p
		_ jobproc.Prioritizable = p
	)
	if p.Name() != "ffmpeg" {
		t.Errorf("Name() = %q, want %q", p.Name(), "ffmpeg")
	}
}

// TestOptionsMirrorsEac3to pins the field set and types of Options against the
// shape the pipeline fills in for the eac3to demuxer.
//
// The two structs cannot be identical: the eac3to one carries EacPath and this
// one carries FFmpeg, FFprobe and MkvExtract, because that is the whole point of
// having two implementations. Everything else — the source, the working prefix,
// the profile slices, the skip-all flags, the volume seam and the priority — has
// to match, or the pipeline cannot populate both from one code path.
func TestOptionsMirrorsEac3to(t *testing.T) {
	t.Parallel()
	want := []struct {
		name string
		typ  reflect.Type
	}{
		{"SourceFile", reflect.TypeOf("")},
		{"WorkingPathPrefix", reflect.TypeOf("")},
		{"AudioTracks", reflect.TypeOf([]model.AudioInfo(nil))},
		{"SubtitleTracks", reflect.TypeOf([]model.Info(nil))},
		{"SkipAllAudioTracks", reflect.TypeOf(false)},
		{"SkipAllSubtitleTracks", reflect.TypeOf(false)},
		{"Volume", reflect.TypeOf((*VolumeMeasurer)(nil)).Elem()},
		{"Priority", reflect.TypeOf(proc.Priority(0))},
	}
	typ := reflect.TypeOf(Options{})
	for _, w := range want {
		f, ok := typ.FieldByName(w.name)
		if !ok {
			t.Errorf("Options is missing %s, which the eac3to demuxer has", w.name)
			continue
		}
		if f.Type != w.typ {
			t.Errorf("Options.%s is %v, want %v", w.name, f.Type, w.typ)
		}
	}
}

// TestResultMirrorsEac3to pins the result field set and types.
func TestResultMirrorsEac3to(t *testing.T) {
	t.Parallel()
	want := []struct {
		name string
		typ  reflect.Type
	}{
		{"MediaFile", reflect.TypeOf((*model.MediaFile)(nil))},
		{"Tracks", reflect.TypeOf([]*TrackInfo(nil))},
		{"Extracted", reflect.TypeOf([]*TrackInfo(nil))},
		{"Length", reflect.TypeOf(0)},
		{"LogPath", reflect.TypeOf("")},
	}
	typ := reflect.TypeOf(Result{})
	for _, w := range want {
		f, ok := typ.FieldByName(w.name)
		if !ok {
			t.Errorf("Result is missing %s, which the eac3to demuxer has", w.name)
			continue
		}
		if f.Type != w.typ {
			t.Errorf("Result.%s is %v, want %v", w.name, f.Type, w.typ)
		}
	}
}

// TestVolumeMeasurerShape pins the seam. internal/jobproc/audio/volume.Checker
// satisfies both demuxers through a structural assertion; the signature is
// repeated here so an edit on this side cannot silently stop matching.
func TestVolumeMeasurerShape(t *testing.T) {
	t.Parallel()
	var _ interface {
		Measure(ctx context.Context, file string) (mean, max float64, err error)
	} = VolumeFunc(nil)

	var m VolumeMeasurer = VolumeFunc(func(context.Context, string) (float64, float64, error) {
		return -23.45, -1.02, nil
	})
	mean, max, err := m.Measure(context.Background(), "x.flac")
	if err != nil {
		t.Fatalf("Measure() error = %v", err)
	}
	if mean != -23.45 || max != -1.02 {
		t.Errorf("Measure() = (%v, %v), want (-23.45, -1.02)", mean, max)
	}
}

// TestTrackInfoMirrorsEac3to pins the fields the pipeline reads off a track. The
// eac3to demuxer's TrackInfo carries the same names and types; the private
// ffmpeg-specific fields (the stream index, the delay) are not part of that
// surface.
func TestTrackInfoMirrorsEac3to(t *testing.T) {
	t.Parallel()
	want := []struct {
		name string
		typ  reflect.Type
	}{
		{"Codec", reflect.TypeOf(TrackCodec(0))},
		{"Index", reflect.TypeOf(0)},
		{"Information", reflect.TypeOf("")},
		{"RawOutput", reflect.TypeOf("")},
		{"SourceFile", reflect.TypeOf("")},
		{"WorkingPathPrefix", reflect.TypeOf("")},
		{"Type", reflect.TypeOf(model.TrackType(0))},
		{"DupOrEmpty", reflect.TypeOf(false)},
		{"Length", reflect.TypeOf(0)},
		{"FileSize", reflect.TypeOf(int64(0))},
		{"MeanVolume", reflect.TypeOf(float64(0))},
		{"MaxVolume", reflect.TypeOf(float64(0))},
	}
	typ := reflect.TypeOf(TrackInfo{})
	for _, w := range want {
		f, ok := typ.FieldByName(w.name)
		if !ok {
			t.Errorf("TrackInfo is missing %s, which the eac3to demuxer has", w.name)
			continue
		}
		if f.Type != w.typ {
			t.Errorf("TrackInfo.%s is %v, want %v", w.name, f.Type, w.typ)
		}
	}
}

// TestCodecNamesMatchEac3to pins the codec vocabulary. The two demuxers report
// the same TrackCodec values, so a log line or a comparison written against one
// reads identically for the other.
func TestCodecNamesMatchEac3to(t *testing.T) {
	t.Parallel()
	// The names are the legacy enum's, reproduced by both packages.
	want := map[TrackCodec]string{
		CodecUnknown:   "Unknown",
		CodecMPEG2:     "MPEG2",
		CodecH264AVC:   "H264_AVC",
		CodecH265HEVC:  "H265_HEVC",
		CodecAV1:       "AV1",
		CodecRAWPCM:    "RAW_PCM",
		CodecFLAC:      "FLAC",
		CodecAAC:       "AAC",
		CodecDTSMA:     "DTSMA",
		CodecTrueHDAC3: "TRUEHD_AC3",
		CodecAC3:       "AC3",
		CodecDTS:       "DTS",
		CodecEAC3:      "EAC3",
		CodecOPUS:      "OPUS",
		CodecPGS:       "PGS",
		CodecChapter:   "Chapter",
		CodecVobSub:    "VobSub",
		CodecASS:       "ASS",
		CodecSRT:       "SRT",
	}
	for codec, name := range want {
		if got := codec.String(); got != name {
			t.Errorf("TrackCodec(%d).String() = %q, want %q", int(codec), got, name)
		}
	}
	// The declarations have to be in the same order too, because the values are
	// comparable across the two packages only if the ordinals agree.
	if CodecUnknown != 0 || CodecSRT != 18 {
		t.Errorf("codec ordinals moved: Unknown=%d SRT=%d, want 0 and 18",
			int(CodecUnknown), int(CodecSRT))
	}
}

// TestStatusStringsMatchEac3to pins the UI texts. The pipeline puts these on the
// task, and an operator must not be able to tell which demuxer ran from the
// status alone.
func TestStatusStringsMatchEac3to(t *testing.T) {
	t.Parallel()
	cases := []struct {
		got, want string
	}{
		{StatusAnalyze, "轨道分析中"},
		{StatusExtract, "抽取音轨中"},
		{StatusExtracted, "音轨已抽取"},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("status = %q, want %q", tc.got, tc.want)
		}
	}
}

// TestErrorSummaryMatchesEac3to pins the operator-facing error summary. Both
// demuxers report through okerr.ErrEac3to, so okerr.Render produces the same
// template whichever tool failed; the message is a property of the step, not of
// the binary behind it.
func TestErrorSummaryMatchesEac3to(t *testing.T) {
	// Serial: helperSpec sets environment variables for the child, which
	// t.Parallel would forbid.
	dir := t.TempDir()
	source := filepath.Join(dir, "a.m2ts")
	if err := os.WriteFile(source, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The tool path exists (the test binary) but the probe exits non-zero.
	spec := helperSpec(t, "", nil, "4")
	p := New(Options{
		FFmpeg:            spec.Path,
		FFprobe:           spec.Path,
		SourceFile:        source,
		WorkingPathPrefix: filepath.Join(dir, "w"),
	})
	err := p.Run(helperCtx(t), nil)
	if err == nil {
		t.Fatal("Run() = nil, want an error")
	}
	e := okerr.AsError(err)
	if e.Summary != okerr.ErrEac3to.Summary {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrEac3to.Summary)
	}
	if e.Kind != okerr.KindTool {
		t.Errorf("kind = %q, want %q", e.Kind, okerr.KindTool)
	}
	if !strings.Contains(okerr.Render(e), "退出代码4") {
		t.Errorf("Render() = %q, want the legacy template", okerr.Render(e))
	}
}
