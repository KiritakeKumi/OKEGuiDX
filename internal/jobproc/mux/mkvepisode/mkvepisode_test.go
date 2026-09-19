package mkvepisode

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/mux/mkvmerge"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/mux/simple"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// The package must satisfy the shared processor contract.
var (
	_ jobproc.Processor     = (*Processor)(nil)
	_ jobproc.Controllable  = (*Processor)(nil)
	_ jobproc.Prioritizable = (*Processor)(nil)
)

const mkvmergePath = `C:\tools\mkvtoolnix\mkvmerge.exe`

// The cases resolve their FileRefs on an empty root, which is standalone mode
// on Windows (see mkvmerge_test.go for the same convention).
const root = ""

func localPath(rel string) string { return model.NewFileRef(rel).ResolveLocal(root) }

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

func audioTrack(file string, mux model.MuxOption, language, name string) *model.AudioTrack {
	info := model.NewInfo()
	info.Mux = mux
	if language != "" {
		info.Language = language
	}
	info.Name = name
	return &model.AudioTrack{Track: model.Track{
		File:      model.NewFileRef(file),
		Info:      info,
		TrackType: model.TrackTypeAudio,
	}}
}

func subtitleTrack(file, language, name string) *model.SubtitleTrack {
	info := model.NewInfo()
	if language != "" {
		info.Language = language
	}
	info.Name = name
	return &model.SubtitleTrack{Track: model.Track{
		File:      model.NewFileRef(file),
		Info:      info,
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

// TestArgs pins the mkvmerge command line this package hands to mkvmerge for
// the shapes the pipeline produces. The construction itself lives in
// internal/jobproc/mux/mkvmerge; what is asserted here is that the episode mux
// wires a MediaFile into it without losing the container choice, the timecode
// file, the chapter file or the track order.
func TestArgs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		output    string
		media     *model.MediaFile
		container mkvmerge.Container
		want      []string
	}{
		{
			// Acceptance 1: a single-part episode, one video and one audio
			// track. The video file is the one AppendVideo produced.
			name:   "single part episode",
			output: `D:\out\00001.m2ts.mkv`,
			media: &model.MediaFile{
				Video: videoTrack(`D:\work\ep01_all.mkv`, ""),
				AudioTracks: []*model.AudioTrack{
					audioTrack(`D:\work\ep01.flac`, model.MuxOptionDefault, "jpn", ""),
				},
			},
			want: []string{
				"--ui-language", "en",
				"--output", `D:\out\00001.m2ts.mkv`,
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", localPath(`D:\work\ep01_all.mkv`), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:1",
				"--language", "0:jpn",
				"--track-name", "0:",
				"(", localPath(`D:\work\ep01.flac`), ")",
				"--track-order", "0:0,1:0",
			},
		},
		{
			// Acceptance 2: a two-part episode. The parts were joined by the
			// append step beforehand, so the muxer sees exactly one video
			// input -- but it still gets the episode's timecode file.
			name:   "two part episode uses the joined video",
			output: `D:\out\00001.m2ts.mkv`,
			media: &model.MediaFile{
				Video: videoTrack(`D:\work\ep01_all.mkv`, `D:\work\ep01.v2.tcfile`),
				AudioTracks: []*model.AudioTrack{
					audioTrack(`D:\work\ep01.flac`, model.MuxOptionDefault, "jpn", "Main"),
				},
			},
			want: []string{
				"--ui-language", "en",
				"--output", `D:\out\00001.m2ts.mkv`,
				"--timestamps", "0:" + localPath(`D:\work\ep01.v2.tcfile`),
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", localPath(`D:\work\ep01_all.mkv`), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:1",
				"--language", "0:jpn",
				"--track-name", "0:Main",
				"(", localPath(`D:\work\ep01.flac`), ")",
				"--track-order", "0:0,1:0",
			},
		},
		{
			// Acceptance 3: a chapter file with a language. The chapter track
			// is added to the main container only, and the chapter options
			// come after every track option, as in the legacy command line.
			name:   "episode with chapters",
			output: `D:\out\00001.m2ts.mkv`,
			media: &model.MediaFile{
				Video: videoTrack(`D:\work\ep01_all.mkv`, ""),
				AudioTracks: []*model.AudioTrack{
					audioTrack(`D:\work\ep01.flac`, model.MuxOptionDefault, "jpn", "Main"),
				},
				SubtitleTracks: []*model.SubtitleTrack{
					subtitleTrack(`D:\work\ep01.ass`, "jpn", ""),
				},
				Chapter: chapterTrack(`D:\work\ep01.txt`, "jpn"),
			},
			want: []string{
				"--ui-language", "en",
				"--output", `D:\out\00001.m2ts.mkv`,
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", localPath(`D:\work\ep01_all.mkv`), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:1",
				"--language", "0:jpn",
				"--track-name", "0:Main",
				"(", localPath(`D:\work\ep01.flac`), ")",
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:jpn",
				"--track-name", "0:",
				"(", localPath(`D:\work\ep01.ass`), ")",
				"--chapter-language", "jpn",
				"--chapters", localPath(`D:\work\ep01.txt`),
				"--track-order", "0:0,1:0,2:0",
			},
		},
		{
			// Acceptance 3: an empty chapter language omits the option but
			// keeps the chapter file.
			name:   "episode with chapters and no language",
			output: `D:\out\00001.m2ts.mkv`,
			media: &model.MediaFile{
				Video:   videoTrack(`D:\work\ep01_all.mkv`, ""),
				Chapter: chapterTrack(`D:\work\ep01.txt`, ""),
			},
			want: []string{
				"--ui-language", "en",
				"--output", `D:\out\00001.m2ts.mkv`,
				"--default-track", "0:1",
				"--language", "0:und",
				"--track-name", "0:",
				"(", localPath(`D:\work\ep01_all.mkv`), ")",
				"--chapters", localPath(`D:\work\ep01.txt`),
				"--track-order", "0:0",
			},
		},
		{
			// The external audio container takes the Mka tracks and never the
			// video or the chapters, exactly like MkaOutFile in the legacy
			// pipeline.
			name:      "external audio container",
			output:    `D:\work\ep01.mka`,
			container: mkvmerge.ContainerMKA,
			media: &model.MediaFile{
				Video: videoTrack(`D:\work\ep01_all.mkv`, ""),
				AudioTracks: []*model.AudioTrack{
					audioTrack(`D:\work\ep01.flac`, model.MuxOptionDefault, "jpn", "Main"),
					audioTrack(`D:\work\ep01c.flac`, model.MuxOptionMka, "eng", "Commentary"),
				},
				Chapter: chapterTrack(`D:\work\ep01.txt`, "jpn"),
			},
			want: []string{
				"--ui-language", "en",
				"--output", `D:\work\ep01.mka`,
				"--no-track-tags", "--no-global-tags",
				"--default-track", "0:0",
				"--language", "0:eng",
				"--track-name", "0:Commentary",
				"(", localPath(`D:\work\ep01c.flac`), ")",
				"--track-order", "0:0",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := New(Options{
				Mkvmerge:  mkvmergePath,
				Output:    tc.output,
				Media:     tc.media,
				Container: tc.container,
			})
			assertArgs(t, p.Args(), tc.want)
		})
	}
}

// TestFileName covers TaskDetail.UpdateOutputFileName, including the fallback
// to the video format when the profile leaves ContainerFormat empty.
func TestFileName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		inputFile       string
		containerFormat string
		videoFormat     string
		want            string
	}{
		{
			name:            "mkv container",
			inputFile:       `D:\BDMV\STREAM\00001.m2ts`,
			containerFormat: "MKV",
			videoFormat:     "HEVC",
			want:            "00001.m2ts.mkv",
		},
		{
			name:            "mp4 container",
			inputFile:       `D:\BDMV\STREAM\00001.m2ts`,
			containerFormat: "MP4",
			videoFormat:     "AVC",
			want:            "00001.m2ts.mp4",
		},
		{
			// The legacy code lowercased the field, so a profile that wrote
			// "mkv" produces the same name as one that wrote "MKV".
			name:            "lowercase container is normalised",
			inputFile:       "00001.m2ts",
			containerFormat: "mkv",
			videoFormat:     "HEVC",
			want:            "00001.m2ts.mkv",
		},
		{
			name:            "empty container falls back to the video format",
			inputFile:       `D:\BDMV\STREAM\00001.m2ts`,
			containerFormat: "",
			videoFormat:     "HEVC",
			want:            "00001.m2ts.hevc",
		},
		{
			name:            "fallback is lowercased too",
			inputFile:       "00001.m2ts",
			containerFormat: "",
			videoFormat:     "AVC",
			want:            "00001.m2ts.avc",
		},
		{
			// The whole input name is kept, dots included: the legacy code
			// appended the suffix to FileInfo.Name rather than replacing the
			// extension.
			name:            "input extension is kept",
			inputFile:       `D:\in\ep01.mkv`,
			containerFormat: "MKV",
			videoFormat:     "HEVC",
			want:            "ep01.mkv.mkv",
		},
		{
			name:            "forward slashes are accepted",
			inputFile:       "D:/in/ep01.m2ts",
			containerFormat: "MKV",
			videoFormat:     "HEVC",
			want:            "ep01.m2ts.mkv",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := FileName(tc.inputFile, tc.containerFormat, tc.videoFormat)
			if got != tc.want {
				t.Errorf("FileName(%q, %q, %q) = %q, want %q",
					tc.inputFile, tc.containerFormat, tc.videoFormat, got, tc.want)
			}
		})
	}
}

