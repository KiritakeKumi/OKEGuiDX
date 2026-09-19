package chapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// fakeTool turns the test binary into a stand-in for an external tool. The
// production Run code path is untouched: the same argv is passed, only the
// executable differs. This mirrors the pattern internal/proc and the jobproc
// wrappers use.
const (
	helperEnvVar     = "OKEGUIDX_CHAPTER_HELPER"
	helperRoleEnvVar = "OKEGUIDX_CHAPTER_ROLE"
	helperBodyEnvVar = "OKEGUIDX_CHAPTER_BODY"
	helperExitEnvVar = "OKEGUIDX_CHAPTER_EXIT"

	roleStdout = "stdout"
	roleStderr = "stderr"
)

// TestMain doubles as the fake tchapter/ffprobe.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnvVar) == "1" {
		os.Exit(helperMain())
	}
	os.Exit(m.Run())
}

// helperMain writes the body named by the environment to the stream the role
// selects, then exits with the recorded code.
func helperMain() int {
	body := os.Getenv(helperBodyEnvVar)
	stream := os.Stdout
	if os.Getenv(helperRoleEnvVar) == roleStderr {
		stream = os.Stderr
	}
	if _, err := stream.WriteString(body); err != nil {
		return 3
	}
	code := 0
	if raw := os.Getenv(helperExitEnvVar); raw != "" {
		for _, c := range raw {
			code = code*10 + int(c-'0')
		}
	}
	return code
}

// helperEnv builds the environment that turns the test binary into a fake
// external tool. Passing it through proc.Spec.Env rather than t.Setenv keeps
// the tests parallel-safe and leaves the process environment untouched.
func helperEnv(role, body string) []string {
	return append(os.Environ(),
		helperEnvVar+"=1",
		helperRoleEnvVar+"="+role,
		helperBodyEnvVar+"="+body,
		helperExitEnvVar+"=0",
	)
}

// helperParser returns a Parser pointed at the test binary, replaying the given
// fixture file.
func helperParser(t *testing.T, fixture string) *Parser {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	return &Parser{Tool: os.Args[0], Env: helperEnv(roleStdout, string(body))}
}

// helperProbe returns a Probe pointed at the test binary, replaying the given
// fixture file.
func helperProbe(t *testing.T, fixture string) *Probe {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", fixture, err)
	}
	return &Probe{Tool: os.Args[0], Env: helperEnv(roleStdout, string(body))}
}

// helperMatroskaParser returns a Parser for the MKV path: the test binary
// stands in for mkvextract (which the CLI cannot replace) and for the CLI
// itself, replaying the given fixture as the CLI's answer.
func helperMatroskaParser(t *testing.T, fixture string) *Parser {
	t.Helper()
	p := helperParser(t, fixture)
	p.MkvExtract = os.Args[0]
	return p
}

// fakeParser returns a Parser replaying an inline JSON document.
func fakeParser(body string) *Parser {
	return &Parser{Tool: os.Args[0], Env: helperEnv(roleStdout, body)}
}

// fakeProbe returns a Probe replaying an inline JSON document.
func fakeProbe(body string) *Probe {
	return &Probe{Tool: os.Args[0], Env: helperEnv(roleStdout, body)}
}

// writeFile creates an empty file and its parents.
func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newTask builds a task around one input path with a known length.
func newTask(input string, lengthMS int64) *model.Task {
	return &model.Task{
		ID:       model.NewTaskID(),
		Inputs:   []model.FileRef{model.NewFileRef(input)},
		LengthMS: lengthMS,
	}
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

func TestParserReadsRealOgmFixture(t *testing.T) {
	t.Parallel()
	p := helperParser(t, "tchapter_ogm.json")
	infos, err := p.Parse(t.Context(), "ep01.txt")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(infos) != 1 {
		t.Fatalf("got %d entries, want 1", len(infos))
	}
	info := infos[0]
	if got := info.Count(); got != 13 {
		t.Errorf("chapter count = %d, want 13", got)
	}
	// The fixture is native/tchapter/testdata/OGM/00001.txt; its first mark is
	// at zero and its second at 00:00:41.041.
	if info.Chapters[0].Time != 0 {
		t.Errorf("first chapter time = %v, want 0", info.Chapters[0].Time)
	}
	if want := 41041 * time.Millisecond; info.Chapters[1].Time != want {
		t.Errorf("second chapter time = %v, want %v", info.Chapters[1].Time, want)
	}
	if want := 1361860 * time.Millisecond; info.Duration != want {
		t.Errorf("duration = %v, want %v", info.Duration, want)
	}
	if info.Chapters[0].Name != "Chapter 01" {
		t.Errorf("first chapter name = %q, want %q", info.Chapters[0].Name, "Chapter 01")
	}
	// The CLI numbers the chapters; the port keeps that numbering.
	if info.Chapters[12].Number != 13 {
		t.Errorf("last chapter number = %d, want 13", info.Chapters[12].Number)
	}
}

func TestParserReadsRealMplsFixture(t *testing.T) {
	t.Parallel()
	p := helperParser(t, "tchapter_mpls.json")
	infos, err := p.Parse(t.Context(), "00001.mpls")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("got %d entries, want 2", len(infos))
	}
	if infos[0].SourceName != "00002" {
		t.Errorf("entry 0 source = %q, want %q", infos[0].SourceName, "00002")
	}
	if infos[1].SourceName != "00003" {
		t.Errorf("entry 1 source = %q, want %q", infos[1].SourceName, "00003")
	}
	if infos[0].FPSNum != 24000 || infos[0].FPSDen != 1001 {
		t.Errorf("entry 0 fps = %d/%d, want 24000/1001", infos[0].FPSNum, infos[0].FPSDen)
	}
}

func TestParserReadsMatroskaXmlFixture(t *testing.T) {
	t.Parallel()
	p := helperParser(t, "tchapter_matroska.json")
	info, err := p.ParseFirst(t.Context(), "chapters.xml")
	if err != nil {
		t.Fatalf("ParseFirst() error = %v", err)
	}
	if info == nil {
		t.Fatal("ParseFirst() = nil, want an entry")
	}
	if got := info.Count(); got != 4 {
		t.Errorf("chapter count = %d, want 4", got)
	}
	if want := 10 * time.Second; info.Chapters[1].Time != want {
		t.Errorf("second chapter time = %v, want %v", info.Chapters[1].Time, want)
	}
}

