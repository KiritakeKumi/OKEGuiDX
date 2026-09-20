package lsmash

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// The fixtures in testdata/ reproduce the exact line shapes muxer.c emits on
// stderr; see testdata/README.md for the mapping back to that source file. The
// acceptance criterion for this package is that the command line matches the
// legacy one argument for argument and that every progress and error line is
// recognised.

const (
	testMuxer    = `D:\tools\l-smash\muxer.exe`
	testOutput   = `D:\WORKS\out\ep01.mp4`
	testVideo    = `D:\WORKS\work\ep01.hevc`
	testAudioJpn = `D:\WORKS\work\ep01_jpn.aac`
	testAudioEng = `D:\WORKS\work\ep01_eng.ac3`
	testChapter  = `D:\WORKS\work\ep01.txt`
)

// audioTrack builds an audio track the way the pipeline does: language and name
// come from the profile, order defaults to "append at the end".
func audioTrack(path, language, name string) *model.AudioTrack {
	return &model.AudioTrack{Track: model.Track{
		File:      model.NewFileRef(path),
		Info:      model.Info{Language: language, Name: name, Order: model.MaxOrder},
		TrackType: model.TrackTypeAudio,
	}}
}

// audioTrackAt builds an audio track with an explicit order.
func audioTrackAt(path, language, name string, order int) *model.AudioTrack {
	a := audioTrack(path, language, name)
	a.Info.Order = order
	return a
}

func videoTrack(path string, num, den int64) *model.VideoTrack {
	return &model.VideoTrack{
		Track: model.Track{
			File:      model.NewFileRef(path),
			Info:      model.Info{Language: model.DefaultLanguage, Order: model.MaxOrder},
			TrackType: model.TrackTypeVideo,
		},
		Video: model.VideoInfo{FpsNum: num, FpsDen: den},
	}
}

func chapterTrack(path string) *model.ChapterTrack {
	return &model.ChapterTrack{Track: model.Track{
		File:      model.NewFileRef(path),
		Info:      model.Info{Language: "jpn", Order: model.MaxOrder},
		TrackType: model.TrackTypeChapter,
	}}
}

// rootsLocal mirrors the standalone node's volume map, where the local volume
// root is the filesystem root, so a resolved path is the one the user typed.
var rootsLocal = map[string]string{model.LocalVolume: ""}

// resolveLocal mirrors FileRef.Resolve on a standalone node, so the
// expectations read like the command line the legacy code produced.
func resolveLocal(path string) string { return model.NewFileRef(path).Resolve(rootsLocal) }

func TestBuildArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		opts Options
		want []string
	}{
		{
			name: "single segment with one audio track",
			opts: Options{
				Muxer:  testMuxer,
				Output: testOutput,
				Video:  &TrackSource{File: testVideo, Options: []string{"fps=24000/1001"}},
				Audio:  []TrackSource{{File: testAudioJpn, Options: []string{"language=jpn", "handler="}}},
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", testVideo + "?fps=24000/1001",
				"-i", testAudioJpn + "?language=jpn,handler=",
			},
		},
		{
			name: "video only, no track options",
			opts: Options{Muxer: "muxer", Output: testOutput, Video: &TrackSource{File: testVideo}},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", testVideo,
			},
		},
		{
			name: "multiple audio tracks keep the given order",
			opts: Options{
				Muxer:  "muxer",
				Output: testOutput,
				Video:  &TrackSource{File: testVideo, Options: []string{"fps=24000/1001"}},
				Audio: []TrackSource{
					{File: testAudioJpn, Options: []string{"language=jpn", "handler=Main"}},
					{File: testAudioEng, Options: []string{"language=eng", "handler=Commentary"}},
				},
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", testVideo + "?fps=24000/1001",
				"-i", testAudioJpn + "?language=jpn,handler=Main",
				"-i", testAudioEng + "?language=eng,handler=Commentary",
			},
		},
		{
			name: "chapter file becomes a global option",
			opts: Options{
				Muxer:       "muxer",
				Output:      testOutput,
				Video:       &TrackSource{File: testVideo, Options: []string{"fps=24000/1001"}},
				Audio:       []TrackSource{{File: testAudioJpn, Options: []string{"language=jpn", "handler="}}},
				ChapterFile: testChapter,
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", testVideo + "?fps=24000/1001",
				"-i", testAudioJpn + "?language=jpn,handler=",
				"--chapter", testChapter,
			},
		},
		{
			name: "m4a episode, no video track",
			opts: Options{
				Muxer:  "muxer",
				Output: `D:\WORKS\out\ep01.m4a`,
				Audio:  []TrackSource{{File: `D:\WORKS\work\ep01.m4a`, Options: []string{"language=jpn", "handler="}}},
			},
			want: []string{
				"--file-format", "mp4",
				"-o", `D:\WORKS\out\ep01.m4a`,
				"-i", `D:\WORKS\work\ep01.m4a?language=jpn,handler=`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertArgs(t, BuildArgs(tc.opts), tc.want)
		})
	}
}

