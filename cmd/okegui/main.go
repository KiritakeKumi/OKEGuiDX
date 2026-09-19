// Command okegui is the headless front end of OKEGuiDX: it parses the command
// line, sets up logging and the toolchain, and hands the work to internal/engine.
//
// The engine is a daemon by design (PLAN.md §1): this binary is its first
// client, not its owner. Nothing in this package encodes, muxes or demuxes —
// every subcommand ends in an engine or toolchain call.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/platform"
	"github.com/KiritakeKumi/OKEGuiDX/internal/toolchain"
)

// version is the reported build version. It is a variable so a release build
// can set it with -ldflags "-X main.version=...".
var version = "0.0.0-dev"

// errVersion is returned when --version was requested.
var errVersion = errors.New("version requested")

// main is a thin wrapper: run does the work and returns an exit code, which
// keeps os.Exit out of the testable path.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses args and dispatches a subcommand. stdout and stderr are injected
// so the whole command line is testable without touching the real streams.
func run(args []string, stdout, stderr io.Writer) int {
	// The logger writes to stderr by default; making it explicit keeps the
	// progress line and the log interleaved in the order they happened.
	log.SetOutput(stderr)
	cmd, rest, err := parseCommand(args)
	switch {
	case errors.Is(err, errHelp):
		printUsage(stdout)
		return exitOK
	case errors.Is(err, errVersion):
		fmt.Fprintf(stdout, "okegui %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return exitOK
	case err != nil:
		fmt.Fprintf(stderr, "okegui: %v\n", err)
		fmt.Fprintln(stderr, "运行 'okegui --help' 查看用法。")
		return exitUsage
	}

	ctx, stop := signalContext()
	defer stop()

	app := &application{stdout: stdout, stderr: stderr, ctx: ctx}
	if err := cmd.run(app, rest); err != nil {
		// A subcommand that printed its own usage is not a failure.
		if errors.Is(err, errHelp) {
			return exitOK
		}
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			log.Warn("已收到终止信号，正在退出")
			fmt.Fprintln(stderr, "okegui: 已中断")
			return exitInterrupted
		}
		printError(stderr, err)
		return exitCodeOf(err)
	}
	return exitOK
}

// signalContext returns a context cancelled by SIGINT/SIGTERM, and the function
// that stops listening.
//
// The first signal starts a graceful shutdown. A second one restores the
// default handler, so a shutdown that is taking too long can still be
// abandoned with another Ctrl-C instead of leaving the operator waiting.
func signalContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case <-ch:
			cancel()
		case <-ctx.Done():
			return
		}
		signal.Reset(os.Interrupt, syscall.SIGTERM)
	}()
	return ctx, func() {
		signal.Stop(ch)
		cancel()
	}
}

// command is one subcommand. Commands own their flag set, so `okegui run -h`
// and `okegui --help` can differ. usage is the one-line form shown by the
// top-level help; each command prints its own full text.
type command struct {
	name    string
	summary string
	run     func(app *application, args []string) error
}

// commands is the command table, in the order the help text lists them.
var commands = []*command{
	{
		name:    "run",
		summary: "运行一个或多个任务配置（profile）",
		run:     (*application).runCommand,
	},
	{
		name:    "daemon",
		summary: "启动常驻服务（HTTP/WS 接口由 E2/E3 提供）",
		run:     (*application).daemonCommand,
	},
	{
		name:    "status",
		summary: "打印本节点能力与队列状态",
		run:     (*application).statusCommand,
	},
	{
		name:    "gui",
		summary: "启动服务并打开浏览器界面",
		run:     (*application).guiCommand,
	},
}

// findCommand returns the command with the given name.
func findCommand(name string) *command {
	for _, c := range commands {
		if c.name == name {
			return c
		}
	}
	return nil
}

// parseCommand splits the argument list into a subcommand and its own
// arguments. A bare invocation and -h/--help print the usage text.
func parseCommand(args []string) (*command, []string, error) {
	if len(args) == 0 {
		return nil, nil, errHelp
	}
	switch args[0] {
	case "-h", "--help", "help":
		return nil, nil, errHelp
	case "-v", "--version", "version":
		return nil, nil, errVersion
	}
	cmd := findCommand(args[0])
	if cmd == nil {
		return nil, nil, fmt.Errorf("未知命令 %q", args[0])
	}
	return cmd, args[1:], nil
}

// printUsage writes the top-level help text.
func printUsage(w io.Writer) {
	fmt.Fprintf(w, "okegui %s — OKEGuiDX 命令行入口 (%s/%s)\n\n", version, runtime.GOOS, runtime.GOARCH)
	fmt.Fprintln(w, "用法: okegui <命令> [选项] [参数]")
	fmt.Fprintln(w, "\n命令:")
	for _, c := range commands {
		fmt.Fprintf(w, "  %-8s %s\n", c.name, c.summary)
	}
	fmt.Fprintln(w, "\n选项:")
	fmt.Fprintln(w, "  -h, --help     显示帮助")
	fmt.Fprintln(w, "  -v, --version  显示版本")
	fmt.Fprintln(w, "\n退出码:")
	fmt.Fprintln(w, "  0   成功")
	fmt.Fprintln(w, "  1   内部错误")
	fmt.Fprintln(w, "  2   任务执行失败")
	fmt.Fprintln(w, "  3   参数或配置错误")
	fmt.Fprintln(w, "  130 被中断")
	fmt.Fprintln(w, "\n用 'okegui <命令> --help' 查看某个命令的选项。")
}