func TestParserReportsCliError(t *testing.T) {
	t.Parallel()
	p := helperParser(t, "tchapter_error.json")
	_, err := p.Parse(t.Context(), "broken.xml")
	if err == nil {
		t.Fatal("Parse() = nil error, want a structured failure")
	}
	e := okerr.AsError(err)
	if e.Kind != okerr.KindTool {
		t.Errorf("Kind = %q, want %q", e.Kind, okerr.KindTool)
	}
	if !strings.Contains(e.Detail, "E_FORMAT") {
		t.Errorf("Detail = %q, want it to name the CLI status", e.Detail)
	}
}

func TestParserRejectsGarbageOutput(t *testing.T) {
	t.Parallel()
	p := fakeParser("not json at all")
	_, err := p.Parse(t.Context(), "ep01.txt")
	if err == nil {
		t.Fatal("Parse() = nil error, want a decode failure")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindTool {
		t.Errorf("Kind = %q, want %q", got, okerr.KindTool)
	}
}

func TestParserRequiresATool(t *testing.T) {
	t.Parallel()
	_, err := (&Parser{}).Parse(t.Context(), "ep01.txt")
	if err == nil {
		t.Fatal("Parse() = nil error, want a not-found error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindNotFound {
		t.Errorf("Kind = %q, want %q", got, okerr.KindNotFound)
	}
}

func TestParserEmptyEntriesIsNotAnError(t *testing.T) {
	t.Parallel()
	info, err := fakeParser(`{"version":1,"format":"ogm","entries":[]}`).ParseFirst(t.Context(), "ep01.txt")
	if err != nil {
		t.Fatalf("ParseFirst() error = %v", err)
	}
	if info != nil {
		t.Errorf("ParseFirst() = %+v, want nil for an empty document", info)
	}
}

// TestParserFixtureEntryToInfo pins the JSON contract field by field, so a
// change in the CLI output is caught here rather than at the muxer.
func TestParserFixtureEntryToInfo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		entry     parseEntry
		wantCount int
		wantFirst time.Duration
		wantLast  time.Duration
	}{
		{
			name: "nanosecond conversion",
			entry: parseEntry{
				FPSNum: 24000, FPSDen: 1001, DurationNS: 1_500_000_000,
				Chapters: []parseChapter{
					{Name: "a", TimeNS: 0},
					{Name: "b", TimeNS: 500_000_000},
					{Name: "c", TimeNS: 1_500_000_000},
				},
			},
			wantCount: 3,
			wantFirst: 0,
			wantLast:  1500 * time.Millisecond,
		},
		{
			name:      "empty chapter list",
			entry:     parseEntry{FPSNum: 25, FPSDen: 1},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			info := tt.entry.toInfo()
			if info.Count() != tt.wantCount {
				t.Fatalf("count = %d, want %d", info.Count(), tt.wantCount)
			}
			if tt.wantCount == 0 {
				return
			}
			if info.Chapters[0].Time != tt.wantFirst {
				t.Errorf("first = %v, want %v", info.Chapters[0].Time, tt.wantFirst)
			}
			if got := info.Chapters[info.Count()-1].Time; got != tt.wantLast {
				t.Errorf("last = %v, want %v", got, tt.wantLast)
			}
			for k, c := range info.Chapters {
				if c.Number != k+1 {
					t.Errorf("chapter %d number = %d, want %d", k, c.Number, k+1)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Probe (the MediaInfo replacement)
// ---------------------------------------------------------------------------

func TestProbeArgs(t *testing.T) {
	t.Parallel()
	got := ffprobeArgs("V:/media/ep01.mkv")
	want := []string{
		"-v", "error",
		"-show_chapters",
		"-show_entries", "format=duration",
		"-of", "json",
		"V:/media/ep01.mkv",
	}
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for k := range want {
		if got[k] != want[k] {
			t.Errorf("args[%d] = %q, want %q", k, got[k], want[k])
		}
	}
}

func TestProbeReadsRealFixtureWithChapters(t *testing.T) {
	t.Parallel()
	p := helperProbe(t, "ffprobe_with_chapters.json")
	has, err := p.HasChapters(t.Context(), "ep01.mkv")
	if err != nil {
		t.Fatalf("HasChapters() error = %v", err)
	}
	if !has {
		t.Error("HasChapters() = false, want true")
	}

	res, err := p.Run(t.Context(), "ep01.mkv")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// The fixture was produced from a six-second file carrying four marks.
	if res.ChapterCount != 4 {
		t.Errorf("ChapterCount = %d, want 4", res.ChapterCount)
	}
	if res.DurationMS != 6000 {
		t.Errorf("DurationMS = %d, want 6000", res.DurationMS)
	}
}

func TestProbeReadsRealFixtureWithoutChapters(t *testing.T) {
	t.Parallel()
	p := helperProbe(t, "ffprobe_no_chapters.json")
	has, err := p.HasChapters(t.Context(), "ep01.mkv")
	if err != nil {
		t.Fatalf("HasChapters() error = %v", err)
	}
	if has {
		t.Error("HasChapters() = true, want false")
	}

	res, err := p.Run(t.Context(), "ep01.mkv")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.ChapterCount != 0 {
		t.Errorf("ChapterCount = %d, want 0", res.ChapterCount)
	}
	if res.DurationMS != 2000 {
		t.Errorf("DurationMS = %d, want 2000", res.DurationMS)
	}
}

func TestProbeEmptyOutputMeansNoChapters(t *testing.T) {
	t.Parallel()
	res, err := fakeProbe("").Run(t.Context(), "ep01.mkv")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.ChapterCount != 0 {
		t.Errorf("ChapterCount = %d, want 0", res.ChapterCount)
	}
	if res.DurationMS != -1 {
		t.Errorf("DurationMS = %d, want -1 for an unknown duration", res.DurationMS)
	}
}

func TestProbeMissingDurationIsMinusOne(t *testing.T) {
	t.Parallel()
	res, err := fakeProbe(`{"chapters":[{"id":1,"time_base":"1/1000","start":0,"end":1000,"tags":{"title":"x"}}]}`).Run(t.Context(), "ep01.mkv")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.ChapterCount != 1 {
		t.Errorf("ChapterCount = %d, want 1", res.ChapterCount)
	}
	if res.DurationMS != -1 {
		t.Errorf("DurationMS = %d, want -1", res.DurationMS)
	}
}

func TestProbeRejectsGarbageOutput(t *testing.T) {
	t.Parallel()
	if _, err := fakeProbe("<html>not json</html>").Run(t.Context(), "ep01.mkv"); err == nil {
		t.Fatal("Run() = nil error, want a decode failure")
	}
}

func TestProbeRequiresATool(t *testing.T) {
	t.Parallel()
	if _, err := (&Probe{}).Run(t.Context(), "ep01.mkv"); err == nil {
		t.Fatal("Run() = nil error, want a not-found error")
	}
}

// ---------------------------------------------------------------------------
// FindChapterFile
// ---------------------------------------------------------------------------

func TestFindChapterFileUsesTheTaskField(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	chapterFile := filepath.Join(dir, "external.txt")
	writeFile(t, chapterFile)

	task := newTask(input, 0)
	task.ChapterFile = model.NewFileRef(chapterFile)

	found, err := (&Service{}).FindChapterFile(task)
	if err != nil {
		t.Fatalf("FindChapterFile() error = %v", err)
	}
	if !found {
		t.Error("FindChapterFile() = false, want true")
	}
}

func TestFindChapterFileDiscoversAndReadsTheLanguage(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		chapterFile  string
		wantLanguage string
	}{
		{"language suffix", "Show.01.jpn.txt", "jpn"},
		{"no language suffix", "Show.01.txt", ""},
		{"longer language tag", "Show.01.zh-Hans.txt", "zh-Hans"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			input := filepath.Join(dir, "Show.01.m2ts")
			writeFile(t, input)
			writeFile(t, filepath.Join(dir, tt.chapterFile))

			task := newTask(input, 0)
			found, err := (&Service{}).FindChapterFile(task)
			if err != nil {
				t.Fatalf("FindChapterFile() error = %v", err)
			}
			if !found {
				t.Fatal("FindChapterFile() = false, want true")
			}
			if want := model.NewFileRef(filepath.Join(dir, tt.chapterFile)); task.ChapterFile.String() != want.String() {
				t.Errorf("ChapterFile = %q, want %q", task.ChapterFile.String(), want.String())
			}
			if task.ChapterLanguage != tt.wantLanguage {
				t.Errorf("ChapterLanguage = %q, want %q", task.ChapterLanguage, tt.wantLanguage)
			}
		})
	}
}

// TestFindChapterFileIgnoresNonMatchingNames covers the exact pattern the
// legacy code used: `<stem>.*txt`. `Show.01x.txt` and `Show.01.jpn.txtx` are
// not chapter files, and a bare `.txt` is not either.
func TestFindChapterFileIgnoresNonMatchingNames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "Show.01.m2ts")
	writeFile(t, input)
	for _, n := range []string{"Show.01x.txt", "Show.01.jpn.txtx", "Show.01.txt.bak", "Show.02.jpn.txt"} {
		writeFile(t, filepath.Join(dir, n))
	}

	task := newTask(input, 0)
	found, err := (&Service{}).FindChapterFile(task)
	if err != nil {
		t.Fatalf("FindChapterFile() error = %v", err)
	}
	if found {
		t.Errorf("FindChapterFile() = true (chose %q), want false", task.ChapterFile.ResolveLocal(""))
	}
}

func TestFindChapterFileRejectsAmbiguity(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "Show.01.m2ts")
	writeFile(t, input)
	writeFile(t, filepath.Join(dir, "Show.01.jpn.txt"))
	writeFile(t, filepath.Join(dir, "Show.01.eng.txt"))

	_, err := (&Service{}).FindChapterFile(newTask(input, 0))
	if err == nil {
		t.Fatal("FindChapterFile() = nil error, want an ambiguity error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindConfig {
		t.Errorf("Kind = %q, want %q", got, okerr.KindConfig)
	}
}

func TestFindChapterFileMissingDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "nope", "ep01.m2ts")

	_, err := (&Service{}).FindChapterFile(newTask(missing, 0))
	if err == nil {
		t.Fatal("FindChapterFile() = nil error, want a not-found error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindNotFound {
		t.Errorf("Kind = %q, want %q", got, okerr.KindNotFound)
	}
}