// TestEpisodeArgsAreIndependentOfTheBuilder hardcodes the complete command line
// instead of resolving the inputs through EpisodeOptions the way TestBuildArgs
// does. The tokens are transcribed from the legacy
// NewMp4EpisodeMuxer.BuildCommandline
// (JobProcessor/Muxer/NewMp4EpisodeMuxer.cs): the global options come first,
// then one -i per input in mux order -- video with its fps= option, then audio
// ordered by Info.Order with language= and handler= always present -- and the
// chapter file last.
//
// TestEpisodeOptionsSelectsAndOrdersTracks spells its paths with resolveLocal,
// which is NewFileRef().Resolve: the expected argv and the argv under test share
// the code path, so a regression in FileRef.Resolve leaves that test green. This
// test carries the paths as literals with real drive letters, so only the builder
// can change them.
func TestEpisodeArgsAreIndependentOfTheBuilder(t *testing.T) {
	t.Parallel()

	media := &model.MediaFile{
		Video: videoTrack(`V:\media\ep01\v.hevc`, 24000, 1001),
		AudioTracks: []*model.AudioTrack{
			// The unsupported .flac and .wav inputs are dropped, the .aac one
			// survives, and Info.Order puts it before the slice position of
			// the dropped entries.
			audioTrackAt(`V:\media\ep01\a1.flac`, "jpn", "Main", 1),
			audioTrackAt(`V:\media\ep01\a2.aac`, "eng", "Commentary", 2),
			audioTrack(`V:\media\ep01\a3.wav`, "jpn", ""),
		},
		Chapter: chapterTrack(`V:\media\ep01\ep01.txt`),
	}
	got := BuildArgs(EpisodeOptions(`C:\tools\l-smash\muxer.exe`, `V:\out\ep01.mp4`, media, 0, rootsLocal))
	want := []string{
		"--file-format", "mp4",
		"-o", `V:\out\ep01.mp4`,
		"-i", `V:\media\ep01\v.hevc?fps=24000/1001`,
		"-i", `V:\media\ep01\a2.aac?language=eng,handler=Commentary`,
		"--chapter", `V:\media\ep01\ep01.txt`,
	}
	assertArgs(t, got, want)
}