// application carries the process-wide dependencies of every subcommand.
type application struct {
	stdout io.Writer
	stderr io.Writer
	ctx    context.Context

	// config is resolved once per invocation by bootstrap.
	config configLoader
}

// options are the flags every subcommand accepts.
type options struct {
	role       string
	toolsRoot  string
	configPath string
	logLevel   string
	logDir     string
	queuePath  string
}

// bind registers the shared options on a flag set.
func (o *options) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.role, "role", string(node.RoleStandalone),
		"节点角色: standalone / coordinator / worker（仅 standalone 可用）")
	fs.StringVar(&o.toolsRoot, "tools", "",
		"tools 目录所在位置，默认为可执行文件所在目录")
	fs.StringVar(&o.configPath, "config", "",
		"OKEGuiConfig.json 的路径，默认为用户配置目录")
	fs.StringVar(&o.logLevel, "log-level", "",
		"日志级别: TRACE / DEBUG / INFO / WARN / ERROR（默认为配置文件中的值）")
	fs.StringVar(&o.logDir, "log-dir", "",
		"日志文件目录；run/status 留空则只写 stderr，daemon/gui 留空则写入配置目录下的 log/")
	fs.StringVar(&o.queuePath, "queue", "",
		"任务队列文件，默认为配置目录下的 queue.json")
}

// roleValue validates the --role value. node.ParseRole silently defaults to
// standalone, which is right for a configuration file but wrong for a command
// line: a typo must not turn a coordinator into a standalone node by accident.
// An empty value means the flag was never set, and the standalone default
// applies.
func (o *options) roleValue() (node.Role, error) {
	if o.role == "" {
		return node.RoleStandalone, nil
	}
	switch node.Role(o.role) {
	case node.RoleStandalone:
		return node.RoleStandalone, nil
	case node.RoleCoordinator, node.RoleWorker:
		return node.Role(o.role), fmt.Errorf(
			"角色 %s 尚未实现：本轮只支持 standalone（见 CLUSTER.md）", o.role)
	default:
		return "", fmt.Errorf("无效的 --role %q：只接受 standalone / coordinator / worker", o.role)
	}
}

// settings is the resolved configuration of one invocation.
type settings struct {
	role      node.Role
	caps      node.Capabilities
	logPath   string
	logFile   *os.File
	queuePath string
	configDir string
}

// setLogLevel applies a level and then re-points the logger at the command's
// stderr. internal/log rebuilds its handler on os.Stderr when the level
// changes, so the output must be re-installed afterwards; the log package is
// frozen, so the ordering lives here.
func setLogLevel(w io.Writer, level string) {
	log.SetLevel(level)
	log.SetOutput(w)
}

// bootstrap loads the application configuration, configures logging and
// discovers the tools. Every subcommand calls it first, so an unusable
// installation fails before any task starts.
func (a *application) bootstrap(o *options) (*settings, error) {
	role, err := o.roleValue()
	if err != nil {
		return nil, wrapExit(exitUsage, err)
	}

	// An explicit --log-level governs everything, including the messages the
	// configuration loader may emit; otherwise the configured level applies
	// once the file has been read.
	if o.logLevel != "" {
		setLogLevel(a.stderr, o.logLevel)
	}
	a.config.load(o.configPath)

	level := o.logLevel
	if level == "" {
		level = a.config.LogLevel()
	}
	setLogLevel(a.stderr, level)

	s := &settings{role: role}

	if o.logDir != "" {
		path, f, err := platform.SetupLogging(o.logDir, level)
		if err != nil {
			// A missing log file must not stop a run: stderr still works.
			log.Warn("无法写入日志文件，日志只输出到 stderr", "dir", o.logDir, "err", err)
		} else {
			s.logPath, s.logFile = path, f
		}
	}

	caps, err := toolchain.Discover(toolchain.Options{
		Root:       o.toolsRoot,
		Explicit:   a.config.Explicit(),
		Role:       role,
		SingleNUMA: a.config.SingleNUMA(),
	})
	if err != nil {
		a.closeLog(s)
		return nil, err
	}
	s.caps = caps

	s.configDir, err = a.config.DirPath()
	if err != nil {
		a.closeLog(s)
		return nil, err
	}
	s.queuePath, err = a.resolveQueuePath(o)
	if err != nil {
		a.closeLog(s)
		return nil, err
	}
	return s, nil
}

// resolveQueuePath decides where the persisted task queue lives. The
// configuration directory is only created when the queue actually falls back to
// it, so a run that names its own queue never touches the user profile.
func (a *application) resolveQueuePath(o *options) (string, error) {
	if o.queuePath == "" {
		dir, err := a.config.Dir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, queueFileName), nil
	}
	abs, err := filepath.Abs(o.queuePath)
	if err != nil {
		return "", wrapExit(exitUsage, err)
	}
	return abs, nil
}

// closeLog closes the log file if one was opened. It runs on every exit path
// because Windows will not delete an open file.
func (a *application) closeLog(s *settings) {
	if s != nil && s.logFile != nil {
		_ = s.logFile.Close()
		s.logFile = nil
	}
}

// parseFlags parses a subcommand's arguments, mapping the flag package's own
// errors onto the usage exit code and turning -h into errHelp.
func parseFlags(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return errHelp
		}
		return wrapExit(exitUsage, err)
	}
	return nil
}
