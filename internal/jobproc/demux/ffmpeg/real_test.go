package ffmpeg

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// Real-tool integration tests.
//
// These run the exact argument vectors the plan builds against the ffmpeg on
// this machine, so the shapes are proven to be accepted by the tool rather than
// merely self-consistent. They are skipped when no ffmpeg is available, which is
// the normal case on a CI runner without the tools tree.
//
// The delay assertions are the point of this file: the unit tests pin the
// argument strings, and these pin that those strings actually move the audio to
// the right movie time.

// toolPaths locates ffmpeg and ffprobe for the integration tests. The environment
// wins so a developer can point at the tools tree; otherwise the usual Windows
// install locations are tried.
func toolPaths(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	if p := os.Getenv("OKEGUIDX_TEST_FFMPEG"); p != "" {
		return p, os.Getenv("OKEGUIDX_TEST_FFPROBE")
	}
	for _, dir := range []string{
		`C:\Program Files (x86)\VapourSynth\core64`,
		`C:\Program Files\VapourSynth\core64`,
	} {
		ff := filepath.Join(dir, "ffmpeg.exe")
		fp := filepath.Join(dir, "ffprobe.exe")
		if fileExists(ff) && fileExists(fp) {
			return ff, fp
		}
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		fp, err := exec.LookPath("ffprobe")
		if err == nil {
			return p, fp
		}
	}
	t.Skip("no ffmpeg/ffprobe on this machine; set OKEGUIDX_TEST_FFMPEG to run")
	return "", ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// buildMarkerSource writes an AC3 file whose only sound is a short tone at
// toneSeconds, plus a silent video of the given length.
func buildMarkerSource(t *testing.T, ffmpeg, dir string, toneSeconds, videoSeconds float64) (video, audio string) {
	t.Helper()
	ctx := context.Background()
	video = filepath.Join(dir, "v.mp4")
	audio = filepath.Join(dir, "m.ac3")
	wav := filepath.Join(dir, "m.wav")

	before := formatSeconds(toneSeconds)
	after := formatSeconds(videoSeconds - toneSeconds - 0.2)
	runTool(t, ctx, ffmpeg,
		"-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "aevalsrc=0:d="+before+":s=48000",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=0.2:sample_rate=48000",
		"-f", "lavfi", "-i", "aevalsrc=0:d="+after+":s=48000",
		"-filter_complex", "[0:a][1:a][2:a]concat=n=3:v=0:a=1[o]",
		"-map", "[o]", "-c:a", "pcm_s16le", wav,
	)
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-i", wav,
		"-c:a", "ac3", "-b:a", "192k", "-ar", "48000", audio)
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration="+formatSeconds(videoSeconds)+":size=160x120:rate=25",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", video)
	return video, audio
}

// runTool runs one ffmpeg command and fails the test if it exits non-zero.
func runTool(t *testing.T, ctx context.Context, tool string, args ...string) {
	t.Helper()
	_, err := proc.Run(ctx, proc.Spec{Path: tool, Args: args, Name: "ffmpeg-test"})
	if err != nil {
		t.Fatalf("%s %s: %v", filepath.Base(tool), strings.Join(args, " "), err)
	}
}

// firstToneAt returns where the first burst of sound begins in a file, in
// seconds from its own start.
//
// silencedetect reports the boundaries of silent stretches. A file that opens on
// the burst has no leading "silence_start: 0", so it returns 0; otherwise the
// first "silence_end" is the burst's position.
func firstToneAt(t *testing.T, ctx context.Context, ffmpeg, file string) float64 {
	t.Helper()
	res, err := proc.Run(ctx, proc.Spec{
		Path: ffmpeg,
		Args: []string{"-hide_banner", "-nostats", "-i", file,
			"-af", "silencedetect=noise=-45dB:d=0.05", "-f", "null", "-"},
		Name: "ffmpeg-test",
	})
	if err != nil {
		t.Fatalf("silencedetect %s: %v", file, err)
	}

	var (
		opensSilent bool
		firstEnd    = -1.0
	)
	for _, line := range strings.Split(res.Stderr, "\n") {
		if _, rest, ok := strings.Cut(line, "silence_start: "); ok {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				if v, err := strconv.ParseFloat(fields[0], 64); err == nil && v == 0 {
					opensSilent = true
				}
			}
			continue
		}
		if _, rest, ok := strings.Cut(line, "silence_end: "); ok && firstEnd < 0 {
			fields := strings.Fields(rest)
			if len(fields) > 0 {
				if v, err := strconv.ParseFloat(fields[0], 64); err == nil {
					firstEnd = v
				}
			}
		}
	}
	if !opensSilent {
		return 0
	}
	return firstEnd
}

