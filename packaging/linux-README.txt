OKEGuiDX Linux 候选测试包

使用 tar -xzf 解压整个包，保留文件执行权限和 tools/ 结构。
运行 ./start-okeguidx.sh 启动浏览器界面；无桌面的机器可运行
./start-okeguidx.sh daemon，默认监听 http://127.0.0.1:8090/。
检查工具：./start-okeguidx.sh status --json

启动脚本使用包内 test-data/OKEGuiDX/ 保存配置、队列和日志，
不读取或覆盖当前用户以前保存的任务。关闭后再次启动会恢复本测试包自己的队列。
直接运行 ./okegui 会使用通常的用户配置目录。

包内包含 Go 主程序、内嵌 Web UI、静态 tchapter CLI 和开源工具。
VapourSynth/vspipe、Python、脚本插件需单独准备；非 Windows 不支持 AAC 编码。
完整任务引擎及旧版真实成品对拍尚未完成。

build-info.json 记录提交、工具来源和版本；SHA256SUMS 校验包内文件。
sources/tools-build/ 保留工具构建脚本和补丁。完整上游源码归档与正式发行
所需许可证交付仍未完成。本包作为候选测试附件，不创建正式 Release。
