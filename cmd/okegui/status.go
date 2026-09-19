package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// statusOptions are the flags of `okegui status`.
type statusOptions struct {
	options

	// asJSON prints the machine-readable form, which is what a monitoring
	// script wants.
	asJSON bool
	// verbose adds the resolved paths and the volume map.
	verbose bool
}

// statusCommand prints what this node can do and what the queue holds. It
// exits 0 whenever the information could be gathered: a node that is missing
// tools is a reportable state, not a failure of the command.
func (a *application) statusCommand(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	var o statusOptions
	o.bind(fs)
	fs.BoolVar(&o.asJSON, "json", false, "以 JSON 输出")
	fs.BoolVar(&o.verbose, "verbose", false, "附加路径与卷映射")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelp) {
			printStatusUsage(a.stdout)
		}
		return err
	}
	if fs.NArg() > 0 {
		return fail(exitUsage, "status 不接受位置参数：%v", fs.Args())
	}

	s, err := a.bootstrap(&o.options)
	if err != nil {
		return err
	}
	defer a.closeLog(s)

	// A queue that cannot be read is part of the status, not a failure of the
	// status command, so the error is carried into the report.
	queue, queueErr := readQueue(s.queuePath)

	if o.asJSON {
		return writeStatusJSON(a.stdout, s, queue)
	}
	writeStatusText(a.stdout, s, &o, queue, queueErr)
	return nil
}

// queueStatus summarises the persisted task queue.
type queueStatus struct {
	Path     string `json:"path"`
	Total    int    `json:"total"`
	Waiting  int    `json:"waiting"`
	Running  int    `json:"running"`
	Finished int    `json:"finished"`
	Failed   int    `json:"failed"`
	// Error is set when the queue file exists but could not be read, in which
	// case every count is zero.
	Error string `json:"error,omitempty"`
}

// readQueue loads the queue file and summarises it. A missing file means an
// empty queue; a damaged one is reported through the returned error.
func readQueue(path string) (*queueStatus, error) {
	q := &queueStatus{Path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return q, nil
		}
		q.Error = err.Error()
		return q, err
	}

	// The schema is engine-internal; only the fields the summary needs are
	// decoded here, so a queue written by a newer engine still yields counts.
	var file struct {
		Tasks []struct {
			Task struct {
				Status model.TaskStatus `json:"status"`
			} `json:"task"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		q.Error = err.Error()
		return q, err
	}
	q.Total = len(file.Tasks)
	for _, entry := range file.Tasks {
		switch entry.Task.Status.Progress {
		case model.TaskRunning:
			q.Running++
		case model.TaskFinished:
			q.Finished++
		case model.TaskError:
			q.Failed++
		default:
			q.Waiting++
		}
	}
	return q, nil
}

// nodeReport is the JSON shape of `okegui status -json`.
type nodeReport struct {
	Version      string            `json:"version"`
	Capabilities node.Capabilities `json:"capabilities"`
	Queue        *queueStatus      `json:"queue"`
	QueuePath    string            `json:"queue_path"`
	LogPath      string            `json:"log_path,omitempty"`
	// ToolError is the structured message naming the mandatory tools that are
	// missing, empty when the node is ready to work.
	ToolError string `json:"tool_error,omitempty"`
}

// writeStatusJSON prints the machine-readable report.
func writeStatusJSON(w io.Writer, s *settings, q *queueStatus) error {
	report := nodeReport{
		Version:      version,
		Capabilities: s.caps,
		Queue:        q,
		QueuePath:    s.queuePath,
		LogPath:      s.logPath,
	}
	if err := toolchain.CheckRequired(s.caps); err != nil {
		report.ToolError = err.Error()
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return wrapExit(exitFailure, err)
	}
	return nil
}

// writeStatusText prints the human-readable report. toolchain.Describe renders
// the capability block, so `status` and the startup log cannot drift apart.
func writeStatusText(w io.Writer, s *settings, o *statusOptions, q *queueStatus, queueErr error) {
	fmt.Fprintf(w, "okegui %s (%s/%s)\n", version, s.caps.OS, s.caps.Arch)
	fmt.Fprint(w, toolchain.Describe(s.caps))

	if err := toolchain.CheckRequired(s.caps); err != nil {
		fmt.Fprintf(w, "必需工具: 缺失（%s）\n", err.Error())
	} else {
		fmt.Fprintln(w, "必需工具: 齐备")
	}

	if o.verbose {
		printPaths(w, s)
	}

	fmt.Fprintf(w, "\n任务队列: %s\n", q.Path)
	if queueErr != nil {
		fmt.Fprintf(w, "  无法读取: %v\n", queueErr)
		return
	}
	fmt.Fprintf(w, "  共 %d 个任务：等待中 %d，运行中 %d，完成 %d，失败 %d\n",
		q.Total, q.Waiting, q.Running, q.Finished, q.Failed)
}

// printPaths lists the resolved locations and the volume map, which is what an
// operator needs when a run cannot find its files.
func printPaths(w io.Writer, s *settings) {
	fmt.Fprintf(w, "\n配置目录: %s\n", s.configDir)
	fmt.Fprintf(w, "任务队列: %s\n", s.queuePath)
	if s.logPath != "" {
		fmt.Fprintf(w, "日志文件: %s\n", s.logPath)
	} else {
		fmt.Fprintln(w, "日志文件: （未启用，日志输出到 stderr）")
	}

	volumes := make([]string, 0, len(s.caps.Volumes))
	for name := range s.caps.Volumes {
		volumes = append(volumes, name)
	}
	sort.Strings(volumes)
	for _, name := range volumes {
		root := s.caps.Volumes[name]
		if root == "" {
			root = "(文件系统根)"
		}
		fmt.Fprintf(w, "卷 %s -> %s\n", name, root)
	}

	if len(s.caps.Features) > 0 {
		fmt.Fprintf(w, "特性: %s\n", strings.Join(s.caps.Features, ", "))
	}
}

// printStatusUsage writes the help text of `okegui status`.
func printStatusUsage(w io.Writer) {
	fmt.Fprint(w, `用法: okegui status [选项]

打印本节点能力（工具、特性、NUMA）与任务队列状态。

选项:
  --json          以 JSON 输出
  --verbose       附加路径与卷映射
  --queue PATH    任务队列文件，默认为配置目录下的 queue.json
  --tools DIR     tools 目录所在位置，默认为可执行文件所在目录
  --config PATH   OKEGuiConfig.json 的路径
  --role NAME     节点角色，只接受 standalone
  --log-level LV  日志级别: TRACE / DEBUG / INFO / WARN / ERROR

退出码:
  0  信息已打印
  1  内部错误
  3  参数或配置错误
`)
}