// TestRealDelayCompensation is the numerical proof of the delay model.
//
// For a source where the audio sits one second after the video, a track aligned
// to video t=0 must have its tone one second later than the source's. Both the
// copy path (pad + concat) and the decoded path (adelay) are measured, and the
// decoded path is additionally required to be exact.
func TestRealDelayCompensation(t *testing.T) {
	if testing.Short() {
		t.Skip("real-tool test")
	}
	ffmpeg, _ := toolPaths(t)
	ctx := helperCtx(t)
	dir := t.TempDir()

	video, audio := buildMarkerSource(t, ffmpeg, dir, 1.0, 8)

	// The source's tone is at 1.0 s of its own content, and the container gives
	// the audio a +1000 ms delay. The extracted track must therefore have its
	// tone one second in, so that muxing it at t=0 puts it where the container
	// had it.
	src := filepath.Join(dir, "pos.mkv")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-i", video,
		"-itsoffset", "1.0", "-i", audio,
		"-map", "0:v", "-map", "1:a", "-c", "copy", "-t", "8", "-f", "matroska", src)

	// A plain extraction drops the offset, which is the bug being compensated.
	unfixed := filepath.Join(dir, "unfixed.ac3")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-i", src,
		"-map", "0:a:0", "-c", "copy", "-f", "ac3", unfixed)
	if got := firstToneAt(t, ctx, ffmpeg, unfixed); !closeTo(got, 1.0, 0.04) {
		t.Fatalf("uncompensated tone at %v, want ~1.0 (the offset is dropped)", got)
	}

	// The copy path: body, silent pad, concat. The pad must restore the second.
	body := filepath.Join(dir, "out.body")
	pad := filepath.Join(dir, "out.pad")
	list := filepath.Join(dir, "out.concat")
	out := filepath.Join(dir, "out.ac3")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-i", src,
		"-map", "0:a:0", "-c", "copy", "-f", "ac3", body)
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono",
		"-t", "1.000000", "-c:a", "ac3", "-b:a", "192000", "-ar", "48000", "-ac", "1",
		"-f", "ac3", pad)
	if err := writeConcatList(list, []string{pad, body}); err != nil {
		t.Fatal(err)
	}
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-f", "concat", "-safe", "0",
		"-i", list, "-c", "copy", "-f", "ac3", out)

	// AC3 frames are 32 ms, so the pad overshoots by at most one frame.
	got := firstToneAt(t, ctx, ffmpeg, out)
	if !closeTo(got, 2.0, 0.04) {
		t.Errorf("copy-path tone at %v, want ~2.0 (within one AC3 frame)", got)
	}

	// The decoded path must be exact.
	decoded := filepath.Join(dir, "out.flac")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-i", src,
		"-map", "0:a:0", "-c:a", "flac", "-af", "adelay=48000S:all=1", "-f", "flac", decoded)
	got = firstToneAt(t, ctx, ffmpeg, decoded)
	if !closeTo(got, 2.0, 0.01) {
		t.Errorf("decoded tone at %v, want 2.0 (sample-exact)", got)
	}
}

// TestRealNegativeDelayCompensation proves the trim path. The audio starts a
// second before the video, so the first second of its content is not part of the
// movie and must be gone.
func TestRealNegativeDelayCompensation(t *testing.T) {
	if testing.Short() {
		t.Skip("real-tool test")
	}
	ffmpeg, _ := toolPaths(t)
	ctx := helperCtx(t)
	dir := t.TempDir()

	video, audio := buildMarkerSource(t, ffmpeg, dir, 1.0, 8)

	// video at +1.0, audio at 0 => delay = -1000 ms. The tone at 1.0 s of the
	// audio is at 0.0 s of movie time, so the aligned file must open on it.
	src := filepath.Join(dir, "neg.mkv")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-itsoffset", "1.0", "-i", video,
		"-i", audio, "-map", "0:v", "-map", "1:a", "-c", "copy", "-t", "8", "-f", "matroska", src)

	// The copy path drops whole packets.
	copied := filepath.Join(dir, "copy.ac3")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-i", src,
		"-map", "0:a:0", "-c", "copy", "-ss", "1.000000", "-f", "ac3", copied)
	if got := firstToneAt(t, ctx, ffmpeg, copied); !closeTo(got, 0.0, 0.04) {
		t.Errorf("copy-path tone at %v, want ~0.0 (within one AC3 frame)", got)
	}

	// The decoded path is sample-exact.
	decoded := filepath.Join(dir, "dec.flac")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-i", src,
		"-map", "0:a:0", "-c:a", "flac",
		"-af", "atrim=start_sample=48000,asetpts=PTS-STARTPTS", "-f", "flac", decoded)
	if got := firstToneAt(t, ctx, ffmpeg, decoded); !closeTo(got, 0.0, 0.01) {
		t.Errorf("decoded tone at %v, want 0.0 (sample-exact)", got)
	}
}

// TestRealMkvRoundTripIsBitExact pins the claim that makes the ffmpeg demuxer
// viable for Matroska without a separate mkvextract path: a `-c copy` extraction
// returns the identical elementary stream, byte for byte.
func TestRealMkvRoundTripIsBitExact(t *testing.T) {
	if testing.Short() {
		t.Skip("real-tool test")
	}
	ffmpeg, _ := toolPaths(t)
	ctx := helperCtx(t)
	dir := t.TempDir()

	original := filepath.Join(dir, "orig.ac3")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=5:sample_rate=48000",
		"-c:a", "ac3", "-b:a", "448k", "-ar", "48000", "-ac", "6", "-f", "ac3", original)

	video := filepath.Join(dir, "v.mp4")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=5:size=160x120:rate=25",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", video)

	wrapped := filepath.Join(dir, "wrap.mkv")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-i", video, "-i", original,
		"-map", "0:v", "-map", "1:a", "-c", "copy", "-t", "5", "-f", "matroska", wrapped)

	extracted := filepath.Join(dir, "extracted.ac3")
	runTool(t, ctx, ffmpeg, "-y", "-loglevel", "error", "-i", wrapped,
		"-map", "0:a:0", "-c", "copy", "-f", "ac3", extracted)

	want, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(extracted)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("extracted %d bytes, want %d", len(got), len(want))
	}
	if string(want) != string(got) {
		t.Error("the MKV round trip is not bit-exact; mkvextract would be needed after all")
	}
}

// closeTo reports whether got is within tolerance of want.
func closeTo(got, want, tolerance float64) bool {
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= tolerance
}
