package chapter

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/wildcard"
)

// Timing thresholds, taken verbatim from ChapterService.LoadChapter. They are
// named because the numbers are otherwise magic in three separate places.
const (
	// dropTailMS removes chapters closer than this to the end of the video;
	// the last one is usually a trailing mark that does not belong in the
	// output.
	dropTailMS = 1001
	// beginDuplicateMS is how close to zero a second chapter must sit to be
	// treated as a split artefact.
	beginDuplicateMS = 100
	// warnTailMS flags a chapter list whose last mark is very close to the
	// end.
	warnTailMS = 3003
)

// Service loads and normalises chapters for one task.
//
// Every external dependency is a parameter: the tchapter CLI that parses
// chapter files and the ffprobe that answers the Matroska question. Nothing
// here reads a global tool path, and no behaviour branches on GOOS.
type Service struct {
	// Parser reads chapter files. Required for LoadChapter.
	Parser *Parser
	// Probe answers the Matroska chapter question. Required for
	// UpdateChapterStatus.
	Probe *Probe
	// Roots maps a logical volume to its local root, as
	// node.Capabilities.Volumes provides. It is used to resolve the task's
	// FileRefs; nil means the local volume only.
	Roots map[string]string
	// RenumberChapters mirrors TaskProfile.RenumberChapters: when set, every
	// loaded chapter list is renamed to "Chapter NN" and the language is
	// forced to English.
	RenumberChapters bool
}

// UpdateChapterStatus decides how a task will get its chapters. Mirrors
// ChapterService.UpdateChapterStatus, including the order of the tests: an
// external chapter file wins over a Blu-ray structure, which wins over
// chapters already inside the Matroska file.
func (s *Service) UpdateChapterStatus(ctx context.Context, task *model.Task) (model.ChapterStatus, error) {
	if task == nil {
		return model.ChapterNo, okerr.New(okerr.KindConfig, "任务为空", "无法为nil任务检测章节")
	}
	found, err := s.FindChapterFile(task)
	if err != nil {
		return model.ChapterNo, err
	}
	if found {
		return model.ChapterYes, nil
	}
	if s.HasBlurayStructure(task) {
		return model.ChapterMaybe, nil
	}
	has, err := s.HasMatroskaChapter(ctx, task)
	if err != nil {
		return model.ChapterNo, err
	}
	if has {
		return model.ChapterMKV, nil
	}
	return model.ChapterNo, nil
}

// HasBlurayStructure reports whether the input sits in a Blu-ray disc tree:
// a .m2ts inside a STREAM directory with a sibling PLAYLIST directory holding
// at least one .mpls. Mirrors ChapterService.HasBlurayStructure.
func (s *Service) HasBlurayStructure(task *model.Task) bool {
	input := s.inputPath(task)
	if !strings.EqualFold(filepath.Ext(input), ".m2ts") {
		log.Warn("输入文件不是蓝光原盘文件", "file", input)
		return false
	}
	streamDir := filepath.Dir(input)
	if !strings.EqualFold(filepath.Base(streamDir), "STREAM") {
		log.Warn("输入文件不在BDMV文件夹结构内", "file", input)
		return false
	}
	playlist := filepath.Join(filepath.Dir(streamDir), "PLAYLIST")
	if st, err := os.Stat(playlist); err != nil || !st.IsDir() {
		log.Warn("输入文件没有上级的PLAYLIST文件夹", "file", input)
		return false
	}
	entries, err := os.ReadDir(playlist)
	if err != nil {
		log.Warn("无法读取PLAYLIST文件夹", "dir", playlist, "err", err)
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && wildcard.Match(e.Name(), "*.mpls") {
			return true
		}
	}
	log.Warn("PLAYLIST文件夹里没有mpls文件", "dir", playlist)
	return false
}

// HasMatroskaChapter reports whether the input is a Matroska file that already
// carries chapters. Mirrors ChapterService.HasMatroskaChapter, with ffprobe in
// place of MediaInfo.
func (s *Service) HasMatroskaChapter(ctx context.Context, task *model.Task) (bool, error) {
	input := s.inputPath(task)
	if !strings.EqualFold(filepath.Ext(input), ".mkv") {
		log.Warn("输入文件不是Matroska文件", "file", input)
		return false, nil
	}
	if s.Probe == nil {
		return false, okerr.New(okerr.KindNotFound, "找不到外部工具",
			"未指定 %s 可执行文件路径", probeTool)
	}
	has, err := s.Probe.HasChapters(ctx, input)
	if err != nil {
		return false, err
	}
	if !has {
		log.Warn("Matroska文件内不含有章节", "file", input)
	}
	return has, nil
}