// TestOutputPath covers ExecuteTaskService.GenerateMuxJob's Path.Combine:
// the deliverable sits next to the profile's output prefix, not below it.
func TestOutputPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		prefix   string
		fileName string
		want     string
	}{
		{
			name:     "windows prefix",
			prefix:   `D:\out\00001.m2ts`,
			fileName: "00001.m2ts.mkv",
			want:     `D:\out\00001.m2ts.mkv`,
		},
		{
			// The legacy code always called Path.GetDirectoryName on the
			// prefix, so a prefix that already ends in an extension keeps the
			// directory and drops the rest.
			name:     "prefix with extension",
			prefix:   `D:\out\ep01.work`,
			fileName: "00001.m2ts.mkv",
			want:     `D:\out\00001.m2ts.mkv`,
		},
		{
			name:     "no directory",
			prefix:   "ep01",
			fileName: "00001.m2ts.mkv",
			want:     "00001.m2ts.mkv",
		},
		{
			name:     "forward slashes are preserved",
			prefix:   "out/ep01",
			fileName: "00001.m2ts.mkv",
			want:     "out/00001.m2ts.mkv",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := OutputPath(tc.prefix, tc.fileName); got != tc.want {
				t.Errorf("OutputPath(%q, %q) = %q, want %q", tc.prefix, tc.fileName, got, tc.want)
			}
		})
	}
}

