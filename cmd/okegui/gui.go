package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// guiCommand starts the daemon and opens the browser front end.
//
// PLAN.md §1 fixes the shape: the Web UI is served by the same process, so
// `gui` is `daemon` plus a browser launch. The address is not reachable until
// E2 wires the HTTP server in, so the command prints where the UI will live and
// says plainly that it is not up yet, rather than opening a browser on a dead
// port.
func (a *application) guiCommand(args []string) error {
	fs := flag.NewFlagSet("gui", flag.ContinueOnError)
	var o daemonOptions
	o.bind(fs)
	o.bindDaemon(fs)
	var openBrowser bool
	fs.BoolVar(&openBrowser, "open", true, "启动后打开默认浏览器")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, errHelp) {
			printGUIUsage(a.stdout)
		}
		return err
	}
	if fs.NArg() > 0 {
		return fail(exitUsage, "gui 不接受位置参数：%v", fs.Args())
	}

	fmt.Fprintf(a.stdout, "界面地址（E2 落地后可用）: http://%s/\n", o.addr)
	if openBrowser {
		fmt.Fprintln(a.stdout, "浏览器启动: 等待 E2 提供 HTTP 服务后启用")
	} else {
		fmt.Fprintln(a.stdout, "浏览器启动: 已禁用（--open=false）")
	}

	// gui shares the daemon's lifecycle from here on: same service, same
	// signals, same graceful shutdown. Only the browser launch differs, and
	// that lands with E2.
	return a.runService(&o)
}

// printGUIUsage writes the help text of `okegui gui`.
func printGUIUsage(w io.Writer) {
	fmt.Fprint(w, `用法: okegui gui [选项]

启动常驻服务并打开默认浏览器。服务部分与 'okegui daemon' 完全相同。

界面尚未接入：E2 提供 HTTP 服务后，本命令才会真正拉起浏览器。

选项:
  --open                 启动后打开默认浏览器（默认 true）
  --addr HOST:PORT       HTTP 接口监听地址（默认 127.0.0.1:8090）
  --shutdown-grace DUR   优雅关闭等待在跑任务的最长时间（默认 30s）
  --pid-file PATH        写入进程号的路径
  --queue PATH           任务队列文件，默认为配置目录下的 queue.json
  --tools DIR            tools 目录所在位置，默认为可执行文件所在目录
  --config PATH          OKEGuiConfig.json 的路径
  --role NAME            节点角色，只接受 standalone
  --log-level LV         日志级别: TRACE / DEBUG / INFO / WARN / ERROR
  --log-dir DIR          日志文件目录，留空则写入配置目录下的 log/

退出码:
  0   正常关闭
  1   内部错误
  3   参数或配置错误
  130 被中断
`)
}