func TestFindChapterFileWithoutInput(t *testing.T) {
	t.Parallel()
	found, err := (&Service{}).FindChapterFile(&model.Task{})
	if err != nil {
		t.Fatalf("FindChapterFile() error = %v", err)
	}
	if found {
		t.Error("FindChapterFile() = true, want false for a task with no input")
	}
}

// ---------------------------------------------------------------------------
// HasBlurayStructure
// ---------------------------------------------------------------------------

// buildBluray creates a disc tree and returns the path of one stream file.
func buildBluray(t *testing.T, playlists ...string) (root, stream string) {
	t.Helper()
	root = t.TempDir()
	streamDir := filepath.Join(root, "BDMV", "STREAM")
	stream = filepath.Join(streamDir, "00001.m2ts")
	writeFile(t, stream)
	for _, name := range playlists {
		writeFile(t, filepath.Join(root, "BDMV", "PLAYLIST", name))
	}
	return root, stream
}

func TestHasBlurayStructure(t *testing.T) {
	t.Parallel()
	t.Run("complete structure", func(t *testing.T) {
		t.Parallel()
		_, stream := buildBluray(t, "00001.mpls")
		if !(&Service{}).HasBlurayStructure(newTask(stream, 0)) {
			t.Error("HasBlurayStructure() = false, want true")
		}
	})
	t.Run("no mpls file", func(t *testing.T) {
		t.Parallel()
		_, stream := buildBluray(t)
		if (&Service{}).HasBlurayStructure(newTask(stream, 0)) {
			t.Error("HasBlurayStructure() = true, want false")
		}
	})
	t.Run("not an m2ts", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "BDMV", "STREAM", "00001.mkv")
		writeFile(t, input)
		writeFile(t, filepath.Join(dir, "BDMV", "PLAYLIST", "00001.mpls"))
		if (&Service{}).HasBlurayStructure(newTask(input, 0)) {
			t.Error("HasBlurayStructure() = true, want false")
		}
	})
	t.Run("not inside STREAM", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "00001.m2ts")
		writeFile(t, input)
		writeFile(t, filepath.Join(dir, "PLAYLIST", "00001.mpls"))
		if (&Service{}).HasBlurayStructure(newTask(input, 0)) {
			t.Error("HasBlurayStructure() = true, want false")
		}
	})
	t.Run("PLAYLIST is a file", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "BDMV", "STREAM", "00001.m2ts")
		writeFile(t, input)
		writeFile(t, filepath.Join(dir, "BDMV", "PLAYLIST"))
		if (&Service{}).HasBlurayStructure(newTask(input, 0)) {
			t.Error("HasBlurayStructure() = true, want false")
		}
	})
	t.Run("uppercase stream directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "BDMV", "stream", "00001.M2TS")
		writeFile(t, input)
		writeFile(t, filepath.Join(dir, "BDMV", "playlist", "00001.MPLS"))
		if !(&Service{}).HasBlurayStructure(newTask(input, 0)) {
			t.Error("HasBlurayStructure() = false, want true (case-insensitive)")
		}
	})
}