// TestMKAOutputPath pins the MKA special case: the prefix gets ".mka"
// appended, it is not turned into a directory plus a file name.
func TestMKAOutputPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		prefix string
		want   string
	}{
		{`D:\work\00001.m2ts`, `D:\work\00001.m2ts.mka`},
		{"work/ep01", "work/ep01.mka"},
	}
	for _, tc := range cases {
		t.Run(tc.prefix, func(t *testing.T) {
			t.Parallel()
			if got := MKAOutputPath(tc.prefix); got != tc.want {
				t.Errorf("MKAOutputPath(%q) = %q, want %q", tc.prefix, got, tc.want)
			}
		})
	}
}

// TestPartsAppendOptions covers the multi-part wiring: the parts are handed to
// the append step in playback order and the intermediate file is named after
// the working prefix, mirroring ExecuteTaskService.GenerateMuxJob.
func TestPartsAppendOptions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		parts      Parts
		codec      string
		wantOutput string
		wantSlices []string
		wantAppend []string
	}{
		{
			name: "two parts",
			parts: Parts{Slices: []Part{
				{File: `D:\work\ep01_part0.mkv`},
				{File: `D:\work\ep01_part1.mkv`},
			}},
			codec:      "MKV",
			wantOutput: `D:\work\ep01_all.mkv`,
			wantSlices: []string{`D:\work\ep01_part0.mkv`, `D:\work\ep01_part1.mkv`},
			wantAppend: []string{
				"--ui-language", "en", "--output", `D:\work\ep01_all.mkv`,
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", `D:\work\ep01_part0.mkv`, ")",
				"+", "(", `D:\work\ep01_part1.mkv`, ")",
				"--append-to", "1:0:0:0",
			},
		},
		{
			name: "three parts",
			parts: Parts{Slices: []Part{
				{File: `D:\work\ep01_part0.mkv`},
				{File: `D:\work\ep01_part1.mkv`},
				{File: `D:\work\ep01_part2.mkv`},
			}},
			codec:      "MKV",
			wantOutput: `D:\work\ep01_all.mkv`,
			wantSlices: []string{`D:\work\ep01_part0.mkv`, `D:\work\ep01_part1.mkv`, `D:\work\ep01_part2.mkv`},
			wantAppend: []string{
				"--ui-language", "en", "--output", `D:\work\ep01_all.mkv`,
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", `D:\work\ep01_part0.mkv`, ")",
				"+", "(", `D:\work\ep01_part1.mkv`, ")",
				"+", "(", `D:\work\ep01_part2.mkv`, ")",
				"--append-to", "1:0:0:0,2:0:1:0",
			},
		},
		{
			// The codec string is lowercased into the extension; MKA jobs
			// produce "ep01_all.mka".
			name: "codec string is lowercased",
			parts: Parts{Slices: []Part{
				{File: "a.mkv"},
				{File: "b.mkv"},
			}},
			codec:      "MKA",
			wantOutput: `D:\work\ep01_all.mka`,
			wantSlices: []string{"a.mkv", "b.mkv"},
			wantAppend: []string{
				"--ui-language", "en", "--output", `D:\work\ep01_all.mka`,
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", "a.mkv", ")",
				"+", "(", "b.mkv", ")",
				"--append-to", "1:0:0:0",
			},
		},
		{
			// A single part still goes through the append step, and the
			// resulting file is the one the episode mux consumes.
			name: "single part",
			parts: Parts{Slices: []Part{
				{File: "only.mkv"},
			}},
			codec:      "MKV",
			wantOutput: `D:\work\ep01_all.mkv`,
			wantSlices: []string{"only.mkv"},
			wantAppend: []string{
				"--ui-language", "en", "--output", `D:\work\ep01_all.mkv`,
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"(", "only.mkv", ")",
				"--append-to",
			},
		},
		{
			// No parts at all keeps the legacy quirk: the flag is emitted with
			// an empty value and mkvmerge rejects the command line.
			name:       "no parts",
			parts:      Parts{},
			codec:      "MKV",
			wantOutput: `D:\work\ep01_all.mkv`,
			wantSlices: []string{},
			wantAppend: []string{
				"--ui-language", "en", "--output", `D:\work\ep01_all.mkv`,
				"--no-audio", "--no-subtitles", "--no-buttons", "--no-track-tags",
				"--no-chapters", "--no-attachments", "--no-global-tags",
				"--default-track", "0:1", "--language", "0:und",
				"--append-to",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := tc.parts.AppendOptions(mkvmergePath, `D:\work\ep01`, tc.codec, 0)
			if got.Output != tc.wantOutput {
				t.Errorf("Output = %q, want %q", got.Output, tc.wantOutput)
			}
			if !slices.Equal(got.Slices, tc.wantSlices) {
				t.Errorf("Slices = %q, want %q", got.Slices, tc.wantSlices)
			}
			// The options are the input of the append step, so the command
			// line that step builds is asserted as well.
			assertArgs(t, simple.AppendVideoArgs(got), tc.wantAppend)
		})
	}
}

