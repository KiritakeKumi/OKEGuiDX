package mkvmerge

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// The package must satisfy the shared processor contract.
var (
	_ jobproc.Processor     = (*Processor)(nil)
	_ jobproc.Controllable  = (*Processor)(nil)
	_ jobproc.Prioritizable = (*Processor)(nil)
)

// root is the local volume root the cases below resolve against. An empty root
// is the standalone configuration on Windows, where toolchain.Discover leaves
// Volumes["local"] empty and a reference resolves to its drive-less path
// (model.FileRef.Resolve).
const root = ""

// localPath is the path mkvmerge receives for a reference below root.
func localPath(rel string) string { return model.NewFileRef(rel).ResolveLocal(root) }

// trackInfo builds the Info of a track under test.
func trackInfo(mux model.MuxOption, language, name string, order int) model.Info {
	i := model.NewInfo()
	i.Mux = mux
	if language != "" {
		i.Language = language
	}
	i.Name = name
	i.Order = order
	return i
}

func videoTrack(file, timecode string) *model.VideoTrack {
	t := &model.VideoTrack{
		Track: model.Track{
			File:      model.NewFileRef(file),
			Info:      model.NewInfo(),
			TrackType: model.TrackTypeVideo,
		},
		Video: model.NewVideoInfo(),
	}
	if timecode != "" {
		t.Video.TimeCodeFile = model.NewFileRef(timecode)
	}
	return t
}

func audioTrack(file string, mux model.MuxOption, language, name string, order int) *model.AudioTrack {
	return &model.AudioTrack{Track: model.Track{
		File:      model.NewFileRef(file),
		Info:      trackInfo(mux, language, name, order),
		TrackType: model.TrackTypeAudio,
	}}
}

func subtitleTrack(file string, mux model.MuxOption, language, name string, order int) *model.SubtitleTrack {
	return &model.SubtitleTrack{Track: model.Track{
		File:      model.NewFileRef(file),
		Info:      trackInfo(mux, language, name, order),
		TrackType: model.TrackTypeSubtitle,
	}}
}

func chapterTrack(file, language string) *model.ChapterTrack {
	info := model.NewInfo()
	// ChapterTrack assigns the language as-is, an empty one included.
	info.Language = language
	return &model.ChapterTrack{Track: model.Track{
		File:      model.NewFileRef(file),
		Info:      info,
		TrackType: model.TrackTypeChapter,
	}}
}

// multiAudioMedia carries one track per mux option, which is what a profile
// with four audio tracks produces.
func multiAudioMedia() *model.MediaFile {
	return &model.MediaFile{
		Video: videoTrack("V:/media/ep01/v.hevc", "V:/media/ep01/ep01.v2.tcfile"),
		AudioTracks: []*model.AudioTrack{
			audioTrack("V:/media/ep01/a1.flac", model.MuxOptionDefault, "jpn", "Main", 0),
			audioTrack("V:/media/ep01/a2.flac", model.MuxOptionMka, "eng", "Commentary", 1),
			audioTrack("V:/media/ep01/a3.flac", model.MuxOptionExternal, "jpn", "Ext", 2),
			audioTrack("V:/media/ep01/a4.flac", model.MuxOptionExtractOnly, "jpn", "", 3),
			audioTrack("V:/media/ep01/a5.flac", model.MuxOptionSkip, "jpn", "", 4),
		},
	}
}