// FindChapterFile looks for an external chapter file next to the input and
// records it on the task. Mirrors ChapterService.FindChapterFile.
//
// The task's existing ChapterFile wins when it exists. Otherwise the input's
// directory is searched for `<stem>.*txt`; more than one match is an error
// because the language cannot be chosen for the operator.
//
// The language is the text between the input's stem and the extension of the
// chapter file, so `Show.01.jpn.txt` gives "jpn".
func (s *Service) FindChapterFile(task *model.Task) (bool, error) {
	if task == nil {
		return false, nil
	}
	if existing := s.chapterPath(task); existing != "" {
		if st, err := os.Stat(existing); err == nil && !st.IsDir() {
			return true, nil
		}
	}

	input := s.inputPath(task)
	dir := filepath.Dir(input)
	stem := stemOf(filepath.Base(input))
	pattern := stem + ".*txt"

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, okerr.Wrap(err, okerr.KindNotFound, "找不到目录",
				"%s 不存在", dir).WithFile(dir)
		}
		return false, okerr.Wrap(err, okerr.KindIO, "无法枚举目录", "%s: %v", dir, err).WithFile(dir)
	}

	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if wildcard.Match(e.Name(), pattern) {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) > 1 {
		return false, okerr.New(okerr.KindConfig, "找到多个章节文件",
			"%s 对应多个章节文件：%s", input, strings.Join(files, ",")).WithFile(input)
	}
	if len(files) == 0 {
		return false, nil
	}

	found := files[0]
	log.Warn("找到章节文件", "file", found)
	task.ChapterFile = model.NewFileRef(found)
	// The language is whatever sits between the input's stem and the chapter
	// file's extension, e.g. "jpn" in `Show.01.jpn.txt`.
	chapterStem := stemOf(filepath.Base(found))
	if len(chapterStem) > len(stem) {
		task.ChapterLanguage = chapterStem[len(stem)+1:]
	}
	log.Warn("章节文件语言", "file", found, "language", task.ChapterLanguage)
	return true, nil
}

// LoadChapter loads, cleans up and validates the chapters of a task.
//
// It mirrors ChapterService.LoadChapter, with two deliberate differences:
//
//   - The MediaInfo/MPLS/MKV branches all go through the tchapter CLI. For an
//     MPLS source the entry whose SourceName matches the input's clip name is
//     chosen, which is what GetChapterFromMPLS did; for a Matroska source the
//     chapters are extracted with mkvextract first, because the CLI has no
//     EBML reader.
//   - The legacy code threw a plain Exception for a missing chapter file;
//     here it is a structured error.
//
// A nil Info with a nil error means "the chapter list is empty, skip muxing
// chapters", matching the legacy `return null` path.
func (s *Service) LoadChapter(ctx context.Context, task *model.Task) (*Info, error) {
	if task == nil {
		return nil, okerr.New(okerr.KindConfig, "任务为空", "无法为nil任务加载章节")
	}
	// Only these three statuses have a chapter source to load; every other
	// value falls through to the legacy `default: return null`.
	switch task.Status.Chapter {
	case model.ChapterYes, model.ChapterMaybe, model.ChapterMKV:
	default:
		return nil, nil
	}
	if s.Parser == nil {
		return nil, okerr.New(okerr.KindNotFound, "找不到外部工具",
			"未指定 %s 可执行文件路径", Name)
	}

	var (
		info *Info
		err  error
	)
	switch task.Status.Chapter {
	case model.ChapterYes:
		if found, findErr := s.FindChapterFile(task); findErr != nil {
			return nil, findErr
		} else if !found {
			return nil, okerr.New(okerr.KindNotFound, "找不到章节文件",
				"%s 检测到使用外挂章节，但压制时未找到对应章节 %s",
				s.inputPath(task), s.chapterPath(task)).WithFile(s.inputPath(task))
		}
		info, err = s.Parser.ParseFirst(ctx, s.chapterPath(task))
	case model.ChapterMaybe:
		info, err = s.loadFromPlaylist(ctx, task)
	case model.ChapterMKV:
		// The CLI cannot read EBML; mkvextract has to run first, exactly as
		// the reference MATROSKAParser did.
		infos, parseErr := s.Parser.ParseMatroska(ctx, s.inputPath(task))
		if parseErr != nil {
			return nil, parseErr
		}
		if len(infos) > 0 {
			info = infos[0]
		}
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, nil
	}

	return s.normalise(task, info), nil
}

// loadFromPlaylist picks the MPLS entry that belongs to the input's clip.
// Mirrors ChapterService.GetChapterFromMPLS, which compared
// `chapter.SourceName + ".m2ts" == inputFile.Name`.
func (s *Service) loadFromPlaylist(ctx context.Context, task *model.Task) (*Info, error) {
	input := s.inputPath(task)
	playlistDir := filepath.Join(filepath.Dir(filepath.Dir(input)), "PLAYLIST")

	entries, err := os.ReadDir(playlistDir)
	if err != nil {
		return nil, okerr.Wrap(err, okerr.KindNotFound, "找不到PLAYLIST文件夹",
			"%s: %v", playlistDir, err).WithFile(playlistDir)
	}

	clip := stemOf(filepath.Base(input))
	for _, e := range entries {
		if e.IsDir() || !wildcard.Match(e.Name(), "*.mpls") {
			continue
		}
		infos, parseErr := s.Parser.Parse(ctx, filepath.Join(playlistDir, e.Name()))
		if parseErr != nil {
			return nil, parseErr
		}
		for _, candidate := range infos {
			if strings.EqualFold(candidate.SourceName, clip) {
				return candidate, nil
			}
		}
	}
	return nil, nil
}