// TestNames pins the two processor names. The MKA run is distinguishable from
// the final mux in logs and status events, matching the two legacy status
// strings "封装MKA中" and "最终封装中".
func TestNames(t *testing.T) {
	t.Parallel()

	mkv := New(Options{Media: &model.MediaFile{}})
	if got := mkv.Name(); got != Name {
		t.Errorf("mkv Name() = %q, want %q", got, Name)
	}
	mka := New(Options{Media: &model.MediaFile{}, Container: mkvmerge.ContainerMKA})
	if got := mka.Name(); got != NameMKA {
		t.Errorf("mka Name() = %q, want %q", got, NameMKA)
	}
}

// TestArgsAccessorReturnsCopy guards against callers mutating the stored argv.
func TestArgsAccessorReturnsCopy(t *testing.T) {
	t.Parallel()
	p := New(Options{
		Output: "out.mkv",
		Media:  &model.MediaFile{Video: videoTrack("v.hevc", "")},
	})
	got := p.Args()
	if len(got) == 0 {
		t.Fatal("Args() is empty")
	}
	got[0] = "mutated"
	if p.Args()[0] == "mutated" {
		t.Fatal("Args() exposed the internal slice")
	}
}

// TestRunRejectsNilMedia checks the guard: mkvmerge is never started for a job
// without tracks, so a bogus path never gets a chance to fail with a less
// useful message.
func TestRunRejectsNilMedia(t *testing.T) {
	t.Parallel()
	p := New(Options{Mkvmerge: "/definitely/not/here/mkvmerge"})
	err := p.Run(t.Context(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a config error")
	}
	e := okerr.AsError(err)
	if e.Kind != okerr.KindConfig {
		t.Errorf("Kind = %q, want %q", e.Kind, okerr.KindConfig)
	}
	if e.Summary != "封装任务不完整" {
		t.Errorf("Summary = %q, want %q", e.Summary, "封装任务不完整")
	}
}

// TestLifecycleBeforeStart ensures the control methods are safe no-ops when no
// child process has been started yet.
func TestLifecycleBeforeStart(t *testing.T) {
	t.Parallel()
	p := New(Options{Media: &model.MediaFile{}})
	if err := p.Close(); err != nil {
		t.Errorf("Close() = %v, want nil", err)
	}
	if err := p.Pause(); err != nil {
		t.Errorf("Pause() = %v, want nil", err)
	}
	if err := p.Resume(); err != nil {
		t.Errorf("Resume() = %v, want nil", err)
	}
	if err := p.SetPriority(jobproc.PriorityHigh); err != nil {
		t.Errorf("SetPriority() = %v, want nil", err)
	}
}

// TestRenderedMessageMatchesLegacy pins the operator-facing text for a failure
// of mkvmerge itself. The legacy ExceptionParser formatted
// Constants.mmgErrorMsg with the text after "Error: " and the input file, and
// okerr.Render must reproduce it; the summary stays ErrMkvmerge so the template
// is selected.
func TestRenderedMessageMatchesLegacy(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "Error: The file 'x.mkv' could not be opened for reading: open file error.")
	t.Setenv(helperExitCode, "2")

	p := New(Options{
		Mkvmerge: os.Args[0],
		Output:   "out.mkv",
		Media:    &model.MediaFile{Video: videoTrack("v.hevc", "")},
	})
	err := p.Run(t.Context(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a failure")
	}
	if !errors.Is(err, okerr.ErrMkvmerge) {
		t.Fatalf("errors.Is(err, ErrMkvmerge) = false for %v", err)
	}
	got := okerr.Render(okerr.AsError(err).WithFile("C:/in/00001.m2ts"))
	want := "mkvmerge出错: The file 'x.mkv' could not be opened for reading: open file error." +
		"。该文件C:/in/00001.m2ts将跳过处理。请转告技术总监复查。"
	if got != want {
		t.Errorf("rendered message =\n  %q\nwant\n  %q", got, want)
	}
}

// TestRunForwardsProgress drives the real process path with a fake mkvmerge and
// checks that the episode mux reports progress through the sink.
func TestRunForwardsProgress(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "Progress: 42%\nMultiplexing took 0 seconds.")
	t.Setenv(helperExitCode, "0")

	p := New(Options{
		Mkvmerge: os.Args[0],
		Output:   "out.mkv",
		Media: &model.MediaFile{
			Video: videoTrack("v.hevc", ""),
			AudioTracks: []*model.AudioTrack{
				audioTrack("a.flac", model.MuxOptionDefault, "jpn", ""),
			},
		},
	})
	var percents []float64
	err := p.Run(t.Context(), jobproc.ProgressFunc(func(pr jobproc.Progress) {
		percents = append(percents, pr.Percent)
	}))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	want := []float64{42, 100}
	if !slices.Equal(percents, want) {
		t.Errorf("reported percents = %v, want %v", percents, want)
	}
}

