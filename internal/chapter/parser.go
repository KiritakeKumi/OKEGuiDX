package chapter

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/proc"
)

// parseResult is the JSON document `tchapter info <file> [format]` writes to
// stdout. The contract is defined in native/tchapter/src/tc_cli.c.
type parseResult struct {
	Version int          `json:"version"`
	Format  string       `json:"format"`
	Entries []parseEntry `json:"entries"`
	// Error is set instead of the fields above when the parse failed; the CLI
	// also exits non-zero, so it is mostly useful for the message.
	Error *parseError `json:"error"`
}

type parseError struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type parseEntry struct {
	Title      string         `json:"title"`
	Source     string         `json:"source"`
	FPSNum     int64          `json:"fps_num"`
	FPSDen     int64          `json:"fps_den"`
	DurationNS int64          `json:"duration_ns"`
	Chapters   []parseChapter `json:"chapters"`
}

type parseChapter struct {
	Name   string `json:"name"`
	TimeNS int64  `json:"time_ns"`
	Frames int64  `json:"frames"`
}

// Parser reads chapter files through the libtchapter CLI.
//
// The CLI is a sidecar process rather than a cgo binding on purpose: linking
// the C library would cost pure GOOS/GOARCH cross-compilation for all five
// targets (PLAN.md §3).
type Parser struct {
	// Tool is the absolute path to the `tchapter` executable. It is required;
	// nothing is discovered here.
	Tool string
	// MkvExtract is the absolute path to `mkvextract`. It is required only by
	// ParseMatroska, because the CLI has no EBML reader and the reference
	// MATROSKAParser ran mkvextract before handing the XML to its XML parser.
	MkvExtract string
	// Priority is applied to the child process. Zero means the package
	// default.
	Priority proc.Priority
	// Format optionally forces the input format, matching the CLI's second
	// argument ("ogm", "mpls", "matroska", ...). Empty lets the CLI detect it.
	Format string
	// Env is the child's full environment. Nil means inherit, which is what
	// production wants; tests use it to keep the process environment intact.
	Env []string
}

// Name is the tool name used in logs and errors.
const Name = "tchapter"

// mkvExtractTool is the tool name used in logs and errors for mkvextract.
const mkvExtractTool = "mkvextract"

// Parse reads every entry of a chapter file, using the parser's configured
// format.
func (p *Parser) Parse(ctx context.Context, path string) ([]*Info, error) {
	return p.ParseFormat(ctx, path, p.Format)
}

// ParseFormat reads every entry of a chapter file, forcing one CLI format.
//
// A parse failure is a structured error carrying the CLI's own status and
// message, so the operator sees "E_FORMAT: ..." rather than a generic failure.
func (p *Parser) ParseFormat(ctx context.Context, path, format string) ([]*Info, error) {
	if p.Tool == "" {
		return nil, okerr.New(okerr.KindNotFound, "找不到外部工具",
			"未指定 %s 可执行文件路径", Name)
	}
	args := []string{"info", path}
	if format != "" {
		args = append(args, format)
	}
	priority := p.Priority
	if priority == 0 {
		priority = proc.DefaultPriority
	}

	res, err := proc.Run(ctx, proc.Spec{
		Path:     p.Tool,
		Args:     args,
		Env:      p.Env,
		Priority: priority,
		Name:     Name,
	})
	if err != nil {
		return nil, err
	}

	var doc parseResult
	if jsonErr := json.Unmarshal([]byte(res.Stdout), &doc); jsonErr != nil {
		return nil, okerr.Wrap(jsonErr, okerr.KindTool, "章节解析失败",
			"%s 的输出不是合法 JSON", Name).
			WithTool(Name, res.ExitCode).
			WithFile(path).
			WithOutput(res.Stdout)
	}
	if doc.Error != nil {
		return nil, okerr.New(okerr.KindTool, "章节解析失败", "%s: %s",
			doc.Error.Status, doc.Error.Message).
			WithTool(Name, res.ExitCode).
			WithFile(path).
			WithOutput(res.Stdout)
	}

	infos := make([]*Info, 0, len(doc.Entries))
	for _, entry := range doc.Entries {
		infos = append(infos, entry.toInfo())
	}
	return infos, nil
}

