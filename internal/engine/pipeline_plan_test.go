package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/chapter"
	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc/video/iframe"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// TestCheckReEncodeSlices pins the I-frame alignment, which is the whole point
// of the re-encode feature. Every case is derived from the C# logic:
// FindNearestLeft for the start, FindNearestRight for the end, then Merge.
func TestCheckReEncodeSlices(t *testing.T) {
	// An index with I-frames at 0, 100, 250, 400 and the script's frame count.
	index := iframe.IFrameInfo{0, 100, 250, 400, 1000}

	cases := []struct {
		name   string
		in     []model.SliceInfo
		want   []model.SliceInfo
		errIs  error
		errSub string
	}{
		{
			name: "exact I-frame boundaries are kept",
			in:   []model.SliceInfo{{Begin: 100, End: 250}},
			want: []model.SliceInfo{{Begin: 100, End: 250}},
		},
		{
			name: "boundaries move outwards to the nearest I-frame",
			in:   []model.SliceInfo{{Begin: 150, End: 300}},
			want: []model.SliceInfo{{Begin: 100, End: 400}},
		},
		{
			name: "an open end becomes the frame count",
			in:   []model.SliceInfo{{Begin: 500, End: model.OpenEnded}},
			want: []model.SliceInfo{{Begin: 400, End: 1000}},
		},
		{
			name: "touching slices merge",
			in:   []model.SliceInfo{{Begin: 100, End: 250}, {Begin: 250, End: 400}},
			want: []model.SliceInfo{{Begin: 100, End: 400}},
		},
		{
			name: "overlapping slices merge",
			in:   []model.SliceInfo{{Begin: 110, End: 260}, {Begin: 200, End: 390}},
			want: []model.SliceInfo{{Begin: 100, End: 400}},
		},
		{
			name:  "a start at the last frame is illegal",
			in:    []model.SliceInfo{{Begin: 1000, End: 1000}},
			errIs: okerr.ErrReEncodeSlice,
		},
		{
			name:  "an end beyond the frame count is illegal",
			in:    []model.SliceInfo{{Begin: 100, End: 1001}},
			errIs: okerr.ErrReEncodeSlice,
		},
		{
			name:   "an empty index is refused",
			in:     []model.SliceInfo{{Begin: 0, End: 10}},
			errSub: "I 帧序列",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := index
			if tc.errSub != "" {
				idx = nil
			}
			got, err := checkReEncodeSlices(tc.in, idx, "/tmp/00001.m2ts")
			if tc.errIs != nil || tc.errSub != "" {
				if err == nil {
					t.Fatalf("checkReEncodeSlices() = %v, want an error", got)
				}
				if tc.errIs != nil && !errors.Is(err, tc.errIs) {
					t.Errorf("error = %v, want %v", err, tc.errIs)
				}
				if tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
					t.Errorf("error = %v, want it to mention %q", err, tc.errSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("checkReEncodeSlices() error = %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("slice %d = %v, want %v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestPlanParts pins the part layout of GenerateReEncodeJob, which decides the
// `_partN` file names and the order the parts are encoded and muxed in.
func TestPlanParts(t *testing.T) {
	cases := []struct {
		name     string
		slices   []model.SliceInfo
		frames   int64
		wantIDs  []int
		wantEnc  []bool
		wantRang []model.SliceInfo
	}{
		{
			name:     "a slice at the head and one at the tail",
			slices:   []model.SliceInfo{{Begin: 0, End: 100}, {Begin: 200, End: 300}},
			frames:   300,
			wantIDs:  []int{0, 1, 2},
			wantEnc:  []bool{true, false, true},
			wantRang: []model.SliceInfo{{Begin: 0, End: 100}, {Begin: 100, End: 200}, {Begin: 200, End: 300}},
		},
		{
			name:     "a leading gap becomes part 0",
			slices:   []model.SliceInfo{{Begin: 50, End: 150}},
			frames:   150,
			wantIDs:  []int{0, 1},
			wantEnc:  []bool{false, true},
			wantRang: []model.SliceInfo{{Begin: 0, End: 50}, {Begin: 50, End: 150}},
		},
		{
			name:     "a trailing gap becomes the last part",
			slices:   []model.SliceInfo{{Begin: 0, End: 100}},
			frames:   250,
			wantIDs:  []int{0, 1},
			wantEnc:  []bool{true, false},
			wantRang: []model.SliceInfo{{Begin: 0, End: 100}, {Begin: 100, End: 250}},
		},
		{
			name:     "a slice covering everything is the only part",
			slices:   []model.SliceInfo{{Begin: 0, End: 200}},
			frames:   200,
			wantIDs:  []int{0},
			wantEnc:  []bool{true},
			wantRang: []model.SliceInfo{{Begin: 0, End: 200}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &runState{
				opts: &PipelineOptions{Caps: capsWith()},
				p: &profile.Profile{
					VideoFormat:       "HEVC",
					ContainerFormat:   "MKV",
					WorkingPathPrefix: filepath.Join("work", "ep01"),
					IsReEncode:        true,
				},
				cfg:            &profile.EpisodeConfig{ReEncodeSliceArray: tc.slices},
				isReEncode:     true,
				reEncodeSlices: tc.slices,
				frames:         tc.frames,
			}
			if err := st.planParts(); err != nil {
				t.Fatalf("planParts() error = %v", err)
			}
			parts := st.parts
			if len(parts) != len(tc.wantIDs) {
				t.Fatalf("got %d parts, want %d: %+v", len(parts), len(tc.wantIDs), parts)
			}
			for i, p := range parts {
				if p.id != tc.wantIDs[i] {
					t.Errorf("part %d id = %d, want %d", i, p.id, tc.wantIDs[i])
				}
				if p.reEncode != tc.wantEnc[i] {
					t.Errorf("part %d reEncode = %v, want %v", i, p.reEncode, tc.wantEnc[i])
				}
				if p.frames != tc.wantRang[i] {
					t.Errorf("part %d range = %v, want %v", i, p.frames, tc.wantRang[i])
				}
			}
		})
	}
}

// TestPartNaming pins the file names GenerateVideoJob and GenerateMuxJob built.
func TestPartNaming(t *testing.T) {
	cases := []struct {
		name      string
		format    string
		container string
		reEncode  bool
		wantEnc   string
		wantMux   string
	}{
		{
			name: "HEVC re-encode part", format: "HEVC", container: "MKV", reEncode: true,
			wantEnc: "ep01_part1.hevc", wantMux: "ep01_part1.mkv",
		},
		{
			name:   "AVC into an MKV profile writes the container directly",
			format: "AVC", container: "MKV", reEncode: true,
			wantEnc: "ep01_part1_.mkv", wantMux: "ep01_part1.mkv",
		},
		{
			name:   "AVC into an MP4 profile writes a raw stream",
			format: "AVC", container: "MP4", reEncode: true,
			wantEnc: "ep01_part1.h264", wantMux: "ep01_part1.mp4",
		},
		{
			name: "AV1 writes an ivf", format: "AV1", container: "MKV", reEncode: true,
			wantEnc: "ep01_part1.ivf", wantMux: "ep01_part1.mkv",
		},
		{
			name:   "a whole-file encode carries no part number",
			format: "HEVC", container: "MKV", reEncode: false,
			wantEnc: "ep01.hevc", wantMux: "ep01.mkv",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &runState{
				opts: &PipelineOptions{Caps: capsWith()},
				p: &profile.Profile{
					VideoFormat:       tc.format,
					ContainerFormat:   tc.container,
					WorkingPathPrefix: "ep01",
					IsReEncode:        tc.reEncode,
				},
				isReEncode: tc.reEncode,
			}
			var p part
			if tc.reEncode {
				p = part{id: 1, reEncode: true}
			} else {
				p = st.wholeVideoPart()
			}
			if got := st.partOutputPath(p); filepath.Base(got) != tc.wantEnc {
				t.Errorf("partOutputPath() = %q, want %q", got, tc.wantEnc)
			}
			if tc.reEncode {
				if got := st.partMuxPath(p); filepath.Base(got) != tc.wantMux {
					t.Errorf("partMuxPath() = %q, want %q", got, tc.wantMux)
				}
			}
		})
	}
}

// TestEncoderParams pins the pipeline's own additions to the profile's encoder
// parameters, from GenerateVideoJob:415-455.
func TestEncoderParams(t *testing.T) {
	cases := []struct {
		name     string
		format   string
		params   string
		qp       string
		partQP   string
		cores    int
		wantSubs []string
		wantNot  []string
	}{
		{
			name: "x265 gets the NUMA pools mask", format: "HEVC", params: "--preset slow",
			wantSubs: []string{"--pools +"},
		},
		{
			name: "an explicit --pools wins", format: "HEVC", params: "--pools 8",
			wantSubs: []string{"--pools 8"}, wantNot: []string{"--pools 8 --pools"},
		},
		{
			name: "the episode qpfile is appended for HEVC", format: "HEVC", params: "",
			qp: "ep01.qpf", wantSubs: []string{`--qpfile "ep01.qpf"`},
		},
		{
			name: "a part qpfile wins over the episode one", format: "HEVC", params: "",
			qp: "ep01.qpf", partQP: "ep01_part1.qpf",
			wantSubs: []string{`--qpfile "ep01_part1.qpf"`},
		},
		{
			name: "AV1 uses the inline force-key-frames form", format: "AV1", params: "",
			qp: "0f,100f", wantSubs: []string{`--force-key-frames "0f,100f"`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &runState{
				opts: &PipelineOptions{Caps: capsWith(), Numa: newTestNuma(1)},
				p: &profile.Profile{
					VideoFormat:  tc.format,
					EncoderParam: tc.params,
				},
				qpValue: tc.qp,
			}
			got := st.encoderParams(part{qpValue: tc.partQP}, 0)
			for _, want := range tc.wantSubs {
				if !strings.Contains(got, want) {
					t.Errorf("encoderParams() = %q, want it to contain %q", got, want)
				}
			}
			for _, not := range tc.wantNot {
				if strings.Contains(got, not) {
					t.Errorf("encoderParams() = %q, want it not to contain %q", got, not)
				}
			}
		})
	}
}

// TestFinalOutputPath pins TaskDetail.UpdateOutputFileName plus the
// Path.Combine in GenerateMuxJob(mediaOutFile, containerFormat).
func TestFinalOutputPath(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		container string
		outPrefix string
		want      string
	}{
		{
			name:  "mkv keeps the input name",
			input: `D:\work\ep01\00001.m2ts`, container: "MKV",
			outPrefix: `D:\out\ep01`, want: `D:\out\00001.m2ts.mkv`,
		},
		{
			name:  "mp4 lowercases the container",
			input: `D:\work\ep01\00001.m2ts`, container: "MP4",
			outPrefix: `D:\out\ep01`, want: `D:\out\00001.m2ts.mp4`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &runState{
				opts: &PipelineOptions{Caps: capsWith()},
				t:    &model.Task{Inputs: []model.FileRef{model.NewFileRef(tc.input)}},
				p:    &profile.Profile{ContainerFormat: tc.container, OutputPathPrefix: tc.outPrefix},
			}
			if got := st.finalOutputPath(); got != tc.want {
				t.Errorf("finalOutputPath() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestChapterFrames pins ChapterService.GetChapterIFrameInfo for both the CFR
// and the VFR form.
func TestChapterFrames(t *testing.T) {
	chapters := &chapter.Info{
		Chapters: []chapter.Chapter{
			{Number: 1, Time: 0},
			{Number: 2, Time: 5 * time.Second},
			{Number: 3, Time: 10*time.Second + 500*time.Millisecond},
		},
	}
	cases := []struct {
		name   string
		vsInfo model.VSVideoInfo
		tc     *Timecode
		want   []int64
	}{
		{
			name:   "CFR rounds to the nearest frame",
			vsInfo: model.VSVideoInfo{FpsNum: 24, FpsDen: 1, FPS: 24},
			// 5s * 24 = 120; 10.5s * 24 = 252.
			want: []int64{0, 120, 252},
		},
		{
			name:   "a fractional rate uses the reported fps",
			vsInfo: model.VSVideoInfo{FpsNum: 24000, FpsDen: 1001, FPS: 24000.0 / 1001.0},
			// 5s at 23.976 is frame 119.88 -> 120; 10.5s is 251.75 -> 252.
			want: []int64{0, 120, 252},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chapterFrames(chapters, tc.vsInfo, tc.tc)
			if len(got) != len(tc.want) {
				t.Fatalf("chapterFrames() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("frame %d = %d, want %d", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestTimecodeFrameNumbers pins the VFR path: a timestamp is mapped onto a frame
// number through the parsed interval list, which is what makes a VFR chapter
// land on the right frame.
func TestTimecodeFrameNumbers(t *testing.T) {
	dir := t.TempDir()
	// A v2 file with four frames at 25 fps, then one at 50 fps.
	body := "# timecode format v2\n" +
		"0.000000\n40.000000\n80.000000\n120.000000\n160.000000\n180.000000\n"
	path := filepath.Join(dir, "test.tcfile")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write timecode: %v", err)
	}

	tc, err := LoadTimecode(path, 6)
	if err != nil {
		t.Fatalf("LoadTimecode() error = %v", err)
	}
	if got := tc.TotalFrames(); got != 6 {
		t.Errorf("TotalFrames() = %d, want 6", got)
	}
	// Frame 0 starts at t=0, frame 2 at 80 ms, frame 5 at 180 ms.
	cases := []struct {
		at   time.Duration
		want int64
	}{
		{0, 0},
		{80 * time.Millisecond, 2},
		{180 * time.Millisecond, 5},
	}
	for _, c := range cases {
		if got := tc.FrameNumberFromTime(c.at); got != c.want {
			t.Errorf("FrameNumberFromTime(%v) = %d, want %d", c.at, got, c.want)
		}
	}
}

// TestTimecodeV1FillsGaps pins the v1 handler: an "assume" rate plus explicit
// ranges, with the gaps filled at the assumed rate.
func TestTimecodeV1FillsGaps(t *testing.T) {
	dir := t.TempDir()
	body := "# timecode format v1\n" +
		"Assume 25.000000\n" +
		"2,3,50.000000\n"
	path := filepath.Join(dir, "test.tcfile")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write timecode: %v", err)
	}

	tc, err := LoadTimecode(path, 6)
	if err != nil {
		t.Fatalf("LoadTimecode() error = %v", err)
	}
	// Frames 0-1 at 25 fps, 2-3 at 50, then 4-5 filled at 25.
	if got := tc.TotalFrames(); got != 6 {
		t.Errorf("TotalFrames() = %d, want 6", got)
	}
	// The total length is 2/25 + 2/50 + 2/25 seconds = 200 ms.
	if got := tc.TotalLength().Milliseconds(); got != 200 {
		t.Errorf("TotalLength() = %d ms, want 200", got)
	}
}

// TestTimecodeRejectsBadInput pins the two format checks the legacy constructor
// performed.
func TestTimecodeRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body string
	}{
		{"no header", "1.0\n2.0\n"},
		{"v1 without assume", "# timecode format v1\n1,2,25\n"},
		{"v1 with a malformed range", "# timecode format v1\nAssume 25\n1,x,25\n"},
		{"v2 with an unparseable timestamp", "# timecode format v2\n0.0\nabc\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_")+".tcfile")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatalf("write timecode: %v", err)
			}
			if _, err := LoadTimecode(path, 10); err == nil {
				t.Error("LoadTimecode() = nil, want an error")
			}
		})
	}
}

// TestCodecExtension pins the per-codec extension table.
func TestCodecExtension(t *testing.T) {
	cases := []struct{ format, container, want string }{
		{"HEVC", "MKV", ".hevc"},
		{"AVC", "MKV", "_.mkv"},
		{"AVC", "MP4", ".h264"},
		{"AV1", "MKV", ".ivf"},
	}
	for _, tc := range cases {
		if got := codecExtension(tc.format, tc.container); got != tc.want {
			t.Errorf("codecExtension(%q, %q) = %q, want %q", tc.format, tc.container, got, tc.want)
		}
	}
}

// TestHumanReadableFilesize pins the legacy HumanReadableFilesize, including its
// binary units and .NET's default double formatting.
func TestHumanReadableFilesize(t *testing.T) {
	cases := []struct {
		size int64
		want string
	}{
		{512, "512 B"},
		{1024, "1 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1 MB"},
		{1024 * 1024 * 1024, "1 GB"},
		{1500000, "1.43 MB"},
	}
	for _, tc := range cases {
		if got := humanReadableFilesize(tc.size, 2); got != tc.want {
			t.Errorf("humanReadableFilesize(%d) = %q, want %q", tc.size, got, tc.want)
		}
	}
}

// TestCRC32Naming pins the tag OKEFile.AddCRC32 appends. The value is the
// standard IEEE CRC-32, which is what Utils/CRC32.cs computed.
func TestCRC32Naming(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ep01.mkv")
	// The classic CRC-32 check value: "123456789" is 0xCBF43926.
	if err := os.WriteFile(path, []byte("123456789"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := addCRC32(path); err != nil {
		t.Fatalf("addCRC32() error = %v", err)
	}
	want := filepath.Join(dir, "ep01 [CBF43926].mkv")
	if _, err := os.Stat(want); err != nil {
		entries, _ := os.ReadDir(dir)
		t.Fatalf("want %s, directory holds %v (err %v)", want, entries, err)
	}
}

// newTestNuma is a one-node allocator, so the x265 pools mask is deterministic.
func newTestNuma(count int) *platform.Numa { return platform.NewNumaWithCount(count) }