// normalise applies the cleanup rules of ChapterService.LoadChapter: drop the
// trailing marks, collapse a split artefact at the start, remove duplicate
// timestamps, renumber, and set the status warnings.
//
// The comparisons use millis() rather than time.Duration.Milliseconds(): the
// reference compares TimeSpan.TotalMilliseconds, which is a double, and
// truncating first would move a boundary case such as 1000.9 ms across the
// `< 1001` test.
func (s *Service) normalise(task *model.Task, info *Info) *Info {
	lengthMS := task.LengthMS

	log.Debug("章节加载完成", "chapters", timesMS(info))

	// 丢弃末尾1秒的章节
	info.Sort()
	info.Chapters = dropTail(info.Chapters, lengthMS)

	// 处理开头的重复章节（一般来自mkvmerge切割）
	removeBegin := false
	if len(info.Chapters) >= 2 &&
		info.Chapters[0].Time == 0 &&
		millis(info.Chapters[1].Time)-millis(info.Chapters[0].Time) < beginDuplicateMS {
		info.Chapters = append(info.Chapters[:1], info.Chapters[2:]...)
		removeBegin = true
	}

	// 删除相同的章节
	removeDup := false
	if deduped, removed := dedup(info.Chapters); removed > 0 {
		removeDup = true
		info.Chapters = deduped
		log.Debug("章节去重", "removed", removed)
	}

	log.Debug("章节整理完成", "chapters", timesMS(info))

	// 章节重命名
	if s.RenumberChapters || removeBegin || removeDup {
		for k := range info.Chapters {
			info.Chapters[k].Name = ChapterLabel(k + 1)
		}
		task.ChapterLanguage = "en"
	}

	if task.Status.Chapter == model.ChapterYes && (removeBegin || removeDup) {
		task.Status.Chapter = model.ChapterWarn
		log.Warn("使用外挂章节，但触发了章节去重，这可能导致章节内容和语言不符合预期，请注意检查",
			"file", s.inputPath(task))
	}

	// 章节序号重排序，对应章节文件中的 CHAPTERxx= / CHAPTERxxNAME=
	info.Renumber()

	if len(info.Chapters) > 1 ||
		(len(info.Chapters) == 1 && info.Chapters[0].Time > 0) {
		last := millis(info.Chapters[len(info.Chapters)-1].Time)
		if float64(lengthMS)-last < warnTailMS {
			task.Status.Chapter = model.ChapterWarn
		}
		if info.Chapters[0].Time != 0 {
			task.Status.Chapter = model.ChapterWarn
		}
		return info
	}

	log.Info("对应章节为空，跳过封装", "file", filepath.Base(s.inputPath(task)))
	return nil
}

// millis is time.Duration.TotalMilliseconds: a fractional value, matching the
// double the reference compares.
func millis(d time.Duration) float64 {
	return float64(d) / float64(time.Millisecond)
}

// dropTail removes the chapters that fall within dropTailMS of the end.
// Mirrors the LINQ filter `LengthInMiliSec - x.Time.TotalMilliseconds > 1001`.
func dropTail(chapters []Chapter, lengthMS int64) []Chapter {
	out := chapters[:0]
	for _, c := range chapters {
		if float64(lengthMS)-millis(c.Time) > dropTailMS {
			out = append(out, c)
		}
	}
	return out
}

// dedup removes a chapter whose timestamp equals the next one's, keeping the
// later entry. Mirrors the loop in LoadChapter.
func dedup(chapters []Chapter) ([]Chapter, int) {
	if len(chapters) < 2 {
		return chapters, 0
	}
	out := make([]Chapter, 0, len(chapters))
	removed := 0
	for i := 0; i < len(chapters)-1; i++ {
		if chapters[i].Time == chapters[i+1].Time {
			removed++
			continue
		}
		out = append(out, chapters[i])
	}
	out = append(out, chapters[len(chapters)-1])
	return out, removed
}

// timesMS renders the chapter timestamps for the debug log, mirroring the
// `string.Join(", ", Chapters.Select(x => x.Time.TotalMilliseconds))` the
// legacy code logged.
func timesMS(info *Info) string {
	var b strings.Builder
	for k, c := range info.Chapters {
		if k > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.FormatInt(c.Time.Milliseconds(), 10))
	}
	return b.String()
}

// inputPath resolves the task's first input.
func (s *Service) inputPath(task *model.Task) string {
	if task == nil || len(task.Inputs) == 0 {
		return ""
	}
	return task.Inputs[0].Resolve(s.Roots)
}

// chapterPath resolves the task's chapter file, or "" when none is set.
func (s *Service) chapterPath(task *model.Task) string {
	if task == nil || task.ChapterFile.IsZero() {
		return ""
	}
	return task.ChapterFile.Resolve(s.Roots)
}

// stemOf returns the file name without its directory and extension, mirroring
// Path.GetFileNameWithoutExtension.
func stemOf(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}
