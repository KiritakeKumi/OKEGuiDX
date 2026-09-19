package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// This file holds the one test that drives real external tools. Everything else
// in the suite fakes them, because a full encode takes minutes; the point here
// is the part the fakes cannot reach: that a real `vspipe --info` output and a
// real `ffprobe` report survive the pipeline's parsing, its frame-rate check and
// its track reconciliation.
//
// The two real tools are VapourSynth's vspipe and ffmpeg. The encoder and the
// muxer stay fake, because the suite must not depend on an encoder being
// installed and because their wrappers already have their own real-process
// tests (internal/jobproc/video/*). The source file is synthesised with ffmpeg,
// so the test carries no fixture and skips when the tools are missing.

// realToolPaths locates vspipe and ffmpeg on this machine, or reports why not.
func realToolPaths(t *testing.T) (vspipe, ffmpeg, ffprobe string) {
	t.Helper()

	candidates := []string{
		`C:\Program Files (x86)\VapourSynth\core64`,
		`C:\Program Files\VapourSynth\core64`,
	}
	for _, dir := range candidates {
		if vspipe == "" {
			p := filepath.Join(dir, "vspipe.exe")
			if fileExists(p) {
				vspipe = p
			}
		}
		if ffmpeg == "" {
			p := filepath.Join(dir, "ffmpeg.exe")
			if fileExists(p) {
				ffmpeg = p
			}
		}
		if ffprobe == "" {
			p := filepath.Join(dir, "ffprobe.exe")
			if fileExists(p) {
				ffprobe = p
			}
		}
	}
	if vspipe == "" || ffmpeg == "" || ffprobe == "" {
		if p, err := exec.LookPath("vspipe"); err == nil {
			vspipe = p
		}
		if p, err := exec.LookPath("ffmpeg"); err == nil {
			ffmpeg = p
		}
		if p, err := exec.LookPath("ffprobe"); err == nil {
			ffprobe = p
		}
	}
	if vspipe == "" || ffmpeg == "" || ffprobe == "" {
		t.Skipf("real tools not installed: vspipe=%q ffmpeg=%q ffprobe=%q", vspipe, ffmpeg, ffprobe)
	}
	return vspipe, ffmpeg, ffprobe
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// makeSyntheticSource writes a short MKV with one video and one AC3 audio track.
// Five frames at 5 fps and a quarter second of tone is enough for every check
// the pipeline performs and costs well under a second to produce.
func makeSyntheticSource(t *testing.T, ffmpeg, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "00001.m2ts.mkv")
	cmd := exec.Command(ffmpeg,
		"-y", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=64x48:rate=5:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:v", "mpeg2video", "-c:a", "ac3", "-shortest",
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot synthesise a source with ffmpeg: %v\n%s", err, out)
	}
	return path
}

// writeBlankScript writes a .vpy whose output is a five-frame clip at 5 fps, so
// `vspipe --info` reports a rate the profile can declare. It needs no source
// plugin, which keeps the test independent of L-SMASH being installed.
//
// The format is YUV420P8: vspipe's --y4m refuses anything else, and the pipeline
// always asks for y4m.
func writeBlankScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "blank.vpy")
	body := "import vapoursynth as vs\n" +
		"core = vs.core\n" +
		"# OKE:INPUTFILE arg=r\"source\"\n" +
		"clip = core.std.BlankClip(width=64, height=48, format=vs.YUV420P8, " +
		"fpsnum=5, fpsden=1, length=5)\n" +
		"clip.set_output()\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

