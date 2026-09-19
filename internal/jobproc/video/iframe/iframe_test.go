package iframe

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// feedLines pushes a fixture through a fresh handler and returns the processor
// plus the first handler error.
func feedLines(t *testing.T, name string, opts Options) (*Processor, error) {
	t.Helper()
	p := New(opts)
	handler := p.lineHandler()

	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if err := handler(sc.Text()); err != nil {
			return p, err
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return p, nil
}

// feed runs raw lines through a fresh handler.
func feed(t *testing.T, opts Options, lines ...string) (*Processor, error) {
	t.Helper()
	p := New(opts)
	handler := p.lineHandler()
	for _, line := range lines {
		if err := handler(line); err != nil {
			return p, err
		}
	}
	return p, nil
}

// --- script generation ------------------------------------------------------

// The generated script is the acceptance surface for this package: it must be
// byte-identical to what PrepareScript wrote, including the raw-string literal
// that keeps Windows backslashes intact and the stderr print the parser reads.
func TestScriptContent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		oldFile string
		want    string
	}{
		{
			name:    "windows path with spaces and brackets",
			oldFile: `D:\BDMV\output\00002.m2ts [6BB45BA9].mkv`,
			want: "import sys\n" +
				"from vapoursynth import core\n" +
				"a=R\"D:\\BDMV\\output\\00002.m2ts [6BB45BA9].mkv\"\n" +
				"src = core.lsmas.LWLibavSource(a, cache=0, framelist=True)\n" +
				"src.text.FrameProps(\"_IFrameList\").set_output(0)\n" +
				"print(f\"IFrameList: {src.get_frame(0).props._IFrameList}\", file=sys.stderr)\n",
		},
		{
			name:    "unix path",
			oldFile: "/mnt/bd/output/ep01.mkv",
			want: "import sys\n" +
				"from vapoursynth import core\n" +
				"a=R\"/mnt/bd/output/ep01.mkv\"\n" +
				"src = core.lsmas.LWLibavSource(a, cache=0, framelist=True)\n" +
				"src.text.FrameProps(\"_IFrameList\").set_output(0)\n" +
				"print(f\"IFrameList: {src.get_frame(0).props._IFrameList}\", file=sys.stderr)\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ScriptContent(tc.oldFile)
			if got != tc.want {
				t.Errorf("ScriptContent(%q) =\n%q\nwant\n%q", tc.oldFile, got, tc.want)
			}
		})
	}
}

// The script carries no "# OKE:" tag at all. Unlike a profile's .vpy, which the
// engine rewrites through the OKE tags, this one bakes the old file's path in
// directly, and that difference must not drift.
func TestScriptContentHasNoOKETags(t *testing.T) {
	t.Parallel()
	got := ScriptContent(`D:\a\b.mkv`)
	if strings.Contains(got, "OKE:") {
		t.Errorf("generated script contains an OKE tag:\n%s", got)
	}
}