func TestEpisodeOptionsSelectsAndOrdersTracks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		media *model.MediaFile
		want  []string
	}{
		{
			name: "single segment with one audio track",
			media: &model.MediaFile{
				Video:       videoTrack(testVideo, 24000, 1001),
				AudioTracks: []*model.AudioTrack{audioTrack(testAudioJpn, "jpn", "")},
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", resolveLocal(testVideo) + "?fps=24000/1001",
				"-i", resolveLocal(testAudioJpn) + "?language=jpn,handler=",
			},
		},
		{
			name: "multiple audio tracks are ordered by Info.Order",
			media: func() *model.MediaFile {
				// The slice order is the reverse of the Info.Order values, so
				// the output proves the ordering was applied.
				return &model.MediaFile{
					Video: videoTrack(testVideo, 24000, 1001),
					AudioTracks: []*model.AudioTrack{
						audioTrackAt(testAudioEng, "eng", "Commentary", 1),
						audioTrackAt(testAudioJpn, "jpn", "Main", 2),
					},
				}
			}(),
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", resolveLocal(testVideo) + "?fps=24000/1001",
				"-i", resolveLocal(testAudioEng) + "?language=eng,handler=Commentary",
				"-i", resolveLocal(testAudioJpn) + "?language=jpn,handler=Main",
			},
		},
		{
			name: "equal orders keep the slice order",
			media: &model.MediaFile{
				Video: videoTrack(testVideo, 24000, 1001),
				AudioTracks: []*model.AudioTrack{
					audioTrackAt(testAudioJpn, "jpn", "Main", 1),
					audioTrackAt(testAudioEng, "eng", "Commentary", 1),
				},
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", resolveLocal(testVideo) + "?fps=24000/1001",
				"-i", resolveLocal(testAudioJpn) + "?language=jpn,handler=Main",
				"-i", resolveLocal(testAudioEng) + "?language=eng,handler=Commentary",
			},
		},
		{
			name: "episode with chapters",
			media: &model.MediaFile{
				Video:       videoTrack(testVideo, 24000, 1001),
				AudioTracks: []*model.AudioTrack{audioTrack(testAudioJpn, "jpn", "")},
				Chapter:     chapterTrack(testChapter),
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", resolveLocal(testVideo) + "?fps=24000/1001",
				"-i", resolveLocal(testAudioJpn) + "?language=jpn,handler=",
				"--chapter", resolveLocal(testChapter),
			},
		},
		{
			name: "subtitle tracks are not passed to l-smash",
			media: &model.MediaFile{
				Video:       videoTrack(testVideo, 24000, 1001),
				AudioTracks: []*model.AudioTrack{audioTrack(testAudioJpn, "jpn", "")},
				SubtitleTracks: []*model.SubtitleTrack{
					{Track: model.Track{
						File:      model.NewFileRef(`D:\WORKS\work\ep01.sup`),
						Info:      model.Info{Language: "jpn"},
						TrackType: model.TrackTypeSubtitle,
					}},
				},
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", resolveLocal(testVideo) + "?fps=24000/1001",
				"-i", resolveLocal(testAudioJpn) + "?language=jpn,handler=",
			},
		},
		{
			name: "audio with an unsupported extension is skipped",
			media: &model.MediaFile{
				Video: videoTrack(testVideo, 24000, 1001),
				AudioTracks: []*model.AudioTrack{
					audioTrack(`D:\WORKS\work\ep01.flac`, "jpn", ""),
					audioTrack(testAudioJpn, "jpn", ""),
				},
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", resolveLocal(testVideo) + "?fps=24000/1001",
				"-i", resolveLocal(testAudioJpn) + "?language=jpn,handler=",
			},
		},
		{
			name: "all audio skipped, video survives",
			media: &model.MediaFile{
				Video: videoTrack(testVideo, 24000, 1001),
				AudioTracks: []*model.AudioTrack{
					audioTrack(`D:\WORKS\work\ep01.wav`, "jpn", ""),
				},
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", resolveLocal(testVideo) + "?fps=24000/1001",
			},
		},
		{
			name: "extension matching is case-insensitive",
			media: &model.MediaFile{
				Video:       videoTrack(testVideo, 24000, 1001),
				AudioTracks: []*model.AudioTrack{audioTrack(`D:\WORKS\work\ep01.AAC`, "jpn", "")},
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", resolveLocal(testVideo) + "?fps=24000/1001",
				"-i", resolveLocal(`D:\WORKS\work\ep01.AAC`) + "?language=jpn,handler=",
			},
		},
		{
			name: "audio only, an m4a episode",
			media: &model.MediaFile{
				AudioTracks: []*model.AudioTrack{audioTrack(`D:\WORKS\work\ep01.m4a`, "jpn", "")},
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", resolveLocal(`D:\WORKS\work\ep01.m4a`) + "?language=jpn,handler=",
			},
		},
		{
			name: "zero frame rate is passed through",
			media: &model.MediaFile{
				Video: videoTrack(testVideo, 0, 1),
			},
			want: []string{
				"--file-format", "mp4",
				"-o", testOutput,
				"-i", resolveLocal(testVideo) + "?fps=0/1",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			opts := EpisodeOptions(testMuxer, testOutput, tc.media, 0, rootsLocal)
			assertArgs(t, BuildArgs(opts), tc.want)
		})
	}
}

func TestEpisodeOptionsWithNilMedia(t *testing.T) {
	t.Parallel()
	assertArgs(t, BuildArgs(EpisodeOptions("muxer", testOutput, nil, 0, rootsLocal)),
		[]string{"--file-format", "mp4", "-o", testOutput})
}

