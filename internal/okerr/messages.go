package okerr

import "fmt"

// Message templates carried over verbatim from the legacy Constants class. The
// wording is intentionally unchanged: operators are used to reading these
// strings, and the regression fixtures compare against them.
const (
	// MsgEac3to mirrors Constants.eac3toErrorMsg.
	MsgEac3to = "eac3to出错: 退出代码%d，请查看日志并手动运行eac3to获取更多信息。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgAudioNumMismatch mirrors Constants.audioNumMismatchMsg.
	MsgAudioNumMismatch = "当前的视频含有轨道数%d，与json中指定的数量%d必须+%d可选不符合。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgSubNumMismatch mirrors Constants.subNumMismatchMsg.
	MsgSubNumMismatch = "当前的视频含有字幕数%d，与json中指定的数量%d必须+%d可选不符合。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgFpsMismatch mirrors Constants.fpsMismatchMsg.
	MsgFpsMismatch = "输出FPS和指定FPS不一致。json里指定帧率为%s，vs输出帧率为%s。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgX264 mirrors Constants.x264ErrorMsg.
	MsgX264 = "x264出错: %s。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgX265 mirrors Constants.x265ErrorMsg.
	MsgX265 = "x265出错: %s。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgSVTAV1 mirrors Constants.svtav1ErrorMsg.
	MsgSVTAV1 = "svt-av1出错: %s。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgVpy mirrors Constants.vpyErrorMsg.
	MsgVpy = "vpy出错: %s。\n该文件%s将跳过处理。请转告技术总监复查。"
	// MsgMkvmerge mirrors Constants.mmgErrorMsg.
	MsgMkvmerge = "mkvmerge出错: %s。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgLSmash mirrors Constants.lsmashErrorMsg.
	MsgLSmash = "l-smash出错: %s。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgUnknown mirrors Constants.unknownErrorMsg.
	MsgUnknown = "未知错误。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgVSCrash mirrors Constants.vsCrashMsg.
	MsgVSCrash = "压制未能完成，预计是vs崩溃。该文件%s将跳过处理，半成品以HEVC形式保留在目录中。请转告技术总监复查。"
	// MsgX264Crash mirrors Constants.x264CrashMsg.
	MsgX264Crash = "压制未能完成，预计是x264崩溃。该文件%s将跳过处理，如果是MKV输出，半成品以_.mkv形式保留在目录中。请转告技术总监复查。"
	// MsgX265Crash mirrors Constants.x265CrashMsg.
	MsgX265Crash = "压制未能完成，预计是x265崩溃。该文件%s将跳过处理，半成品以HEVC形式保留在目录中。请转告技术总监复查。"
	// MsgSVTAV1Crash mirrors Constants.svtav1CrashMsg.
	MsgSVTAV1Crash = "压制未能完成，预计是svt-av1崩溃。该文件%s将跳过处理，半成品以HEVC形式保留在目录中。请转告技术总监复查。"
	// MsgQAAC mirrors Constants.qaacErrorMsg.
	MsgQAAC = "QAAC无法正常运行。请确保你安装了Apple Application Support 64bit"
	// MsgAudioFormatMismatch mirrors Constants.audioFormatMistachMsg.
	MsgAudioFormatMismatch = "无法将%s格式的音轨转为%s格式。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgRPC mirrors Constants.rpcErrorMsg.
	MsgRPC = "RPC出错: %s。\n请手动检查%s，并请转告技术总监复查。"
	// MsgReEncodeSlice mirrors Constants.reEncodeSliceErrorMsg.
	MsgReEncodeSlice = "切片不合法: %s，视频长度为%s。该文件%s将跳过处理。请转告技术总监复查。"
	// MsgReEncodeFrames mirrors Constants.reEncodeFramesErrorMsg.
	MsgReEncodeFrames = "帧数错误: 脚本输出帧数为%s，但旧版压制成品帧数为%s。该文件%s将跳过处理。请转告技术总监复查。"
)

// Render produces the operator-facing message for an error, using the legacy
// template that matches the error's summary. When the error carries no extra
// data the summary and detail are returned as-is.
//
// This is the Go equivalent of ExceptionParser.Parse plus the Constants
// templates, merged because the engine now has a single error type.
func Render(e *Error) string {
	if e == nil {
		return ""
	}
	file := e.File
	switch e.Summary {
	case ErrEac3to.Summary:
		return fmt.Sprintf(MsgEac3to, e.ExitCode, file)
	case ErrAudioNumMismatch.Summary, ErrSubNumMismatch.Summary:
		return fmt.Sprintf("%s。该文件%s将跳过处理。请转告技术总监复查。", e.Detail, file)
	case ErrFpsMismatch.Summary:
		return fmt.Sprintf("%s。该文件%s将跳过处理。请转告技术总监复查。", e.Detail, file)
	case ErrX264.Summary:
		return fmt.Sprintf(MsgX264, e.Output, file)
	case ErrX265.Summary:
		return fmt.Sprintf(MsgX265, e.Output, file)
	case ErrSVTAV1.Summary:
		return fmt.Sprintf(MsgSVTAV1, e.Output, file)
	case ErrVpy.Summary:
		return fmt.Sprintf(MsgVpy, e.Output, file)
	case ErrMkvmerge.Summary:
		return fmt.Sprintf(MsgMkvmerge, e.Output, file)
	case ErrLSmash.Summary:
		return fmt.Sprintf(MsgLSmash, e.Output, file)
	case ErrVSCrash.Summary:
		return fmt.Sprintf(MsgVSCrash, file)
	case ErrX264Crash.Summary:
		return fmt.Sprintf(MsgX264Crash, file)
	case ErrX265Crash.Summary:
		return fmt.Sprintf(MsgX265Crash, file)
	case ErrSVTAV1Crash.Summary:
		return fmt.Sprintf(MsgSVTAV1Crash, file)
	case ErrQAAC.Summary:
		return MsgQAAC
	case ErrRPC.Summary:
		return fmt.Sprintf(MsgRPC, e.Output, file)
	case ErrUnknown.Summary:
		return fmt.Sprintf(MsgUnknown, file)
	default:
		if e.Detail != "" {
			if file != "" {
				return file + " : " + e.Detail
			}
			return e.Detail
		}
		return e.Summary
	}
}