// TestBuildArgs pins the generated command line. mkvmerge resolves
// --track-order against the order of the source files, so the whole slice is
// compared, not just its set of tokens.
func TestBuildArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		output    string
		media     *model.MediaFile
		container Container
		roots     map[string]string
		want      []string
	}{
		{
			// Requirement 1: the simplest episode, one video and one audio
			// track.
			name:   "video and one audio track",
			output: "out/ep01.mkv",
			media: &model.MediaFile{
				Video: videoTrack("V:/media/ep01/v.hevc", ""),
				AudioTracks: []*model.AudioTrack{
					audioTrack("V:/media/ep01/a1.flac", model.MuxOptionDefault, "jpn", "", model.MaxOrder),
				},
			},
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mkv",
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", localPath("V:/media/ep01/v.hevc"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:1",
				"--language", "0:jpn",
				"--track-name", "0:",
				"(", localPath("V:/media/ep01/a1.flac"), ")",
				"--track-order", "0:0,1:0",
			},
		},
		{
			// Requirement 2, main container: only Default tracks are muxed.
			// Mka goes to the external file, External is only renamed with a
			// CRC32, ExtractOnly and Skip are not muxed at all.
			name:   "main container drops non-default tracks",
			output: "out/ep01.mkv",
			media:  multiAudioMedia(),
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mkv",
				"--timestamps", "0:" + localPath("V:/media/ep01/ep01.v2.tcfile"),
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", localPath("V:/media/ep01/v.hevc"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:1",
				"--language", "0:jpn",
				"--track-name", "0:Main",
				"(", localPath("V:/media/ep01/a1.flac"), ")",
				"--track-order", "0:0,1:0",
			},
		},
		{
			// Requirement 2, external audio container: Mka tracks only, and
			// none of them is default because there is no video track.
			name:      "external audio container takes only Mka tracks",
			output:    "out/ep01.mka",
			media:     multiAudioMedia(),
			container: ContainerMKA,
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mka",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:eng",
				"--track-name", "0:Commentary",
				"(", localPath("V:/media/ep01/a2.flac"), ")",
				"--track-order", "0:0",
			},
		},
		{
			// Requirement 3: subtitle tracks, Default in the main container.
			name:   "subtitle track in the main container",
			output: "out/ep01.mkv",
			media: &model.MediaFile{
				Video: videoTrack("V:/media/ep01/v.hevc", ""),
				AudioTracks: []*model.AudioTrack{
					audioTrack("V:/media/ep01/a1.flac", model.MuxOptionDefault, "jpn", "Main", 0),
				},
				SubtitleTracks: []*model.SubtitleTrack{
					subtitleTrack("V:/media/ep01/s1.ass", model.MuxOptionDefault, "jpn", "", 0),
				},
			},
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mkv",
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", localPath("V:/media/ep01/v.hevc"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:1",
				"--language", "0:jpn",
				"--track-name", "0:Main",
				"(", localPath("V:/media/ep01/a1.flac"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:jpn",
				"--track-name", "0:",
				"(", localPath("V:/media/ep01/s1.ass"), ")",
				"--track-order", "0:0,1:0,2:0",
			},
		},
		{
			// Requirement 3, external audio container: a Mka subtitle track
			// travels with the audio.
			name:      "subtitle track in the external audio container",
			output:    "out/ep01.mka",
			container: ContainerMKA,
			media: &model.MediaFile{
				Video: videoTrack("V:/media/ep01/v.hevc", ""),
				AudioTracks: []*model.AudioTrack{
					audioTrack("V:/media/ep01/a1.flac", model.MuxOptionDefault, "jpn", "Main", 0),
					audioTrack("V:/media/ep01/a2.flac", model.MuxOptionMka, "eng", "Commentary", 1),
				},
				SubtitleTracks: []*model.SubtitleTrack{
					subtitleTrack("V:/media/ep01/s1.ass", model.MuxOptionDefault, "jpn", "", 0),
					subtitleTrack("V:/media/ep01/s2.ass", model.MuxOptionMka, "eng", "Signs", 1),
				},
			},
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mka",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:eng",
				"--track-name", "0:Commentary",
				"(", localPath("V:/media/ep01/a2.flac"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:eng",
				"--track-name", "0:Signs",
				"(", localPath("V:/media/ep01/s2.ass"), ")",
				"--track-order", "0:0,1:0",
			},
		},
		{
			// Requirement 4: chapters, with a chapter language.
			name:   "chapter file with a language",
			output: "out/ep01.mkv",
			media: &model.MediaFile{
				Video: videoTrack("V:/media/ep01/v.hevc", ""),
				AudioTracks: []*model.AudioTrack{
					audioTrack("V:/media/ep01/a1.flac", model.MuxOptionDefault, "jpn", "Main", 0),
				},
				Chapter: chapterTrack("V:/media/ep01/ep01.txt", "jpn"),
			},
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mkv",
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", localPath("V:/media/ep01/v.hevc"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:1",
				"--language", "0:jpn",
				"--track-name", "0:Main",
				"(", localPath("V:/media/ep01/a1.flac"), ")",
				"--chapter-language", "jpn",
				"--chapters", localPath("V:/media/ep01/ep01.txt"),
				"--track-order", "0:0,1:0",
			},
		},
		{
			// Requirement 4: an empty chapter language omits the option.
			name:   "chapter file without a language",
			output: "out/ep01.mkv",
			media: &model.MediaFile{
				Video:   videoTrack("V:/media/ep01/v.hevc", ""),
				Chapter: chapterTrack("V:/media/ep01/ep01.txt", ""),
			},
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mkv",
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", localPath("V:/media/ep01/v.hevc"), ")",
				"--chapters", localPath("V:/media/ep01/ep01.txt"),
				"--track-order", "0:0",
			},
		},
		{
			// Requirement 4: the external audio container never carries
			// chapters, exactly like MkaOutFile in the legacy code.
			name:      "external audio container drops chapters",
			output:    "out/ep01.mka",
			container: ContainerMKA,
			media: &model.MediaFile{
				AudioTracks: []*model.AudioTrack{
					audioTrack("V:/media/ep01/a2.flac", model.MuxOptionMka, "eng", "Commentary", 0),
				},
				Chapter: chapterTrack("V:/media/ep01/ep01.txt", "jpn"),
			},
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mka",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:eng",
				"--track-name", "0:Commentary",
				"(", localPath("V:/media/ep01/a2.flac"), ")",
				"--track-order", "0:0",
			},
		},
		{
			// Requirement 5: Info.Order decides placement, and the file ids in
			// --track-order follow the emitted order. Tracks sharing an order
			// keep their original position (OrderBy is stable). Audio and
			// subtitles are ordered independently, and the audio order does
			// not change where a subtitle lands.
			name:   "Order decides track placement",
			output: "out/ep01.mkv",
			media: &model.MediaFile{
				Video: videoTrack("V:/media/ep01/v.hevc", ""),
				AudioTracks: []*model.AudioTrack{
					audioTrack("V:/media/ep01/aC.flac", model.MuxOptionDefault, "jpn", "C", 2),
					audioTrack("V:/media/ep01/aA.flac", model.MuxOptionDefault, "jpn", "A", model.MaxOrder),
					audioTrack("V:/media/ep01/aB.flac", model.MuxOptionDefault, "jpn", "B", 0),
				},
				SubtitleTracks: []*model.SubtitleTrack{
					subtitleTrack("V:/media/ep01/s2.ass", model.MuxOptionDefault, "jpn", "S2", 2),
					subtitleTrack("V:/media/ep01/s1.ass", model.MuxOptionDefault, "jpn", "S1", 1),
				},
			},
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mkv",
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", localPath("V:/media/ep01/v.hevc"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:1",
				"--language", "0:jpn",
				"--track-name", "0:B",
				"(", localPath("V:/media/ep01/aB.flac"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:jpn",
				"--track-name", "0:C",
				"(", localPath("V:/media/ep01/aC.flac"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:jpn",
				"--track-name", "0:A",
				"(", localPath("V:/media/ep01/aA.flac"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:jpn",
				"--track-name", "0:S1",
				"(", localPath("V:/media/ep01/s1.ass"), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:jpn",
				"--track-name", "0:S2",
				"(", localPath("V:/media/ep01/s2.ass"), ")",
				"--track-order", "0:0,1:0,2:0,3:0,4:0,5:0",
			},
		},
		{
			// A volume root replaces the drive-less path.
			name:   "explicit volume root is honoured",
			output: "out/ep01.mkv",
			roots:  map[string]string{model.LocalVolume: "V:"},
			media: &model.MediaFile{
				Video: videoTrack("V:/media/ep01/v.hevc", ""),
			},
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mkv",
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", filepath.Join("V:", filepath.FromSlash("/media/ep01/v.hevc")), ")",
				"--track-order", "0:0",
			},
		},
		{
			// A job without tracks still gets the option, because the legacy
			// code appended it unconditionally. mkvmerge rejects the run.
			name:   "no media",
			output: "out/ep01.mkv",
			want: []string{
				"--ui-language", "en",
				"--output", "out/ep01.mkv",
				"--track-order", "",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := BuildArgs(Options{
				Mkvmerge:  `C:\tools\mkvtoolnix\mkvmerge.exe`,
				Output:    tc.output,
				Media:     tc.media,
				Container: tc.container,
				Roots:     tc.roots,
			})
			assertArgs(t, got, tc.want)
		})
	}
}