func TestNewEpisodeMatchesBuildArgs(t *testing.T) {
	t.Parallel()
	media := &model.MediaFile{
		Video:       videoTrack(testVideo, 24000, 1001),
		AudioTracks: []*model.AudioTrack{audioTrack(testAudioJpn, "jpn", "")},
		Chapter:     chapterTrack(testChapter),
	}
	p := NewEpisode(testMuxer, testOutput, media, 1024, rootsLocal)
	want := BuildArgs(EpisodeOptions(testMuxer, testOutput, media, 1024, rootsLocal))
	assertArgs(t, p.Args(), want)
	if p.Name() != Name {
		t.Errorf("Name() = %q, want %q", p.Name(), Name)
	}
}

func TestArgsReturnsACopy(t *testing.T) {
	t.Parallel()
	p := New(Options{Muxer: "muxer", Output: testOutput, Video: &TrackSource{File: testVideo}})
	want := append([]string(nil), p.Args()...)
	p.Args()[0] = "mutated"
	assertArgs(t, p.Args(), want)
}

func TestProgressLinesFromFixture(t *testing.T) {
	t.Parallel()
	// 400 MiB of input, so the 4 MiB steps land above the one-percent gate
	// after the first line, exactly as they did in production.
	p := New(Options{Output: testOutput, TotalFileSize: 400 << 20})
	sink := &recordingSink{}

	forEachLine(t, "muxer_progress.log", func(line string) {
		if err := p.handleLine(line, sink); err != nil {
			t.Fatalf("handleLine(%q) error = %v", line, err)
		}
	})

	if len(sink.reports) == 0 {
		t.Fatal("no progress reported from the fixture")
	}
	last := sink.reports[len(sink.reports)-1]
	if last.Percent != 100 {
		t.Errorf("final Percent = %v, want 100", last.Percent)
	}
	if last.Status != "封装完成" {
		t.Errorf("final Status = %q, want %q", last.Status, "封装完成")
	}
	// Every forwarded value must be above the one-percent gate.
	for i, r := range sink.reports[:len(sink.reports)-1] {
		if r.Percent <= 1 {
			t.Errorf("report[%d].Percent = %v, want above 1", i, r.Percent)
		}
		if r.Status != "封装中" {
			t.Errorf("report[%d].Status = %q, want %q", i, r.Status, "封装中")
		}
	}
}

func TestProgressUnknownTotalReportsNothing(t *testing.T) {
	t.Parallel()
	// A zero total makes the percentage unknown; nothing may be reported for an
	// Importing line, mirroring the legacy division by a zero total size.
	p := New(Options{Output: testOutput})
	sink := &recordingSink{}
	if err := p.handleLine("Importing: 4194304 bytes\r", sink); err != nil {
		t.Fatalf("handleLine() error = %v", err)
	}
	if len(sink.reports) != 0 {
		t.Fatalf("reports = %v, want none when the total size is unknown", sink.reports)
	}
}

func TestCarriageReturnSeparatedStepsAreAllSeen(t *testing.T) {
	t.Parallel()
	// muxer.c writes each Importing step as "…bytes\r" without a newline, so
	// several steps can arrive inside one line as read by internal/proc.
	p := New(Options{Output: testOutput, TotalFileSize: 100 << 20})
	sink := &recordingSink{}
	raw := "Importing: 4194304 bytes\rImporting: 20971520 bytes\rImporting: 41943040 bytes\r"
	if err := p.handleLine(raw, sink); err != nil {
		t.Fatalf("handleLine() error = %v", err)
	}
	if len(sink.reports) != 3 {
		t.Fatalf("reports = %d, want 3 (one per carriage-return separated step)", len(sink.reports))
	}
	for i, want := range []float64{4, 20, 40} {
		if sink.reports[i].Percent != want {
			t.Errorf("report[%d].Percent = %v, want %v", i, sink.reports[i].Percent, want)
		}
	}
}