// ---------------------------------------------------------------------------
// HasMatroskaChapter
// ---------------------------------------------------------------------------

func TestHasMatroskaChapter(t *testing.T) {
	t.Parallel()
	t.Run("mkv with chapters", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "ep01.mkv")
		writeFile(t, input)
		s := &Service{Probe: helperProbe(t, "ffprobe_with_chapters.json")}
		has, err := s.HasMatroskaChapter(t.Context(), newTask(input, 0))
		if err != nil {
			t.Fatalf("HasMatroskaChapter() error = %v", err)
		}
		if !has {
			t.Error("HasMatroskaChapter() = false, want true")
		}
	})
	t.Run("mkv without chapters", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "ep01.mkv")
		writeFile(t, input)
		s := &Service{Probe: helperProbe(t, "ffprobe_no_chapters.json")}
		has, err := s.HasMatroskaChapter(t.Context(), newTask(input, 0))
		if err != nil {
			t.Fatalf("HasMatroskaChapter() error = %v", err)
		}
		if has {
			t.Error("HasMatroskaChapter() = true, want false")
		}
	})
	t.Run("not an mkv never probes", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "ep01.m2ts")
		writeFile(t, input)
		// A nil probe would fail if it were consulted.
		has, err := (&Service{}).HasMatroskaChapter(t.Context(), newTask(input, 0))
		if err != nil {
			t.Fatalf("HasMatroskaChapter() error = %v", err)
		}
		if has {
			t.Error("HasMatroskaChapter() = true, want false")
		}
	})
}

// ---------------------------------------------------------------------------
// UpdateChapterStatus
// ---------------------------------------------------------------------------

func TestUpdateChapterStatus(t *testing.T) {
	t.Parallel()
	t.Run("chapter file wins", func(t *testing.T) {
		t.Parallel()
		root, stream := buildBluray(t, "00001.mpls")
		writeFile(t, filepath.Join(root, "BDMV", "STREAM", "00001.jpn.txt"))
		s := &Service{Probe: helperProbe(t, "ffprobe_with_chapters.json")}
		got, err := s.UpdateChapterStatus(t.Context(), newTask(stream, 0))
		if err != nil {
			t.Fatalf("UpdateChapterStatus() error = %v", err)
		}
		if got != model.ChapterYes {
			t.Errorf("status = %v, want Yes", got)
		}
	})
	t.Run("bluray structure", func(t *testing.T) {
		t.Parallel()
		_, stream := buildBluray(t, "00001.mpls")
		got, err := (&Service{}).UpdateChapterStatus(t.Context(), newTask(stream, 0))
		if err != nil {
			t.Fatalf("UpdateChapterStatus() error = %v", err)
		}
		if got != model.ChapterMaybe {
			t.Errorf("status = %v, want Maybe", got)
		}
	})
	t.Run("mkv chapters", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "ep01.mkv")
		writeFile(t, input)
		s := &Service{Probe: helperProbe(t, "ffprobe_with_chapters.json")}
		got, err := s.UpdateChapterStatus(t.Context(), newTask(input, 0))
		if err != nil {
			t.Fatalf("UpdateChapterStatus() error = %v", err)
		}
		if got != model.ChapterMKV {
			t.Errorf("status = %v, want MKV", got)
		}
	})
	t.Run("mkv without chapters", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "ep01.mkv")
		writeFile(t, input)
		s := &Service{Probe: helperProbe(t, "ffprobe_no_chapters.json")}
		got, err := s.UpdateChapterStatus(t.Context(), newTask(input, 0))
		if err != nil {
			t.Fatalf("UpdateChapterStatus() error = %v", err)
		}
		if got != model.ChapterNo {
			t.Errorf("status = %v, want No", got)
		}
	})
	t.Run("plain file has none", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		input := filepath.Join(dir, "ep01.m2ts")
		writeFile(t, input)
		got, err := (&Service{}).UpdateChapterStatus(t.Context(), newTask(input, 0))
		if err != nil {
			t.Fatalf("UpdateChapterStatus() error = %v", err)
		}
		if got != model.ChapterNo {
			t.Errorf("status = %v, want No", got)
		}
	})
	t.Run("nil task", func(t *testing.T) {
		t.Parallel()
		if _, err := (&Service{}).UpdateChapterStatus(t.Context(), nil); err == nil {
			t.Error("UpdateChapterStatus(nil) = nil error, want a rejection")
		}
	})
}

// ---------------------------------------------------------------------------
// LoadChapter
// ---------------------------------------------------------------------------