// TestEpisodeArgsAreIndependentOfTheBuilder hardcodes the complete command line
// instead of calling the builder the way TestBuildArgs does. The tokens are
// transcribed from the legacy NewMkvEpisodeMuxer.BuildCommandline
// (JobProcessor/Muxer/NewMkvEpisodeMuxer.cs): the video file is grouped in
// parentheses with its per-track options and gets a --timestamps option when a
// timecode file is present, every audio and subtitle file is preceded by the
// --no-track-tags --no-global-tags pair, the Default audio track is the only
// default one, and the chapter options and --track-order close the line.
//
// TestBuildArgs spells its paths with localPath, which is NewFileRef().ResolveLocal:
// the expected argv and the argv under test share the code path, so a regression
// in FileRef.ResolveLocal leaves that test green. This test carries the paths as
// literals with real drive letters, so only the builder can change them.
func TestEpisodeArgsAreIndependentOfTheBuilder(t *testing.T) {
	t.Parallel()

	media := &model.MediaFile{
		Video: videoTrack(`V:\media\ep01\v.hevc`, `V:\media\ep01\ep01.v2.tcfile`),
		AudioTracks: []*model.AudioTrack{
			audioTrack(`V:\media\ep01\a1.flac`, model.MuxOptionDefault, "jpn", "Main", 0),
			audioTrack(`V:\media\ep01\a2.flac`, model.MuxOptionMka, "eng", "Commentary", 1),
			audioTrack(`V:\media\ep01\a3.flac`, model.MuxOptionExternal, "jpn", "Ext", 2),
			audioTrack(`V:\media\ep01\a4.flac`, model.MuxOptionExtractOnly, "jpn", "", 3),
			audioTrack(`V:\media\ep01\a5.flac`, model.MuxOptionSkip, "jpn", "", 4),
		},
		SubtitleTracks: []*model.SubtitleTrack{
			subtitleTrack(`V:\media\ep01\s1.ass`, model.MuxOptionDefault, "jpn", "", 0),
			subtitleTrack(`V:\media\ep01\s2.ass`, model.MuxOptionMka, "eng", "Signs", 1),
		},
		Chapter: chapterTrack(`V:\media\ep01\ep01.txt`, "jpn"),
	}
	got := BuildArgs(Options{
		Mkvmerge: `C:\tools\mkvtoolnix\mkvmerge.exe`,
		Output:   `V:\out\ep01.mkv`,
		Media:    media,
	})
	want := []string{
		"--ui-language", "en",
		"--output", `V:\out\ep01.mkv`,
		"--timestamps", `0:V:\media\ep01\ep01.v2.tcfile`,
		"--default-track", "0:1",
		"--language", "0:und",
		"--track-name", "0:",
		"(", `V:\media\ep01\v.hevc`, ")",
		"--no-track-tags", "--no-global-tags",
		"--default-track", "0:1",
		"--language", "0:jpn",
		"--track-name", "0:Main",
		"(", `V:\media\ep01\a1.flac`, ")",
		"--no-track-tags", "--no-global-tags",
		"--default-track", "0:0",
		"--language", "0:jpn",
		"--track-name", "0:",
		"(", `V:\media\ep01\s1.ass`, ")",
		"--chapter-language", "jpn",
		"--chapters", `V:\media\ep01\ep01.txt`,
		"--track-order", "0:0,1:0,2:0",
	}
	assertArgs(t, got, want)
}