func TestScriptContentLabels(t *testing.T) {
	t.Parallel()
	got := ScriptContent(`D:\a\b.mkv`)

	// Every label the parser depends on, checked individually so a failure
	// names the missing piece rather than dumping the whole script.
	for _, want := range []string{
		"import sys",
		"from vapoursynth import core",
		"core.lsmas.LWLibavSource(a, cache=0, framelist=True)",
		`src.text.FrameProps("_IFrameList").set_output(0)`,
		`print(f"IFrameList: {src.get_frame(0).props._IFrameList}", file=sys.stderr)`,
		`a=R"D:\a\b.mkv"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated script is missing %q:\n%s", want, got)
		}
	}
}

func TestScriptPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		workingPath string
		want        string
	}{
		{`D:\BDMV\output\ep01`, `D:\BDMV\output\ep01_iframe.vpy`},
		{"/mnt/bd/ep01", "/mnt/bd/ep01_iframe.vpy"},
	}
	for _, tc := range cases {
		t.Run(tc.workingPath, func(t *testing.T) {
			t.Parallel()
			if got := ScriptPath(tc.workingPath); got != tc.want {
				t.Errorf("ScriptPath(%q) = %q, want %q", tc.workingPath, got, tc.want)
			}
		})
	}
}

// --- I-frame output parsing -------------------------------------------------

func TestParseTypicalFixture(t *testing.T) {
	t.Parallel()

	const frames = 35175
	p, err := feedLines(t, "iframes-typical.txt", Options{NumberOfFrames: frames})
	if err != nil {
		t.Fatalf("line handler error = %v", err)
	}

	got := p.IFrames()
	// The captured list has 300 entries; the frame count (35175) is not among
	// them, so the bracket appends it for 301 total.
	if want := 301; len(got) != want {
		t.Fatalf("len(IFrames()) = %d, want %d", len(got), want)
	}
	if got[0] != 0 {
		t.Errorf("first I-frame = %d, want 0", got[0])
	}
	if got[len(got)-1] != frames {
		t.Errorf("last I-frame = %d, want %d", got[len(got)-1], frames)
	}

	// The list must stay ascending: the slicer's lookups are linear scans that
	// assume order.
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Fatalf("I-frame list is not sorted at %d: %v", i, got)
		}
	}
}

func TestParseListAddsBrackets(t *testing.T) {
	// A captured list from a 120-frame clip whose keyframes ffprobe confirms
	// at 0, 25, 49, 73 and 97. The frame count is 120, which is not itself a
	// keyframe, so the bracket must append it.
	p, err := feedLines(t, "iframes-small.txt", Options{NumberOfFrames: 120})
	if err != nil {
		t.Fatalf("line handler error = %v", err)
	}
	assertIFrames(t, p.IFrames(), []int64{0, 25, 49, 73, 97, 120})
}

func TestParseListBracketCases(t *testing.T) {
	cases := []struct {
		name     string
		lines    []string
		frames   int64
		want     []int64
		wantList bool
	}{
		{
			name:     "both ends missing",
			lines:    []string{"IFrameList: [24, 48, 72]"},
			frames:   100,
			want:     []int64{0, 24, 48, 72, 100},
			wantList: true,
		},
		{
			name:     "zero present",
			lines:    []string{"IFrameList: [0, 24, 48]"},
			frames:   100,
			want:     []int64{0, 24, 48, 100},
			wantList: true,
		},
		{
			name:     "frame count present",
			lines:    []string{"IFrameList: [0, 24, 48, 100]"},
			frames:   100,
			want:     []int64{0, 24, 48, 100},
			wantList: true,
		},
		{
			name:     "single element",
			lines:    []string{"IFrameList: [0]"},
			frames:   1,
			want:     []int64{0, 1},
			wantList: true,
		},
		{
			name:     "no spaces after commas",
			lines:    []string{"IFrameList: [0,24,48,100]"},
			frames:   100,
			want:     []int64{0, 24, 48, 100},
			wantList: true,
		},
		{
			name:     "extra spaces",
			lines:    []string{"IFrameList: [0,  24,   48,  100]"},
			frames:   100,
			want:     []int64{0, 24, 48, 100},
			wantList: true,
		},
		{
			name:     "empty list falls back to the bracket",
			lines:    []string{"IFrameList: []"},
			frames:   500,
			want:     []int64{0, 500},
			wantList: true,
		},
		{
			name:     "frame count zero is not duplicated",
			lines:    []string{"IFrameList: [0, 5, 10]"},
			frames:   0,
			want:     []int64{0, 5, 10},
			wantList: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := feed(t, Options{NumberOfFrames: tc.frames}, tc.lines...)
			if err != nil {
				t.Fatalf("line handler error = %v", err)
			}
			assertIFrames(t, p.IFrames(), tc.want)
		})
	}
}

// A line that mentions the marker but is not a well-formed list must not panic.
// The legacy regex would have thrown IndexOutOfRange on split[1] here.
func TestParseMalformedListIsIgnored(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		lines []string
	}{
		{"bare marker", []string{"IFrameList"}},
		{"marker without colon", []string{"IFrameList [0, 24]"}},
		{"non-numeric body", []string{"IFrameList: [a, b, c]"}},
		{"unterminated list", []string{"IFrameList: [0, 24"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := feed(t, Options{NumberOfFrames: 100}, tc.lines...)
			if err != nil {
				t.Fatalf("line handler error = %v", err)
			}
			if got := p.IFrames(); got != nil {
				t.Errorf("IFrames() = %v, want nil for a malformed list line", got)
			}
		})
	}
}

func TestParseEveryListLineAppends(t *testing.T) {
	t.Parallel()

	// vspipe --info evaluates the script once, so a second list is not
	// expected. Should one appear anyway, the legacy code appended its numbers
	// verbatim rather than replacing the list, and so does this. The result is
	// deliberately asserted as-is: the bracket is only applied once, after the
	// first list, exactly as the original did.
	p, err := feed(t, Options{NumberOfFrames: 100},
		"IFrameList: [0, 24]",
		"IFrameList: [0, 48]",
	)
	if err != nil {
		t.Fatalf("line handler error = %v", err)
	}
	assertIFrames(t, p.IFrames(), []int64{0, 24, 100, 0, 48})
}

func TestParseFrameCountMismatchWarns(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		line       string
		frames     int64
		wantErr    bool
		wantKind   okerr.Kind
		wantDetail string
	}{
		{
			name:   "equal",
			line:   "Frames: 3218",
			frames: 3218,
		},
		{
			name:   "shorter old release warns but continues",
			line:   "Frames: 3000",
			frames: 3218,
		},
		{
			name:       "longer old release fails",
			line:       "Frames: 4000",
			frames:     3218,
			wantErr:    true,
			wantKind:   okerr.KindMismatch,
			wantDetail: "脚本输出帧数为3218，但旧版压制成品帧数为4000",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := feed(t, Options{NumberOfFrames: tc.frames}, tc.line)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("line handler error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("line handler error = nil, want a frame count failure")
			}
			e := okerr.AsError(err)
			if e.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", e.Kind, tc.wantKind)
			}
			if e.Summary != okerr.ErrReEncodeFrames.Summary {
				t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrReEncodeFrames.Summary)
			}
			if e.Detail != tc.wantDetail {
				t.Errorf("detail = %q, want %q", e.Detail, tc.wantDetail)
			}
		})
	}
}

func TestParsePythonErrorFixture(t *testing.T) {
	t.Parallel()

	_, err := feedLines(t, "python-error.txt", Options{NumberOfFrames: 3218})
	if err == nil {
		t.Fatal("line handler error = nil, want a vpy failure")
	}
	e := okerr.AsError(err)
	if e.Kind != okerr.KindTool {
		t.Errorf("kind = %q, want %q", e.Kind, okerr.KindTool)
	}
	if e.Summary != okerr.ErrVpy.Summary {
		t.Errorf("summary = %q, want %q", e.Summary, okerr.ErrVpy.Summary)
	}
	// The detail accumulates every traceback line, so the operator sees the
	// real Python message rather than just the exception type.
	if !strings.Contains(e.Detail, "LWLibavSource: failed to open the file") {
		t.Errorf("detail = %q, want it to carry the Python error", e.Detail)
	}
}

// A traceback must not be mistaken for a list, and vice versa: the two markers
// share no text, but the ordering of the checks matters.
func TestParseErrorSuppressesListParsing(t *testing.T) {
	t.Parallel()

	_, err := feed(t, Options{NumberOfFrames: 100},
		"Python exception: boom",
		"Traceback (most recent call last):",
		`  File "ep01_iframe.vpy", line 5, in <module>`,
		"ValueError: something went wrong",
	)
	if err == nil {
		t.Fatal("line handler error = nil, want a vpy failure")
	}
	e := okerr.AsError(err)
	if !strings.Contains(e.Detail, "ValueError: something went wrong") {
		t.Errorf("detail = %q, want the ValueError line", e.Detail)
	}
}

// --- IFrameInfo lookups -----------------------------------------------------

func TestFindNearestLeft(t *testing.T) {
	t.Parallel()

	list := IFrameInfo{0, 24, 48, 72, 240, 600}

	cases := []struct {
		name  string
		begin int64
		want  int64
		ok    bool
	}{
		{"exact hit", 48, 48, true},
		{"between two entries", 50, 48, true},
		{"first element", 0, 0, true},
		{"before the first element", -1, 0, false},
		{"after the last element", 601, 600, true},
		{"far past the end", 1 << 40, 600, true},
		{"empty list", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := list
			if tc.name == "empty list" {
				src = IFrameInfo{}
			}
			got, ok := src.FindNearestLeft(tc.begin)
			if got != tc.want || ok != tc.ok {
				t.Errorf("FindNearestLeft(%d) = (%d, %v), want (%d, %v)",
					tc.begin, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestFindNearestRight(t *testing.T) {
	t.Parallel()

	list := IFrameInfo{0, 24, 48, 72, 240, 600}

	cases := []struct {
		name string
		end  int64
		want int64
		ok   bool
	}{
		{"exact hit", 48, 48, true},
		{"between two entries", 50, 72, true},
		{"last element", 600, 600, true},
		{"after the last element", 601, 0, false},
		{"before the first element", -5, 0, true},
		{"empty list", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := list
			if tc.name == "empty list" {
				src = IFrameInfo{}
			}
			got, ok := src.FindNearestRight(tc.end)
			if got != tc.want || ok != tc.ok {
				t.Errorf("FindNearestRight(%d) = (%d, %v), want (%d, %v)",
					tc.end, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// FindInRangeIndex returns an INDEX range, not a frame range: [first, last]
// positions inside the list. These expectations are the C# semantics.
func TestFindInRangeIndex(t *testing.T) {
	t.Parallel()

	list := IFrameInfo{0, 24, 48, 72, 240, 600}

	cases := []struct {
		name  string
		r     model.SliceInfo
		want  model.SliceInfo
		found bool
	}{
		{
			name:  "exact single element",
			r:     model.NewSliceInfo(24, 48),
			want:  model.NewSliceInfo(1, 1),
			found: true,
		},
		{
			name:  "run in the middle",
			r:     model.NewSliceInfo(24, 100),
			want:  model.NewSliceInfo(1, 3),
			found: true,
		},
		{
			name:  "whole list",
			r:     model.NewSliceInfo(0, 601),
			want:  model.NewSliceInfo(0, 5),
			found: true,
		},
		{
			name:  "first element only",
			r:     model.NewSliceInfo(0, 24),
			want:  model.NewSliceInfo(0, 0),
			found: true,
		},
		{
			name:  "upper bound is exclusive",
			r:     model.NewSliceInfo(48, 72),
			want:  model.NewSliceInfo(2, 2),
			found: true,
		},
		{
			name:  "jumps over a gap to the next entry",
			r:     model.NewSliceInfo(73, 600),
			want:  model.NewSliceInfo(4, 4),
			found: true,
		},
		{
			name:  "no element in range",
			r:     model.NewSliceInfo(100, 200),
			found: false,
		},
		{
			name:  "range before the list",
			r:     model.NewSliceInfo(-100, 0),
			found: false,
		},
		{
			name:  "empty list",
			r:     model.NewSliceInfo(0, 100),
			found: false,
		},
		{
			name:  "empty range",
			r:     model.NewSliceInfo(24, 24),
			found: false,
		},
		{
			name:  "run is cut by the first non-member",
			r:     model.NewSliceInfo(0, 73),
			want:  model.NewSliceInfo(0, 3),
			found: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := list
			if tc.name == "empty list" {
				src = IFrameInfo{}
			}
			got, found := src.FindInRangeIndex(tc.r)
			if found != tc.found {
				t.Fatalf("FindInRangeIndex(%v) found = %v, want %v", tc.r, found, tc.found)
			}
			if !found {
				return
			}
			if got != tc.want {
				t.Errorf("FindInRangeIndex(%v) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

// InRange is the GetRange(index.begin, index.GetLength()+1) call the slicer
// made after FindInRangeIndex.
func TestInRange(t *testing.T) {
	t.Parallel()

	list := IFrameInfo{0, 24, 48, 72, 240, 600}

	cases := []struct {
		name  string
		index model.SliceInfo
		want  IFrameInfo
	}{
		{"single element", model.NewSliceInfo(1, 1), IFrameInfo{24}},
		{"middle run", model.NewSliceInfo(1, 3), IFrameInfo{24, 48, 72}},
		{"whole list", model.NewSliceInfo(0, 5), IFrameInfo{0, 24, 48, 72, 240, 600}},
		{"end out of bounds", model.NewSliceInfo(4, 9), nil},
		{"inverted", model.NewSliceInfo(3, 1), nil},
		{"negative", model.NewSliceInfo(-1, 1), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := list.InRange(tc.index)
			if len(got) != len(tc.want) {
				t.Fatalf("InRange(%v) = %v, want %v", tc.index, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("InRange(%v) = %v, want %v", tc.index, got, tc.want)
				}
			}
		})
	}
}

// The slicer's pipeline, end to end: align a raw slice to I-frames, then pull
// the chapter I-frames that fall inside the aligned slice.
func TestSliceAlignmentPipeline(t *testing.T) {
	t.Parallel()

	list := IFrameInfo{0, 24, 48, 72, 240, 600}
	raw := model.NewSliceInfo(50, 250)

	begin, ok := list.FindNearestLeft(raw.Begin)
	if !ok {
		t.Fatal("FindNearestLeft found nothing")
	}
	end, ok := list.FindNearestRight(raw.End)
	if !ok {
		t.Fatal("FindNearestRight found nothing")
	}
	if begin != 48 || end != 600 {
		t.Fatalf("aligned slice = [%d, %d], want [48, 600]", begin, end)
	}

	aligned := model.NewSliceInfo(begin, end)
	index, ok := list.FindInRangeIndex(aligned)
	if !ok {
		t.Fatal("FindInRangeIndex found nothing inside the aligned slice")
	}
	// The aligned slice is [48, 600); 600 itself is excluded by the half-open
	// range, so the run ends at index 4 (frame 240), not at the final entry.
	if want := model.NewSliceInfo(2, 4); index != want {
		t.Fatalf("FindInRangeIndex(%v) = %v, want %v", aligned, index, want)
	}
	if got := list.InRange(index); len(got) != 3 {
		t.Fatalf("InRange(%v) = %v, want 3 elements", index, got)
	}
}

func assertIFrames(t *testing.T, got, want []int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("IFrames() = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("IFrames()[%d] = %d, want %d (full: %v)", i, got[i], want[i], got)
		}
	}
}