// externalChapterTask builds a task whose status is Yes and whose chapter file
// is present, wired to the given fixture.
func externalChapterTask(t *testing.T, fixture string, lengthMS int64) (*Service, *model.Task, string) {
	t.Helper()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	chapterFile := filepath.Join(dir, "ep01.jpn.txt")
	writeFile(t, chapterFile)

	task := newTask(input, lengthMS)
	task.Status.Chapter = model.ChapterYes
	task.ChapterFile = model.NewFileRef(chapterFile)
	return &Service{Parser: helperParser(t, fixture)}, task, input
}

func TestLoadChapterFromExternalFile(t *testing.T) {
	t.Parallel()
	// The OGM fixture runs to 1361860 ms; give the task a matching length so
	// the tail filter keeps every mark.
	s, task, _ := externalChapterTask(t, "tchapter_ogm.json", 1361860)
	// The language is discovered by FindChapterFile when the file is not known
	// yet, which is what UpdateChapterStatus does before LoadChapter runs.
	task.ChapterLanguage = "jpn"

	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if info == nil {
		t.Fatal("LoadChapter() = nil, want chapters")
	}
	if got := info.Count(); got != 12 {
		t.Errorf("chapter count = %d, want 12", got)
	}
	// The surviving list ends ~200 s before the video does, so no warning.
	if task.Status.Chapter != model.ChapterYes {
		t.Errorf("status = %v, want Yes", task.Status.Chapter)
	}
	if task.ChapterLanguage != "jpn" {
		t.Errorf("language = %q, want %q", task.ChapterLanguage, "jpn")
	}
	// Renumber is applied unconditionally, so the numbering is 1..12.
	for k, c := range info.Chapters {
		if c.Number != k+1 {
			t.Errorf("chapter %d number = %d, want %d", k, c.Number, k+1)
		}
	}
}

// TestLoadChapterKeepsAPresetLanguage is the counterpart: the legacy
// FindChapterFile returns as soon as ChapterFileName exists, so a task that
// already carries one does not have its language recomputed.
func TestLoadChapterKeepsAPresetLanguage(t *testing.T) {
	t.Parallel()
	s, task, _ := externalChapterTask(t, "tchapter_ogm.json", 1361860)
	task.ChapterLanguage = "eng"

	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if info == nil {
		t.Fatal("LoadChapter() = nil, want chapters")
	}
	if task.ChapterLanguage != "eng" {
		t.Errorf("language = %q, want the preset %q", task.ChapterLanguage, "eng")
	}
}

func TestLoadChapterDropsTheTrailingSecond(t *testing.T) {
	t.Parallel()
	// The fixture's last mark sits exactly at the fixture's duration, so the
	// `length - t > 1001` filter drops it. That is the legacy behaviour: a
	// chapter at the very end of the video is not worth muxing.
	s, task, _ := externalChapterTask(t, "tchapter_ogm.json", 1361860)

	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if got := info.Count(); got != 12 {
		t.Errorf("chapter count = %d, want 12", got)
	}
	if last := info.Chapters[info.Count()-1].Time; last != 1161911*time.Millisecond {
		t.Errorf("last chapter = %v, want the second-to-last fixture mark", last)
	}
}

func TestLoadChapterWarnsWhenTheTailIsClose(t *testing.T) {
	t.Parallel()
	// A three-mark list whose last mark is two seconds from the end: inside
	// the 3003 ms warning window.
	body := `{"version":1,"format":"ogm","entries":[{"title":"","source":"","fps_num":0,"fps_den":1,"duration_ns":0,"chapters":[` +
		`{"name":"a","time_ns":0,"frames":-1},` +
		`{"name":"b","time_ns":30000000000,"frames":-1},` +
		`{"name":"c","time_ns":58000000000,"frames":-1}]}]}`

	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	chapterFile := filepath.Join(dir, "ep01.txt")
	writeFile(t, chapterFile)

	task := newTask(input, 60_000)
	task.Status.Chapter = model.ChapterYes
	task.ChapterFile = model.NewFileRef(chapterFile)

	s := &Service{Parser: fakeParser(body)}
	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if info == nil {
		t.Fatal("LoadChapter() = nil, want chapters")
	}
	if task.Status.Chapter != model.ChapterWarn {
		t.Errorf("status = %v, want Warn", task.Status.Chapter)
	}
}

// TestLoadChapterWarnsWhenTheFirstChapterIsLate covers the other warning: a
// chapter list that does not start at zero.
func TestLoadChapterWarnsWhenTheFirstChapterIsLate(t *testing.T) {
	t.Parallel()
	body := `{"version":1,"format":"ogm","entries":[{"title":"","source":"","fps_num":0,"fps_den":1,"duration_ns":0,"chapters":[` +
		`{"name":"a","time_ns":1000000000,"frames":-1},` +
		`{"name":"b","time_ns":30000000000,"frames":-1}]}]}`

	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	chapterFile := filepath.Join(dir, "ep01.txt")
	writeFile(t, chapterFile)

	task := newTask(input, 120_000)
	task.Status.Chapter = model.ChapterYes
	task.ChapterFile = model.NewFileRef(chapterFile)

	s := &Service{Parser: fakeParser(body)}
	if _, err := s.LoadChapter(t.Context(), task); err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if task.Status.Chapter != model.ChapterWarn {
		t.Errorf("status = %v, want Warn", task.Status.Chapter)
	}
}

// TestLoadChapterDeduplicatesMatroskaEndTimes covers B5 from
// DECISIONS-NEEDED.md: an XML atom with both a start and an end produces two
// chapters with the same name, and the duplicate timestamps are collapsed here.
func TestLoadChapterDeduplicatesMatroskaEndTimes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.mkv")
	writeFile(t, input)

	task := newTask(input, 3_000_000)
	task.Status.Chapter = model.ChapterMKV

	s := &Service{Parser: helperMatroskaParser(t, "tchapter_xml_endtimes.json")}
	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if info == nil {
		t.Fatal("LoadChapter() = nil, want chapters")
	}
	// The fixture has six marks; the ones that share a timestamp with the next
	// one (0, 384000 and 680000 ms each appear twice) are collapsed to the
	// later entry.
	want := []time.Duration{
		0,
		384 * time.Second,
		680 * time.Second,
		1706 * time.Second,
	}
	if info.Count() != len(want) {
		t.Fatalf("chapter count = %d (%v), want %d", info.Count(), timesMS(info), len(want))
	}
	for k, w := range want {
		if info.Chapters[k].Time != w {
			t.Errorf("chapter %d time = %v, want %v", k, info.Chapters[k].Time, w)
		}
	}
	// The dedup triggers the renumbering branch, which forces English.
	if task.ChapterLanguage != "en" {
		t.Errorf("language = %q, want %q after dedup", task.ChapterLanguage, "en")
	}
	// And it warns, because the source was an external chapter file... except
	// this task is MKV, which does not warn.
	if task.Status.Chapter == model.ChapterWarn {
		t.Error("status = Warn, want MKV (the external-file warning does not apply)")
	}
}