// TestParseLineClassification pins the line classifier, including the two
// quirks of the legacy implementation: the threshold on progress values and the
// fixed offset used for the error detail.
func TestParseLineClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		line       string
		wantPct    float64
		wantOK     bool
		wantErr    bool
		wantDetail string
	}{
		{
			name:    "progress",
			line:    "Progress: 92%",
			wantPct: 92,
			wantOK:  true,
		},
		{
			name: "progress at the threshold is ignored",
			line: "Progress: 1%",
		},
		{
			name: "progress without digits is ignored",
			line: "Progress: %",
		},
		{
			name:    "old completion wording",
			line:    "Muxing took 3 seconds.",
			wantPct: 100,
			wantOK:  true,
		},
		{
			name:    "new completion wording",
			line:    "Multiplexing took 0 seconds.",
			wantPct: 100,
			wantOK:  true,
		},
		{
			name: "demultiplexer chatter",
			line: "'v.hevc': Using the demultiplexer for the format 'HEVC/h.265'.",
		},
		{
			name: "flac pre-parsing is not mux progress",
			line: "+-> Pre-parsing FLAC file: 100%",
		},
		{
			name:       "error",
			line:       "Error: The file 'x.mkv' could not be opened for reading: open file error.",
			wantErr:    true,
			wantDetail: "The file 'x.mkv' could not be opened for reading: open file error.",
		},
		{
			// MkvmergeMuxer used line.Substring(7), so a marker that does not
			// start the line yields a garbled detail. Real mkvmerge errors
			// always start with the marker; pinned so the port stays faithful.
			name:       "embedded error marker keeps the legacy offset",
			line:       "'x.mkv': Error: something",
			wantErr:    true,
			wantDetail: ": Error: something",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pct, ok, err := parseLine(tc.line)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseLine(%q) error = nil, want an error", tc.line)
				}
				e := okerr.AsError(err)
				if !errors.Is(e, okerr.ErrMkvmerge) {
					t.Errorf("kind = %q, want %q", e.Kind, okerr.KindTool)
				}
				if e.Output != tc.wantDetail {
					t.Errorf("detail = %q, want %q", e.Output, tc.wantDetail)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLine(%q) error = %v", tc.line, err)
			}
			if ok != tc.wantOK {
				t.Fatalf("parseLine(%q) ok = %v, want %v", tc.line, ok, tc.wantOK)
			}
			if pct != tc.wantPct {
				t.Errorf("parseLine(%q) percent = %v, want %v", tc.line, pct, tc.wantPct)
			}
		})
	}
}

