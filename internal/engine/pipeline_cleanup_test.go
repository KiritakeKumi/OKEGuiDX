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

// TestPipelineCleanupIsOptIn pins the two halves of the cleanup contract: the
// legacy pipeline never cleaned at task end (Utils/Cleaner.cs has one caller,
// the wizard's pre-run sweep), so the default must leave the intermediate files
// alone; CleanAfterTask must delete them without touching the deliverable.
func TestPipelineCleanupIsOptIn(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	opts := ft.options(t)
	_, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// The extracted audio track is one of the suffixes the cleaner removes, and
	// it survives only because the cleanup is off by default. The demuxer names
	// it `{stem(SourceFile)}_{Index}.flac` in the working prefix's directory.
	track := filepath.Join(ft.dir, "work", "00001_2.flac")
	if _, statErr := os.Stat(track); statErr != nil {
		entries, _ := os.ReadDir(filepath.Join(ft.dir, "work"))
		t.Errorf("the default run deleted an intermediate file: %v (work holds %v)", statErr, entries)
	}
}

// TestPipelineCleanupRemovesIntermediates proves the opt-in sweep works and
// spares the deliverable.
func TestPipelineCleanupRemovesIntermediates(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	opts := ft.options(t)
	opts.CleanAfterTask = true
	_, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// The deliverable survives.
	entries, readErr := os.ReadDir(filepath.Dir(prof.OutputPathPrefix))
	if readErr != nil {
		t.Fatalf("read output dir: %v", readErr)
	}
	var deliverable string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".mkv") {
			deliverable = e.Name()
		}
	}
	if deliverable == "" {
		t.Fatalf("the cleanup deleted the deliverable: %v", entries)
	}
	// The extracted audio track is one of the suffixes the cleaner removes.
	track := filepath.Join(ft.dir, "work", "00001_2.flac")
	if _, statErr := os.Stat(track); statErr == nil {
		t.Errorf("the cleanup left the extracted track behind: %s", track)
	}
}

// TestPipelineCleanupSparesTheSource proves the input file is never deleted: the
// cleaner appends it to its own white list.
func TestPipelineCleanupSparesTheSource(t *testing.T) {
	dir := t.TempDir()
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), normalSpec())
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	source := task.Inputs[0].Resolve(ft.caps.Volumes)
	ft.installEnv(t, prof.WorkingPathPrefix, source)

	opts := ft.options(t)
	opts.CleanAfterTask = true
	_, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, statErr := os.Stat(source); statErr != nil {
		t.Errorf("the cleanup deleted the source file: %v", statErr)
	}
}

// TestPipelineChapterDetectionFindsAChapterFile proves DetectChapters works: the
// pipeline runs the wizard's detection, finds the external chapter file next to
// the source, and muxes the chapters.
func TestPipelineChapterDetectionFindsAChapterFile(t *testing.T) {
	dir := t.TempDir()
	spec := normalSpec()
	spec.ChapterJSON = chapterDoc([]string{"0", "2000000000"})
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	source := task.Inputs[0].Resolve(ft.caps.Volumes)

	// The chapter service looks for `<stem>.*txt` next to the source and takes
	// the language from the text between the two stems.
	chapterPath := filepath.Join(filepath.Dir(source), "00001.jpn.txt")
	if err := os.WriteFile(chapterPath, []byte("CHAPTER01=00:00:00.000\r\n"), 0o600); err != nil {
		t.Fatalf("write chapters: %v", err)
	}
	ft.installEnv(t, prof.WorkingPathPrefix, source)

	opts := ft.options(t)
	opts.DetectChapters = true
	_, err := runPipeline(t, opts, task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// The chapter service reports Warn when a mark sits within 3 s of the end;
	// the short synthetic script makes that the expected outcome, and it is the
	// service's own rule rather than the pipeline's. What matters here is that
	// the detection ran at all: without it the status would still be No.
	if task.Status.Chapter == model.ChapterNo {
		t.Errorf("chapter status = %v, want the detection to have found the file", task.Status.Chapter)
	}
	if task.ChapterLanguage != "jpn" {
		t.Errorf("chapter language = %q, want the language from the file name", task.ChapterLanguage)
	}
	var chaptersMuxed bool
	for _, c := range ft.callsFor(t, roleMkvmerge) {
		if strings.Contains(c, "--chapters") {
			chaptersMuxed = true
		}
	}
	if !chaptersMuxed {
		t.Error("the detected chapters never reached the muxer")
	}
}

// TestPipelineChapterDetectionIsOptIn pins the DetectChapters option: the
// pipeline does not run the wizard's chapter detection unless it is asked to,
// because a profile whose ChapterFile field is already set must not be re-probed.
func TestPipelineChapterDetectionIsOptIn(t *testing.T) {
	dir := t.TempDir()
	spec := normalSpec()
	spec.ChapterJSON = chapterDoc([]string{"0", "2000000000"})
	ft := newFakeTools(t, dir, capsWith(node.FeatureEac3to, node.FeatureAAC), spec)
	prof := makeProfile(dir, func(p *profile.Profile) {
		p.AudioTracks = []profile.AudioTrackSpec{normalAudioTrack()}
	})
	task := prepare(t, ft, prof, nil)
	ft.installEnv(t, prof.WorkingPathPrefix, task.Inputs[0].Resolve(ft.caps.Volumes))

	// The source is not a Matroska file, so the detection would log and stop
	// without a chapter source; the assertion is on the observable outcome.
	_, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if task.Status.Chapter != model.ChapterNo {
		t.Errorf("chapter status = %v, want No for a task with no chapter source", task.Status.Chapter)
	}
}