func TestLoadChapterWarnsOnDedupForExternalFiles(t *testing.T) {
	t.Parallel()
	s, task, _ := externalChapterTask(t, "tchapter_xml_endtimes.json", 3_000_000)

	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if info == nil {
		t.Fatal("LoadChapter() = nil, want chapters")
	}
	if task.Status.Chapter != model.ChapterWarn {
		t.Errorf("status = %v, want Warn", task.Status.Chapter)
	}
	if task.ChapterLanguage != "en" {
		t.Errorf("language = %q, want %q", task.ChapterLanguage, "en")
	}
}

// TestLoadChapterRemovesTheBeginDuplicate covers the mkvmerge split artefact:
// a chapter at zero followed by another within 100 ms.
func TestLoadChapterRemovesTheBeginDuplicate(t *testing.T) {
	t.Parallel()
	body := `{"version":1,"format":"ogm","entries":[{"title":"","source":"","fps_num":0,"fps_den":1,"duration_ns":0,"chapters":[` +
		`{"name":"a","time_ns":0,"frames":-1},` +
		`{"name":"b","time_ns":50000000,"frames":-1},` +
		`{"name":"c","time_ns":60000000000,"frames":-1}]}]}`
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	chapterFile := filepath.Join(dir, "ep01.txt")
	writeFile(t, chapterFile)

	task := newTask(input, 600_000)
	task.Status.Chapter = model.ChapterYes
	task.ChapterFile = model.NewFileRef(chapterFile)

	s := &Service{Parser: fakeParser(body)}
	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if got := info.Count(); got != 2 {
		t.Fatalf("chapter count = %d (%v), want 2", got, timesMS(info))
	}
	// The second mark is dropped; the survivor keeps its own name because
	// nothing forced renumbering... except removeBegin does.
	if info.Chapters[0].Time != 0 {
		t.Errorf("first time = %v, want 0", info.Chapters[0].Time)
	}
	if info.Chapters[1].Time != 60*time.Second {
		t.Errorf("second time = %v, want 60s", info.Chapters[1].Time)
	}
	if info.Chapters[1].Name != "Chapter 02" {
		t.Errorf("second name = %q, want %q", info.Chapters[1].Name, "Chapter 02")
	}
	if task.Status.Chapter != model.ChapterWarn {
		t.Errorf("status = %v, want Warn", task.Status.Chapter)
	}
}

// TestLoadChapterKeepsAChapterAtZeroOnly: a single chapter at zero is not a
// split artefact and must survive.
func TestLoadChapterKeepsAChapterAtZeroOnly(t *testing.T) {
	t.Parallel()
	body := `{"version":1,"format":"ogm","entries":[{"title":"","source":"","fps_num":0,"fps_den":1,"duration_ns":0,"chapters":[` +
		`{"name":"a","time_ns":0,"frames":-1},` +
		`{"name":"b","time_ns":60000000000,"frames":-1}]}]}`
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	chapterFile := filepath.Join(dir, "ep01.txt")
	writeFile(t, chapterFile)

	task := newTask(input, 600_000)
	task.Status.Chapter = model.ChapterYes
	task.ChapterFile = model.NewFileRef(chapterFile)

	s := &Service{Parser: fakeParser(body)}
	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if got := info.Count(); got != 2 {
		t.Fatalf("chapter count = %d, want 2", got)
	}
	if task.Status.Chapter != model.ChapterYes {
		t.Errorf("status = %v, want Yes", task.Status.Chapter)
	}
	if info.Chapters[0].Name != "a" {
		t.Errorf("first name = %q, want %q (no renumbering happened)", info.Chapters[0].Name, "a")
	}
}

// TestLoadChapterEmptyListSkipsMuxing covers the legacy `return null` path: a
// list with a single chapter at zero is not worth muxing.
func TestLoadChapterEmptyListSkipsMuxing(t *testing.T) {
	t.Parallel()
	body := `{"version":1,"format":"ogm","entries":[{"title":"","source":"","fps_num":0,"fps_den":1,"duration_ns":0,"chapters":[` +
		`{"name":"a","time_ns":0,"frames":-1}]}]}`
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)
	chapterFile := filepath.Join(dir, "ep01.txt")
	writeFile(t, chapterFile)

	task := newTask(input, 600_000)
	task.Status.Chapter = model.ChapterYes
	task.ChapterFile = model.NewFileRef(chapterFile)

	s := &Service{Parser: fakeParser(body)}
	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if info != nil {
		t.Errorf("LoadChapter() = %+v, want nil for an empty list", info)
	}
}

func TestLoadChapterMissingExternalFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.m2ts")
	writeFile(t, input)

	task := newTask(input, 0)
	task.Status.Chapter = model.ChapterYes
	task.ChapterFile = model.NewFileRef(filepath.Join(dir, "gone.txt"))

	s := &Service{Parser: helperParser(t, "tchapter_ogm.json")}
	_, err := s.LoadChapter(t.Context(), task)
	if err == nil {
		t.Fatal("LoadChapter() = nil error, want a not-found error")
	}
	if got := okerr.AsError(err).Kind; got != okerr.KindNotFound {
		t.Errorf("Kind = %q, want %q", got, okerr.KindNotFound)
	}
}