// The fixtures are the captured stdout of real mkvmerge v34.0.0 runs: one
// complete episode mux and one failure.

func TestParseRealSuccessfulRun(t *testing.T) {
	t.Parallel()

	var (
		updates int
		last    float64
	)
	for _, line := range readLines(t, "mux_ok.log") {
		pct, ok, err := parseLine(line)
		if err != nil {
			t.Fatalf("parseLine(%q) error = %v", line, err)
		}
		if !ok {
			continue
		}
		updates++
		last = pct
	}
	if updates < 2 {
		t.Errorf("progress updates = %d, want at least 2", updates)
	}
	if last != 100 {
		t.Errorf("final percent = %v, want 100", last)
	}
}

func TestParseRealFailedRun(t *testing.T) {
	t.Parallel()

	var errs []error
	for _, line := range readLines(t, "mux_error.log") {
		_, _, err := parseLine(line)
		if err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) != 1 {
		t.Fatalf("errors = %d, want 1", len(errs))
	}
	e := okerr.AsError(errs[0])
	if !errors.Is(e, okerr.ErrMkvmerge) {
		t.Errorf("kind = %q, want %q", e.Kind, okerr.KindTool)
	}
	const want = "The file 'missing_source.mkv' could not be opened for reading: open file error."
	if e.Output != want {
		t.Errorf("detail = %q, want %q", e.Output, want)
	}
}