// TestPipelineRealVSPipeInfoAndFFmpegDemux is the end-to-end smoke test: a real
// vspipe reports the script's properties and a real ffprobe/ffmpeg pair extracts
// the source's audio track, while the encoder and muxer are the test binary.
func TestPipelineRealVSPipeInfoAndFFmpegDemux(t *testing.T) {
	vspipe, ffmpeg, ffprobe := realToolPaths(t)
	dir := t.TempDir()

	source := makeSyntheticSource(t, ffmpeg, dir)
	script := writeBlankScript(t, dir)

	// The fake tools cover the encoder and the muxer; vspipe and ffmpeg are
	// overridden with the real binaries below.
	ft := newFakeTools(t, dir, capsWith(), normalSpec())
	ft.caps.Tools[toolchain.ToolVSPipe] = node.ToolInfo{Path: vspipe}
	ft.caps.Tools[toolchain.ToolFFmpeg] = node.ToolInfo{Path: ffmpeg}
	ft.caps.Tools[toolchain.ToolFFprobe] = node.ToolInfo{Path: ffprobe}

	prof := makeProfile(dir, func(p *profile.Profile) {
		p.InputScript = script
		// The script's BlankClip is 5 fps, and the source's audio is AC3 which
		// the profile asks to keep as AC3 (the demuxer's Lossy downgrade only
		// applies to AAC requests, so this exercises the passthrough path).
		p.FpsNum = 5
		p.FpsDen = 1
		p.AudioTracks = []profile.AudioTrackSpec{
			{OutputCodec: "AC3", MuxOption: model.MuxOptionDefault},
		}
	})
	if err := os.MkdirAll(filepath.Dir(prof.WorkingPathPrefix), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(prof.OutputPathPrefix), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw, err := marshalProfile(prof)
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	if err := os.WriteFile(prof.ConfigFilePath, raw, 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	task := &model.Task{
		ID:      model.NewTaskID(),
		Name:    prof.ProjectName,
		Profile: prof,
		Inputs:  []model.FileRef{model.NewFileRef(source)},
	}
	ft.installEnv(t, prof.WorkingPathPrefix, source)

	events, err := runPipeline(t, ft.options(t), task)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if last := events[len(events)-1]; last.Progress != model.TaskFinished {
		t.Errorf("final progress = %v, want FINISHED", last.Progress)
	}

	// The real demuxer extracted the AC3 track under the name eac3to's scheme
	// produces — `{stem(SourceFile)}_{Index}.ac3` — and the pipeline passed it
	// to the muxer. vspipe and ffmpeg are real binaries, so they record no
	// argv; the muxer's command line is what proves the track reached it.
	muxCalls := ft.callsFor(t, roleMkvmerge)
	if len(muxCalls) == 0 {
		t.Fatal("the muxer never ran")
	}
	final := muxCalls[len(muxCalls)-1]
	if !strings.Contains(final, "_2.ac3") {
		t.Errorf("the extracted AC3 track is missing from the muxer command line: %s", final)
	}
	// The extracted file really exists and carries data.
	track := filepath.Join(filepath.Dir(prof.WorkingPathPrefix), "00001.m2ts_2.ac3")
	if info, statErr := os.Stat(track); statErr != nil {
		t.Errorf("the extracted track is missing: %v", statErr)
	} else if info.Size() == 0 {
		t.Errorf("the extracted track is empty: %s", track)
	}
}

// TestPipelineRealVSPipeRejectsMismatchedFps proves the frame-rate check runs
// against a real vspipe report rather than against a fixture.
func TestPipelineRealVSPipeRejectsMismatchedFps(t *testing.T) {
	vspipe, _, _ := realToolPaths(t)
	dir := t.TempDir()

	source := filepath.Join(dir, "00001.m2ts")
	if err := os.WriteFile(source, []byte("not a real file"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	script := writeBlankScript(t, dir)

	ft := newFakeTools(t, dir, capsWith(), normalSpec())
	ft.caps.Tools[toolchain.ToolVSPipe] = node.ToolInfo{Path: vspipe}

	prof := makeProfile(dir, func(p *profile.Profile) {
		p.InputScript = script
		// The script is 5 fps; the profile declares 24.
		p.FpsNum = 24
		p.FpsDen = 1
	})
	task := &model.Task{
		ID:      model.NewTaskID(),
		Profile: prof,
		Inputs:  []model.FileRef{model.NewFileRef(source)},
	}
	ft.installEnv(t, prof.WorkingPathPrefix, source)

	_, err := runPipeline(t, ft.options(t), task)
	if err == nil {
		t.Fatal("Run() = nil, want a frame-rate mismatch from the real script")
	}
	if !strings.Contains(err.Error(), "5.000") || !strings.Contains(err.Error(), "24.000") {
		t.Errorf("error = %v, want both real frame rates", err)
	}
}