func TestErrorLineBecomesStructuredError(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		line       string
		wantDetail string
	}{
		{
			name:       "unsupported input",
			line:       "Error: failed to open input file.",
			wantDetail: "failed to open input file.",
		},
		{
			name:       "no media",
			line:       "Error: there is no media that can be stored in output movie.",
			wantDetail: "there is no media that can be stored in output movie.",
		},
		{
			name:       "corrupted frame",
			line:       "Error: failed to get a frame from input file. Maybe corrupted.",
			wantDetail: "failed to get a frame from input file. Maybe corrupted.",
		},
		{
			// muxer_error() prints the prefix and the message with two separate
			// eprintf calls, so a run whose message starts a new line leaves
			// only the prefix on this one. The legacy Substring(7) produced the
			// same empty detail.
			name:       "bare prefix",
			line:       "Error: ",
			wantDetail: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := New(Options{Output: testOutput})
			err := p.handleLine(tc.line, &recordingSink{})
			if err == nil {
				t.Fatalf("handleLine(%q) = nil, want an error", tc.line)
			}
			e := okerr.AsError(err)
			if e.Summary != okerr.ErrLSmash.Summary {
				t.Errorf("Summary = %q, want %q", e.Summary, okerr.ErrLSmash.Summary)
			}
			if e.Detail != tc.wantDetail {
				t.Errorf("Detail = %q, want %q", e.Detail, tc.wantDetail)
			}
			if e.Kind != okerr.KindTool {
				t.Errorf("Kind = %q, want %q", e.Kind, okerr.KindTool)
			}
			// The operator-facing rendering must use the legacy template.
			rendered := okerr.Render(e)
			if !strings.Contains(rendered, "l-smash出错") || !strings.Contains(rendered, testOutput) {
				t.Errorf("Render() = %q, want it to mention l-smash and the output file", rendered)
			}
		})
	}
}

func TestErrorFixtureStopsAtTheFirstFailure(t *testing.T) {
	t.Parallel()
	// 1 MiB of input, so the single 4 MiB step in the fixture is above the
	// one-percent gate and must have been reported before the failure.
	p := New(Options{Output: testOutput, TotalFileSize: 1 << 20})
	sink := &recordingSink{}

	var (
		lines int
		err   error
	)
	forEachLine(t, "muxer_error.log", func(line string) {
		lines++
		if err != nil {
			t.Errorf("handleLine(%q) ran after an error was already returned", line)
			return
		}
		err = p.handleLine(line, sink)
	})
	if err == nil {
		t.Fatal("the error fixture did not produce an error")
	}
	if lines < 2 {
		t.Fatalf("fixture only had %d lines, want progress before the failure", lines)
	}
	if len(sink.reports) == 0 {
		t.Error("no progress was reported before the failure")
	}
	if e := okerr.AsError(err); !strings.Contains(e.Detail, "failed to open input file") {
		t.Errorf("Detail = %q, want the l-smash message", e.Detail)
	}
}

func TestHandlerIgnoresUnrelatedOutput(t *testing.T) {
	t.Parallel()
	p := New(Options{Output: testOutput, TotalFileSize: 1 << 30})
	for _, line := range []string{
		"MP4 muxing mode",
		"Track 1: H.265 High Efficiency Video Coding",
		"Track 2: MPEG-4 Audio",
		"Finalizing: [ 45.67%]",
		"",
	} {
		if err := p.handleLine(line, &recordingSink{}); err != nil {
			t.Errorf("handleLine(%q) error = %v", line, err)
		}
	}
}

func TestPercentOf(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		size  int64
		total int64
		want  float64
	}{
		{"half", 50, 100, 50},
		{"unknown total", 50, 0, -1},
		{"negative total", 50, -1, -1},
		{"more than the total", 200, 100, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := percentOf(tc.size, tc.total); got != tc.want {
				t.Errorf("percentOf(%d, %d) = %v, want %v", tc.size, tc.total, got, tc.want)
			}
		})
	}
}

func TestAcceptableAudioExtensions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		ext  string
		want bool
	}{
		{".aac", true},
		{".m4a", true},
		{".ac3", true},
		{".dts", true},
		{".eac3", true},
		{".AAC", true},
		{".flac", false},
		{".wav", false},
		{".sup", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isAcceptableAudio(tc.ext); got != tc.want {
			t.Errorf("isAcceptableAudio(%q) = %v, want %v", tc.ext, got, tc.want)
		}
	}
}

// recordingSink captures the progress reports of a run.
type recordingSink struct{ reports []jobproc.Progress }

func (s *recordingSink) Report(p jobproc.Progress) { s.reports = append(s.reports, p) }

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

func forEachLine(t *testing.T, name string, fn func(string)) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fn(sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
}