// TestProcessorForwardsProgress drives the handler Run installs on the process
// streams, without spawning anything.
func TestProcessorForwardsProgress(t *testing.T) {
	t.Parallel()

	var got []float64
	p := New(Options{})
	p.sink = jobproc.ProgressFunc(func(prog jobproc.Progress) {
		got = append(got, prog.Percent)
	})

	for _, line := range readLines(t, "mux_ok.log") {
		if err := p.onLine(line); err != nil {
			t.Fatalf("onLine(%q) error = %v", line, err)
		}
	}

	if len(got) < 2 {
		t.Fatalf("reported percentages = %v, want at least two updates", got)
	}
	if got[len(got)-1] != 100 {
		t.Errorf("last reported percentage = %v, want 100", got[len(got)-1])
	}
}

func TestProcessorStopsOnErrorLine(t *testing.T) {
	t.Parallel()

	p := New(Options{})
	err := p.onLine("Error: Invalid track order")
	if err == nil {
		t.Fatal("onLine() error = nil, want an error")
	}
	if !errors.Is(err, okerr.ErrMkvmerge) {
		t.Errorf("kind = %q, want %q", okerr.AsError(err).Kind, okerr.KindTool)
	}
}

// TestRecordFailureKeepsFirst checks the legacy behaviour of keeping the first
// failure while reading continues: mkvmerge can print several "Error:" lines
// and only the first one is reported.
func TestRecordFailureKeepsFirst(t *testing.T) {
	t.Parallel()

	p := New(Options{})
	_, _, first := parseLine("Error: first")
	_, _, second := parseLine("Error: second")
	p.recordFailure(first)
	p.recordFailure(second)

	if got := okerr.AsError(p.failure()).Output; got != "first" {
		t.Errorf("recorded failure = %q, want %q", got, "first")
	}
}

func TestProcessorIgnoresMissingFailure(t *testing.T) {
	t.Parallel()

	if err := New(Options{}).failure(); err != nil {
		t.Errorf("failure() = %v, want nil", err)
	}
}

// TestRenderedMessageMatchesLegacy pins the operator-facing text. The legacy
// ExceptionParser formatted Constants.mmgErrorMsg with the text after
// "Error: " and the input file, and okerr.Render must reproduce it.
func TestRenderedMessageMatchesLegacy(t *testing.T) {
	t.Parallel()

	_, _, err := parseLine("Error: The file 'x.mkv' could not be opened for reading: open file error.")
	if err == nil {
		t.Fatal("parseLine() error = nil, want an error")
	}
	got := okerr.Render(okerr.AsError(err).WithFile("C:/in/ep01.m2ts"))
	want := "mkvmerge出错: The file 'x.mkv' could not be opened for reading: open file error." +
		"。该文件C:/in/ep01.m2ts将跳过处理。请转告技术总监复查。"
	if got != want {
		t.Errorf("rendered message =\n  %q\nwant\n  %q", got, want)
	}
}

func TestLastLine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"trailing newline", "one\ntwo\n", "two"},
		{"trailing blank lines", "one\n\n   \n", "one"},
		{"single line", "only", "only"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := lastLine(tc.input); got != tc.want {
				t.Errorf("lastLine(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("argument count = %d, want %d\ngot:  %q\nwant: %q", len(got), len(want), got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("arguments[%d] = %q, want %q\ngot:  %q\nwant: %q", i, got[i], want[i], got, want)
		}
	}
}

func readLines(t *testing.T, name string) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture %s: %v", name, err)
	}
	defer func() { _ = f.Close() }()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return lines
}
