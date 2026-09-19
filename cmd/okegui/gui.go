package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os/exec"
	"runtime"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
)

// guiCommand starts the daemon and opens the browser front end.
//
// PLAN.md §1 fixes the shape: the Web UI is served by the same process, so
// `gui` is `daemon` plus a browser launch. The service itself is identical —
// same engine, same HTTP front end, same signals — so the only thing this
// command owns is the browser.
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

	// The browser needs the port the service actually bound, so the listener is
	// opened here and handed to the daemon's lifecycle. `gui` never binds port
	// 0 in practice, but going through the same path keeps one code path for
	// both commands.
	if o.listener == nil {
		ln, err := net.Listen("tcp", o.addr)
		if err != nil {
			return fail(exitUsage, "无法监听 %s：%v", o.addr, err)
		}
		o.listener = ln
	}

	if openBrowser {
		go openWhenReady("http://"+o.listener.Addr().String()+"/", a.ctx.Done())
	} else {
		fmt.Fprintln(a.stdout, "浏览器启动: 已禁用（--open=false）")
	}

	return a.runService(&o)
}

// browserDelay is how long the launch waits before the first attempt: the
// server is already listening, so the wait only covers the handler being
// installed.
const browserDelay = 250 * time.Millisecond

// openWhenReady opens url in the default browser once the service is up.
//
// It runs in the background because the command must not block on a browser:
// a headless machine has none, and that must not stop the daemon. The failure
// is logged rather than returned for the same reason.
func openWhenReady(url string, stop <-chan struct{}) {
	select {
	case <-time.After(browserDelay):
	case <-stop:
		return
	}
	if err := openBrowser(url); err != nil {
		log.Warn("无法启动浏览器，请手动打开界面地址", "url", url, "err", err)
		return
	}
	log.Info("已在默认浏览器中打开界面", "url", url)
}

// openBrowser asks the operating system to open url.
func openBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		// rundll32 is the documented way to reach the shell's URL handler
		// without a cmd.exe in the middle.
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}

// printGUIUsage writes the help text of `okegui gui`.
func printGUIUsage(w io.Writer) {
	fmt.Fprint(w, `用法: okegui gui [选项]

启动常驻服务并打开默认浏览器。服务部分与 'okegui daemon' 完全相同：
同一套 REST 接口、WebSocket 进度流与内嵌界面。

选项:
  --open                 启动后打开默认浏览器（默认 true）
  --addr HOST:PORT       HTTP 接口监听地址（默认 127.0.0.1:8090，必须是回环地址）
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