// ParseFirst reads a file and returns its first entry, which is what
// ChapterService.LoadChapter uses for every source but MPLS. It returns a nil
// Info (and no error) when the file holds no entries.
func (p *Parser) ParseFirst(ctx context.Context, path string) (*Info, error) {
	infos, err := p.Parse(ctx, path)
	if err != nil {
		return nil, err
	}
	if len(infos) == 0 {
		return nil, nil
	}
	return infos[0], nil
}

// ParseMatroska reads the chapters embedded in a Matroska file.
//
// libtchapter has no EBML reader: its Matroska parser takes the XML that
// `mkvextract chapters` writes, which is exactly what the reference
// MATROSKAParser did (it ran mkvextract and handed the stdout to XMLParser).
// The extraction therefore happens here, and its output is parsed as the
// Matroska XML format.
//
// A Matroska file with no chapter track makes mkvextract exit non-zero with
// "Error: ..." on stderr and nothing on stdout; the reference threw
// "No Chapter Found" there, and the chapter service treats an empty list as
// "skip chapters", so that case returns no entries rather than an error.
func (p *Parser) ParseMatroska(ctx context.Context, path string) ([]*Info, error) {
	if p.MkvExtract == "" {
		return nil, okerr.New(okerr.KindNotFound, "找不到外部工具",
			"未指定 %s 可执行文件路径", mkvExtractTool)
	}
	priority := p.Priority
	if priority == 0 {
		priority = proc.DefaultPriority
	}

	res, err := proc.Run(ctx, proc.Spec{
		Path:     p.MkvExtract,
		Args:     []string{"chapters", path},
		Env:      p.Env,
		Priority: priority,
		Name:     mkvExtractTool,
	})
	if err != nil {
		// A non-zero exit with nothing on stdout is what mkvextract does when
		// the file simply has no chapter track: not a failure, an empty list.
		// A nil result means the process never ran at all (a missing binary,
		// for instance), and that must stay an error.
		if res != nil && strings.TrimSpace(res.Stdout) == "" {
			return nil, nil
		}
		return nil, err
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return nil, nil
	}

	return p.parseDocument(ctx, res.Stdout, "matroska")
}

// parseDocument runs the CLI over an in-memory XML document by way of a
// temporary file, because the CLI only reads paths. It is used for mkvextract
// output, which arrives on stdout.
func (p *Parser) parseDocument(ctx context.Context, body, format string) ([]*Info, error) {
	f, err := os.CreateTemp("", "okegui-chapters-*.xml")
	if err != nil {
		return nil, okerr.Wrap(err, okerr.KindIO, "无法写入临时章节文件", "%v", err)
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := f.WriteString(body); err != nil {
		_ = f.Close()
		return nil, okerr.Wrap(err, okerr.KindIO, "无法写入临时章节文件", "%s: %v", name, err)
	}
	if err := f.Close(); err != nil {
		return nil, okerr.Wrap(err, okerr.KindIO, "无法写入临时章节文件", "%s: %v", name, err)
	}

	return p.ParseFormat(ctx, name, format)
}

func (e parseEntry) toInfo() *Info {
	info := &Info{
		Title:      e.Title,
		SourceName: e.Source,
		FPSNum:     e.FPSNum,
		FPSDen:     e.FPSDen,
		Duration:   time.Duration(e.DurationNS),
	}
	if e.FPSDen == 0 {
		// The CLI guarantees a non-zero denominator; guard anyway so a
		// malformed document cannot produce a division by zero later.
		info.FPSNum, info.FPSDen = 0, 0
	}
	info.Chapters = make([]Chapter, 0, len(e.Chapters))
	for k, c := range e.Chapters {
		info.Chapters = append(info.Chapters, Chapter{
			Number: k + 1,
			Time:   time.Duration(c.TimeNS),
			Name:   c.Name,
		})
	}
	return info
}