// TestRunExitCode pins what this package guarantees about mkvmerge's exit
// codes: 0 succeeds and 2 (mkvmerge's "error") fails with the legacy summary.
//
// Exit code 1 is deliberately not asserted here. mkvmerge uses 1 for warnings
// and the legacy onExited only treated 2 as a failure, but the exit-code
// decision lives in internal/jobproc/mux/mkvmerge (A10) and currently turns 1
// into an error as well; this package delegates to it. See the hand-off note in
// the package report.
func TestRunExitCode(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "Multiplexing took 0 seconds.")

	cases := []struct {
		code    string
		wantErr bool
	}{
		{"0", false},
		{"2", true},
	}
	for _, tc := range cases {
		t.Run("exit "+tc.code, func(t *testing.T) {
			t.Setenv(helperExitCode, tc.code)
			p := New(Options{
				Mkvmerge: os.Args[0],
				Output:   "out.mkv",
				Media:    &model.MediaFile{Video: videoTrack("v.hevc", "")},
			})
			err := p.Run(t.Context(), jobproc.NopSink{})
			if tc.wantErr && err == nil {
				t.Fatal("Run() = nil, want a failure")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Run() = %v, want nil", err)
			}
			if tc.wantErr {
				e := okerr.AsError(err)
				if e.ExitCode != 2 {
					t.Errorf("ExitCode = %d, want 2", e.ExitCode)
				}
				if !errors.Is(err, okerr.ErrMkvmerge) {
					t.Errorf("errors.Is(err, ErrMkvmerge) = false for %v", err)
				}
			}
		})
	}
}