func TestLoadChapterStatusNoIsANoOp(t *testing.T) {
	t.Parallel()
	task := newTask("ep01.m2ts", 0)
	task.Status.Chapter = model.ChapterNo
	// No parser is configured: the status must short-circuit before touching
	// one, which is what the nil result proves.
	info, err := (&Service{}).LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if info != nil {
		t.Errorf("LoadChapter() = %+v, want nil", info)
	}
}

func TestLoadChapterFromBlurayPlaylist(t *testing.T) {
	t.Parallel()
	root, stream := buildBluray(t, "00001.mpls")
	task := newTask(stream, 1_422_087)
	task.Status.Chapter = model.ChapterMaybe

	// The MPLS fixture's first entry is sourced from clip 00002, which is the
	// name of the stream file here.
	stream = filepath.Join(filepath.Dir(stream), "00002.m2ts")
	writeFile(t, stream)
	task.Inputs[0] = model.NewFileRef(stream)

	s := &Service{Parser: helperParser(t, "tchapter_mpls.json")}
	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if info == nil {
		t.Fatal("LoadChapter() = nil, want the playlist's chapters")
	}
	if info.SourceName != "00002" {
		t.Errorf("source = %q, want %q", info.SourceName, "00002")
	}
	if got := info.Count(); got != 6 {
		t.Errorf("chapter count = %d, want 6", got)
	}
	_ = root
}

func TestLoadChapterFromMatroska(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.mkv")
	writeFile(t, input)

	task := newTask(input, 35_000)
	task.Status.Chapter = model.ChapterMKV

	// The MKV path runs mkvextract and then the CLI, so the fake stands in for
	// both: mkvextract prints the extracted XML, and the CLI (fed that XML
	// through a temp file) prints the JSON.
	s := &Service{Parser: &Parser{
		Tool:       os.Args[0],
		MkvExtract: os.Args[0],
		Env:        helperEnv(roleStdout, `{"version":1,"format":"matroska_xml","entries":[{"title":"","source":"","fps_num":0,"fps_den":1,"duration_ns":0,"chapters":[{"name":"Chapter 01","time_ns":0,"frames":-1},{"name":"Chapter 02","time_ns":10000000000,"frames":-1},{"name":"Chapter 03","time_ns":20000000000,"frames":-1},{"name":"Chapter 04","time_ns":30000000000,"frames":-1}]}]}`),
	}}
	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if info == nil {
		t.Fatal("LoadChapter() = nil, want chapters")
	}
	if got := info.Count(); got != 4 {
		t.Errorf("chapter count = %d, want 4", got)
	}
}

// TestLoadChapterFromMatroskaNeedsMkvExtract pins the dependency: without an
// mkvextract path the MKV branch must fail loudly rather than hand the raw
// container to a CLI that cannot read EBML.
func TestLoadChapterFromMatroskaNeedsMkvExtract(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.mkv")
	writeFile(t, input)

	task := newTask(input, 35_000)
	task.Status.Chapter = model.ChapterMKV

	s := &Service{Parser: helperParser(t, "tchapter_matroska.json")}
	_, err := s.LoadChapter(t.Context(), task)
	if err == nil {
		t.Fatal("LoadChapter() = nil error, want a missing-mkvextract error")
	}
	if !strings.Contains(err.Error(), "mkvextract") {
		t.Errorf("error = %v, want it to name mkvextract", err)
	}
}

// TestLoadChapterFromMatroskaMissingBinaryIsAnError is the sharper form of the
// check above: a configured path that does not exist must not be mistaken for
// "the file has no chapters".
func TestLoadChapterFromMatroskaMissingBinaryIsAnError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.mkv")
	writeFile(t, input)

	task := newTask(input, 35_000)
	task.Status.Chapter = model.ChapterMKV

	s := &Service{Parser: &Parser{
		Tool:       os.Args[0],
		MkvExtract: filepath.Join(dir, "no-such-mkvextract.exe"),
		Env:        helperEnv(roleStdout, ""),
	}}
	_, err := s.LoadChapter(t.Context(), task)
	if err == nil {
		t.Fatal("LoadChapter() = nil error, want the spawn failure to surface")
	}
}

// TestLoadChapterFromMatroskaWithoutChapters covers a container that carries no
// chapter track: mkvextract fails with nothing on stdout and the service must
// treat that as "no chapters", not as an error.
func TestLoadChapterFromMatroskaWithoutChapters(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.mkv")
	writeFile(t, input)

	task := newTask(input, 35_000)
	task.Status.Chapter = model.ChapterMKV

	// Exit code 1 with an empty stdout is what mkvextract does when the file
	// has no chapters.
	s := &Service{Parser: &Parser{
		Tool:       os.Args[0],
		MkvExtract: os.Args[0],
		Env:        append(helperEnv(roleStdout, ""), helperExitEnvVar+"=1"),
	}}
	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v, want nil for an empty chapter list", err)
	}
	if info != nil {
		t.Errorf("LoadChapter() = %+v, want nil", info)
	}
}

func TestLoadChapterRequiresAParser(t *testing.T) {
	t.Parallel()
	task := newTask("ep01.mkv", 0)
	task.Status.Chapter = model.ChapterMKV
	if _, err := (&Service{}).LoadChapter(t.Context(), task); err == nil {
		t.Fatal("LoadChapter() = nil error, want a missing-tool error")
	}
}

