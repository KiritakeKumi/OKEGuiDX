package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// TestPipelineVFRWritesTimecode pins the VFR path of DoPreparation: a v2
// timecode file is written next to the source and handed to the muxer.
func TestPipelineVFRWritesTimecode(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.TimeCode = true
		// A VFR job skips the frame-rate check, so the profile's rate may
		// differ from the script's.
		p.FpsNum = 1
		p.FpsDen = 1
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	source := task.Inputs[0].Resolve(ft.caps.Volumes)

	// The timecode source is `Path.ChangeExtension(InputFile, ".tcfile")`.
	tcPath := replaceExt(source, ".tcfile")
	body := "# timecode format v2\n" +
		"0.000000\n20.000000\n40.000000\n60.000000\n80.000000\n"
	if err := os.WriteFile(tcPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write timecode: %v", err)
	}
	ft.installEnv(t, prof.WorkingPathPrefix, source)

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// The corrected file is what the muxer is given.
	corrected := replaceExt(source, ".v2.tcfile")
	if _, statErr := os.Stat(corrected); statErr != nil {
		t.Fatalf("the corrected timecode was not written: %v", statErr)
	}
	muxCalls := ft.callsFor(t, roleMkvmerge)
	if len(muxCalls) == 0 {
		t.Fatal("the muxer never ran")
	}
	final := muxCalls[len(muxCalls)-1]
	if !strings.Contains(final, "--timestamps") {
		t.Errorf("the muxer was not given the timecode: %s", final)
	}
	if !strings.Contains(final, ".v2.tcfile") {
		t.Errorf("the muxer was given the wrong timecode file: %s", final)
	}
}

// TestPipelineVFRRejectsVariableScript pins the one case the vspipe-info
// processor refuses outright: a script whose own output is variable-rate.
func TestPipelineVFRRejectsVariableScript(t *testing.T) {
	dir := t.TempDir()
	spec := normalSpec()
	spec.VSPipeInfo = "Width: 1920\nHeight: 1080\nFrames: 100\nFPS: Variable (23.976 fps)\n"
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.TimeCode = true
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	_, err := runPipeline(t, ft.options(t), task)
	if err == nil {
		t.Fatal("Run() = nil, want a VFR rejection")
	}
	if !strings.Contains(err.Error(), "VFR") {
		t.Errorf("error = %v, want it to name VFR", err)
	}
}

// TestPipelineChapterFileIsWrittenAndMuxed pins the chapter half of
// DoPreparation: the loaded list is written as OGM next to the working prefix
// and handed to the muxer.
func TestPipelineChapterFileIsWrittenAndMuxed(t *testing.T) {
	dir := t.TempDir()
	spec := normalSpec()
	// The chapter service parses through the tchapter CLI; the fake returns one
	// mark at the start and one at two seconds.
	spec.ChapterJSON = chapterDoc([]string{"0", "2000000000"})
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	source := task.Inputs[0].Resolve(ft.caps.Volumes)

	// The chapter service looks for `<stem>.*txt` next to the source.
	chapterPath := filepath.Join(filepath.Dir(source), "00001.jpn.txt")
	body := "CHAPTER01=00:00:00.000\r\nCHAPTER01NAME=Opening\r\n" +
		"CHAPTER02=00:00:02.000\r\nCHAPTER02NAME=Part A\r\n"
	if err := os.WriteFile(chapterPath, []byte(body), 0o600); err != nil {
		t.Fatalf("write chapters: %v", err)
	}
	task.Status.Chapter = model.ChapterYes
	ft.installEnv(t, prof.WorkingPathPrefix, source)

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	written := replaceExt(prof.WorkingPathPrefix, ".txt")
	if _, statErr := os.Stat(written); statErr != nil {
		t.Fatalf("the OGM chapter file was not written: %v", statErr)
	}
	muxCalls := ft.callsFor(t, roleMkvmerge)
	if len(muxCalls) == 0 {
		t.Fatal("the muxer never ran")
	}
	final := muxCalls[len(muxCalls)-1]
	if !strings.Contains(final, "--chapters") {
		t.Errorf("the muxer was not given the chapters: %s", final)
	}
	if !strings.Contains(final, "--chapter-language") {
		t.Errorf("the muxer was not given the chapter language: %s", final)
	}
}

// TestPipelineNoChapterSourceSkipsChapters pins the `return null` path of
// LoadChapter: a task with no chapter source muxes without one.
func TestPipelineNoChapterSourceSkipsChapters(t *testing.T) {
	ft, opts, task := newNormalPipeline(t, nil)
	task.Status.Chapter = model.ChapterNo
	_, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, c := range ft.callsFor(t, roleMkvmerge) {
		if strings.Contains(c, "--chapters") {
			t.Errorf("the muxer was given chapters for a chapterless task: %s", c)
		}
	}
}

// TestPipelineReEncodeCarriesChapterQPFile pins the per-part qpfile: only the
// chapter marks inside a part survive, rebased onto the part's own numbering.
func TestPipelineReEncodeCarriesChapterQPFile(t *testing.T) {
	dir := t.TempDir()
	spec := reEncodeSpec()
	// A mark at five seconds is frame 120, which the alignment puts inside the
	// encoded part [100, 300).
	spec.ChapterJSON = chapterDoc([]string{"0", "5000000000"})
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	oldFile := filepath.Join(dir, "old.mkv")
	cfg := &profile.EpisodeConfig{
		EnableReEncode:     true,
		ReEncodeOldFile:    oldFile,
		ReEncodeSliceArray: []model.SliceInfo{{Begin: 150, End: 250}},
	}
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.IsReEncode = true
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, cfg)
	source := task.Inputs[0].Resolve(ft.caps.Volumes)
	chapterPath := filepath.Join(filepath.Dir(source), "00001.jpn.txt")
	if err := os.WriteFile(chapterPath, []byte("CHAPTER01=00:00:00.000\r\n"), 0o600); err != nil {
		t.Fatalf("write chapters: %v", err)
	}
	task.Status.Chapter = model.ChapterYes
	ft.installEnv(t, prof.WorkingPathPrefix, source)

	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// The part qpfile exists and holds the rebased frame number: 120 - 100 = 20.
	partQP := filepath.Join(dir, "work", "ep01_part1.qpf")
	raw, err := os.ReadFile(partQP)
	if err != nil {
		t.Fatalf("the part qpfile was not written: %v", err)
	}
	if !strings.Contains(string(raw), "20 I") {
		t.Errorf("part qpfile = %q, want the rebased frame 20", raw)
	}
	// The encoder was told to use it.
	calls := ft.callsFor(t, roleEncoder)
	if len(calls) != 1 {
		t.Fatalf("the encoder ran %d times, want 1", len(calls))
	}
	if !strings.Contains(calls[0], "_part1.qpf") {
		t.Errorf("the encoder was not given the part qpfile: %s", calls[0])
	}
}
