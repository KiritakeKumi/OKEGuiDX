OKEGuiDX Windows x64 候选测试包

1. 解压整个文件夹，保留 tools/ 结构。
2. 双击 Start-OKEGuiDX.cmd，默认浏览器将打开任务界面。
3. 命令行检查：.\Start-OKEGuiDX.cmd status --json

包内包含 Go 主程序（Web UI 已内嵌）、tchapter CLI 和开源编码/抽流/封装工具。
无需 .NET。候选包用于测试，尚未完成旧版真实素材成品对拍。

开始真实任务前还需准备：
- VapourSynth、vspipe、Python 及脚本所需插件。可把完整的可用环境放在
  tools/vapoursynth/ 下，使 vspipe.exe 位于该目录；也可在配置中指定 VSPipePath。
- AAC 编码需另备 qaac 及其运行依赖。选择 eac3to 抽流时也需另备对应工具。
status 中提示缺少 vspipe 是外部环境尚未配置，不表示候选包下载损坏。

tools/ 保持在主程序旁边即可，--tools 参数接收包含 tools/ 的父目录。
启动脚本使用包内 test-data/OKEGuiDX/ 保存配置、队列和日志，不读取或覆盖旧用户记录。
关闭后会恢复本测试包自己的队列。直接运行 okegui.exe 仍使用通常的用户配置目录。

build-info.json 记录主程序提交、工具提交、构建运行和锁定的上游版本。
SHA256SUMS 记录包内文件校验值。sources/tools-build/ 保留工具构建脚本和补丁。
本候选包不包含完整上游源码归档；正式发行的源码和许可证交付仍需完成。
