# OKEGuiDX

OKEGuiDX 将 OKEGui 的任务引擎移植为 Go，提供命令行、常驻服务和内嵌 Web UI。当前主分支使用 Go；旧 .NET/WPF 代码保留为行为参考，冻结版本见 legacy/net472。

## 当前进度（2026-10-04）

[最新 Go CI](https://github.com/KiritakeKumi/OKEGuiDX/actions/runs/36912486741) 在 75bbfa9 上通过：Go 测试（含 race 检查）、lint、五目标编译、libtchapter 三架构构建与真实章节工具测试。

目标平台为 Windows x64/arm64 和 Linux x64/arm64/riscv64。已有 riscv64 实机 Go 测试与编码器测速；完整成品回归、旧版黄金基准对拍和 Go 版自动发布仍未完成。当前从源码构建，旧版 Release 的 OKEGui.exe / NSIS 安装器不代表 Go 版交付。

## 构建与运行

需要 Go 1.25 或更高版本。在仓库根目录运行：

~~~sh
go build -o okegui ./cmd/okegui
./okegui --help
./okegui gui
~~~

Windows 使用 go build -o okegui.exe ./cmd/okegui，随后运行 .\okegui.exe gui。gui 启动服务并打开默认浏览器，默认地址为 http://127.0.0.1:8090/。

其他入口：

~~~sh
./okegui daemon
./okegui status
./okegui run --dry-run profile.json
./okegui run profile.json
~~~

各命令参数见 okegui <命令> --help。当前仅支持 standalone；coordinator/worker 网络调度尚未实现。

## 外部工具

主程序无需 .NET；编码、抽流、封装依赖外部工具。构建与版本见 [OKEGuiDX-tools](https://github.com/KiritakeKumi/OKEGuiDX-tools)。五目标工具构建与自检尚未全部通过，主程序交叉编译成功不能等同完整工具链可用。

将工具按 tools/x26x、tools/ffmpeg、tools/mkvtoolnix 等目录放在主程序旁边。--tools 参数接收包含 tools/ 的父目录：

~~~sh
./okegui status --tools /path/to/install
~~~

VapourSynth/vspipe、Python 与用户脚本插件单独准备，tools 构建不包含它们。libtchapter 默认以独立 tchapter CLI 调用；C 编译环境下可运行 make -C native/tchapter cli，将产物按 tools/tchapter/tchapter（Windows 加 .exe）布局安装。

Windows x64 的 x265 优先 Kyouko，其次 Asuna、upstream；其他平台使用 upstream（当前锁定 4.2）。不同变体不能直接以 md5 判断跨平台一致性。AAC 编码依赖 Windows qaac；非 Windows 会明确拒绝 AAC 编码。

## 代码结构

- cmd/okegui：CLI、daemon、浏览器界面入口。
- internal/engine：队列、Worker 与任务管线。
- internal/jobproc：编码、音频、抽流与封装工具包装。
- internal/api：REST /api/v1 与 WebSocket。
- web：嵌入主程序的 Web UI。
- native/tchapter：C 章节库与 CLI。
- OKEGui、TChapter、dist/windows：旧版代码与打包参考。

Task 是单个源到成品的完整任务；Job 是抽流、压制或封装阶段；Worker 决定同时执行的任务数。

## 旧版资料

原 OKEGui 的说明见 [Wiki](https://github.com/vcb-s/OKEGui/wiki) 和 [VCB-Studio 教程](https://guides.vcb-s.com/basic-guide-11)。这些资料描述旧版工作流；当前入口与参数以命令帮助为准。