func TestRunMissingToolFails(t *testing.T) {
	t.Parallel()
	p := New(Options{
		Mkvmerge: "/definitely/not/here/mkvmerge",
		Output:   "out.mkv",
		Media:    &model.MediaFile{Video: videoTrack("v.hevc", "")},
	})
	err := p.Run(t.Context(), jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a not-found error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindNotFound {
		t.Errorf("Kind = %q, want %q", got, okerr.KindNotFound)
	}
}

func TestRunHonoursCancellation(t *testing.T) {
	t.Setenv(helperEnvVar, "1")
	t.Setenv(helperScript, "")
	t.Setenv(helperExitCode, "0")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	p := New(Options{
		Mkvmerge: os.Args[0],
		Output:   "out.mkv",
		Media:    &model.MediaFile{Video: videoTrack("v.hevc", "")},
	})
	err := p.Run(ctx, jobproc.NopSink{})
	if err == nil {
		t.Fatal("Run() = nil, want a cancellation error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindCanceled {
		t.Errorf("Kind = %q, want %q", got, okerr.KindCanceled)
	}
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if slices.Equal(got, want) {
		return
	}
	t.Fatalf("args mismatch\n got: %q\nwant: %q", got, want)
}

// The test below drives a real child process: mkvmerge is replaced by this test
// binary re-executed with a sentinel environment variable. TestMain runs before
// the testing package parses its flags, so the child sees the exact argv the
// processor built.
const (
	helperEnvVar   = "OKEGUIDX_MKVEPISODE_HELPER"
	helperScript   = "OKEGUIDX_MKVEPISODE_HELPER_SCRIPT"
	helperExitCode = "OKEGUIDX_MKVEPISODE_HELPER_EXIT"
)

// TestMain doubles as the fake mkvmerge.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnvVar) == "1" {
		os.Exit(helperMain())
	}
	os.Exit(m.Run())
}

func helperMain() int {
	for _, line := range strings.Split(os.Getenv(helperScript), "\n") {
		if line == "" {
			continue
		}
		// A leading '!' marks a line that goes to stderr.
		if rest, ok := strings.CutPrefix(line, "!"); ok {
			fmt.Fprintln(os.Stderr, rest)
			continue
		}
		fmt.Println(line)
	}
	code, _ := strconv.Atoi(os.Getenv(helperExitCode))
	return code
}
