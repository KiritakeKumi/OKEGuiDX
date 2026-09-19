package chapter

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// probeTool is the tool name used in logs and errors.
const probeTool = "ffprobe"

// ProbeToolName returns the toolchain key of the probe, so callers can look it
// up in node.Capabilities without importing this package's constants.
func ProbeToolName() string { return probeTool }

// ffprobeArgs is the exact command line the probe runs:
//
//	-v error              only real failures are printed
//	-show_chapters        the Matroska chapter list
//	-show_entries format=duration  the container duration, for the callers
//	                              that want a length alongside the chapters
//	-of json              machine-readable, and stable across versions
//
// This is the direct replacement for the legacy
//
//	MI.Open(file); MI.Option("Complete");
//	int.TryParse(MI.Get(StreamKind.General, 0, "MenuCount"), out var MenuCount);
//
// MediaInfo's MenuCount is the number of chapter entries in the container, so
// "has chapters" is exactly "the chapter list is non-empty".
func ffprobeArgs(path string) []string {
	return []string{
		"-v", "error",
		"-show_chapters",
		"-show_entries", "format=duration",
		"-of", "json",
		path,
	}
}

// ffprobeOutput is the part of ffprobe's JSON the probe reads.
type ffprobeOutput struct {
	Chapters []ffprobeChapter `json:"chapters"`
	Format   struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

type ffprobeChapter struct {
	ID        int64  `json:"id"`
	TimeBase  string `json:"time_base"`
	Start     int64  `json:"start"`
	StartTime string `json:"start_time"`
	End       int64  `json:"end"`
	EndTime   string `json:"end_time"`
	Tags      struct {
		Title string `json:"title"`
	} `json:"tags"`
}

// Probe answers the questions the chapter service used to ask MediaInfo.
type Probe struct {
	// Tool is the absolute path to ffprobe. Required.
	Tool string
	// Priority is applied to the child process. Zero means the package
	// default.
	Priority proc.Priority
	// Env is the child's full environment. Nil means inherit.
	Env []string
}

// ProbeResult is what one ffprobe run found.
type ProbeResult struct {
	// ChapterCount is the number of chapter entries, the equivalent of
	// MediaInfo's MenuCount.
	ChapterCount int
	// DurationMS is the container duration in milliseconds, or -1 when the
	// container does not report one.
	DurationMS int64
}

// HasChapters reports whether the Matroska file carries chapters. It is the
// replacement for ChapterService.HasMatroskaChapter's MediaInfo call.
func (p *Probe) HasChapters(ctx context.Context, path string) (bool, error) {
	res, err := p.Run(ctx, path)
	if err != nil {
		return false, err
	}
	return res.ChapterCount > 0, nil
}

// Run executes ffprobe and parses its report.
func (p *Probe) Run(ctx context.Context, path string) (ProbeResult, error) {
	if p.Tool == "" {
		return ProbeResult{}, okerr.New(okerr.KindNotFound, "找不到外部工具",
			"未指定 %s 可执行文件路径", probeTool)
	}
	priority := p.Priority
	if priority == 0 {
		priority = proc.DefaultPriority
	}

	res, err := proc.Run(ctx, proc.Spec{
		Path:     p.Tool,
		Args:     ffprobeArgs(path),
		Env:      p.Env,
		Priority: priority,
		Name:     probeTool,
	})
	if err != nil {
		return ProbeResult{}, err
	}

	out := ProbeResult{DurationMS: -1}
	// ffprobe prints nothing at all for some containers; an empty document is
	// "no chapters", not a failure.
	body := strings.TrimSpace(res.Stdout)
	if body == "" {
		return out, nil
	}
	var doc ffprobeOutput
	if jsonErr := json.Unmarshal([]byte(body), &doc); jsonErr != nil {
		return ProbeResult{}, okerr.Wrap(jsonErr, okerr.KindTool, "无法解析ffprobe输出",
			"%s 的输出不是合法 JSON", probeTool).
			WithTool(probeTool, res.ExitCode).
			WithFile(path).
			WithOutput(res.Stdout)
	}
	out.ChapterCount = len(doc.Chapters)
	if doc.Format.Duration != "" {
		seconds, parseErr := strconv.ParseFloat(doc.Format.Duration, 64)
		if parseErr == nil {
			out.DurationMS = int64(seconds*1000 + 0.5)
		}
	}
	return out, nil
}