func TestLoadChapterNilTask(t *testing.T) {
	t.Parallel()
	if _, err := (&Service{}).LoadChapter(t.Context(), nil); err == nil {
		t.Error("LoadChapter(nil) = nil error, want a rejection")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func TestInfoSortAndRenumber(t *testing.T) {
	t.Parallel()
	info := &Info{Chapters: []Chapter{
		{Number: 9, Time: 30 * time.Second, Name: "c"},
		{Number: 8, Time: 0, Name: "a"},
		{Number: 7, Time: 10 * time.Second, Name: "b"},
	}}
	info.Sort()
	want := []string{"a", "b", "c"}
	for k, w := range want {
		if info.Chapters[k].Name != w {
			t.Errorf("after Sort, chapter %d = %q, want %q", k, info.Chapters[k].Name, w)
		}
	}
	info.Renumber()
	for k := range info.Chapters {
		if info.Chapters[k].Number != k+1 {
			t.Errorf("after Renumber, chapter %d number = %d, want %d", k, info.Chapters[k].Number, k+1)
		}
	}
}

// TestInfoSortIsStable keeps the dedup step deterministic: two marks sharing a
// timestamp must not swap.
func TestInfoSortIsStable(t *testing.T) {
	t.Parallel()
	info := &Info{Chapters: []Chapter{
		{Time: 10 * time.Second, Name: "first"},
		{Time: 10 * time.Second, Name: "second"},
		{Time: 10 * time.Second, Name: "third"},
	}}
	info.Sort()
	want := []string{"first", "second", "third"}
	for k, w := range want {
		if info.Chapters[k].Name != w {
			t.Errorf("chapter %d = %q, want %q", k, info.Chapters[k].Name, w)
		}
	}
}

func TestChapterLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		index int
		want  string
	}{
		{1, "Chapter 01"},
		{2, "Chapter 02"},
		{9, "Chapter 09"},
		{10, "Chapter 10"},
		{99, "Chapter 99"},
		{100, "Chapter 100"},
		{101, "Chapter 101"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := ChapterLabel(tt.index); got != tt.want {
				t.Errorf("ChapterLabel(%d) = %q, want %q", tt.index, got, tt.want)
			}
		})
	}
}

func TestDedup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		in        []time.Duration
		want      []time.Duration
		wantCount int
	}{
		{"empty", nil, nil, 0},
		{"single", []time.Duration{0}, []time.Duration{0}, 0},
		{"no duplicates", []time.Duration{0, 10, 20}, []time.Duration{0, 10, 20}, 0},
		{"pair keeps the later", []time.Duration{0, 0, 10}, []time.Duration{0, 10}, 1},
		{"run of three keeps the last", []time.Duration{0, 0, 0, 10}, []time.Duration{0, 10}, 2},
		{"trailing pair", []time.Duration{0, 10, 10}, []time.Duration{0, 10}, 1},
		{"all equal", []time.Duration{5, 5, 5}, []time.Duration{5}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			in := make([]Chapter, 0, len(tt.in))
			for k, d := range tt.in {
				in = append(in, Chapter{Number: k + 1, Time: d})
			}
			got, removed := dedup(in)
			if removed != tt.wantCount {
				t.Errorf("removed = %d, want %d", removed, tt.wantCount)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("kept %d chapters, want %d", len(got), len(tt.want))
			}
			for k, w := range tt.want {
				if got[k].Time != w {
					t.Errorf("chapter %d time = %v, want %v", k, got[k].Time, w)
				}
			}
		})
	}
}

func TestDropTail(t *testing.T) {
	t.Parallel()
	chapters := []Chapter{
		{Time: 0},
		{Time: 1000 * time.Millisecond},
		{Time: 1999 * time.Millisecond},
		{Time: 2000 * time.Millisecond},
	}
	// With a 3000 ms video, `3000 - t > 1001` keeps t < 1999.
	got := dropTail(chapters, 3000)
	want := []time.Duration{0, 1000 * time.Millisecond}
	if len(got) != len(want) {
		t.Fatalf("kept %d chapters, want %d", len(got), len(want))
	}
	for k, w := range want {
		if got[k].Time != w {
			t.Errorf("chapter %d = %v, want %v", k, got[k].Time, w)
		}
	}
}

// TestDropTailIsFractional pins the comparison to the reference's double
// arithmetic: 3000 - 1998.5 = 1001.5 > 1001 keeps the mark, while truncating
// the milliseconds to 1998 would also keep it but truncating to 1999 would
// not. The boundary case is 1999.0 ms, which is dropped.
func TestDropTailIsFractional(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		at   time.Duration
		want bool
	}{
		{"just under the boundary is kept", 1998*time.Millisecond + 999*time.Microsecond, true},
		{"exactly at the boundary is dropped", 1999 * time.Millisecond, false},
		{"well past the boundary is dropped", 2500 * time.Millisecond, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := dropTail([]Chapter{{Time: tt.at}}, 3000)
			if kept := len(got) == 1; kept != tt.want {
				t.Errorf("dropTail(%v) kept = %v, want %v", tt.at, kept, tt.want)
			}
		})
	}
}

func TestTimesMS(t *testing.T) {
	t.Parallel()
	info := &Info{Chapters: []Chapter{
		{Time: 0},
		{Time: 41041 * time.Millisecond},
	}}
	if got, want := timesMS(info), "0, 41041"; got != want {
		t.Errorf("timesMS() = %q, want %q", got, want)
	}
}

func TestStemOf(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in, want string
	}{
		{"ep01.m2ts", "ep01"},
		{`dir/Show.01.m2ts`, "Show.01"},
		{"ep01", "ep01"},
		{".hidden", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := stemOf(tt.in); got != tt.want {
				t.Errorf("stemOf(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestServiceUsesRoots proves the FileRef indirection is honoured rather than
// the raw path being used.
func TestServiceUsesRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "ep01.mkv")
	writeFile(t, input)

	// The task carries a volume-qualified reference; the service has to
	// resolve it through Roots to find the file.
	task := &model.Task{
		Inputs: []model.FileRef{{Volume: "media", Rel: "/ep01.mkv"}},
	}
	s := &Service{
		Parser: helperMatroskaParser(t, "tchapter_matroska.json"),
		Roots:  map[string]string{"media": dir},
	}
	task.Status.Chapter = model.ChapterMKV
	task.LengthMS = 35_000

	info, err := s.LoadChapter(t.Context(), task)
	if err != nil {
		t.Fatalf("LoadChapter() error = %v", err)
	}
	if info == nil || info.Count() != 4 {
		t.Errorf("LoadChapter() = %+v, want four chapters", info)
	}
}

// TestLoadChapterHonoursContextCancellation proves the parser really goes
// through internal/proc: a canceled context must abort the run.
func TestLoadChapterHonoursContextCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := fakeParser(`{"version":1,"format":"ogm","entries":[]}`).Parse(ctx, "ep01.txt"); err == nil {
		t.Fatal("Parse(canceled ctx) = nil error, want a cancellation")
	}
}
