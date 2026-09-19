package platform

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// LogDirName is the subdirectory that holds log files, relative to the working
// directory. The legacy Initializer wrote to a literal "log" directory.
const LogDirName = "log"

// The legacy log name pattern is OKE_{yyyyMMdd-HHmm}_{pid:X}.log: the launch
// time in local time to the minute, then the process id as upper-case
// hexadecimal with no padding.
const (
	logNamePrefix = "OKE_"
	logNameExt    = ".log"
	logTimeLayout = "20060102-1504"
)

// LogFileName renders the legacy log file name for a timestamp and process
// id, for example OKE_20260919-1030_1F4.log for pid 500 at 10:30 on
// 19 September 2026. It is pure so callers and tests can compute a name
// without touching the clock.
func LogFileName(t time.Time, pid int) string {
	return logNamePrefix + t.Format(logTimeLayout) + "_" +
		strings.ToUpper(strconv.FormatInt(int64(pid), 16)) + logNameExt
}

// LogPath returns dir/LogFileName(t, pid) without creating anything.
func LogPath(dir string, t time.Time, pid int) string {
	return filepath.Join(dir, LogFileName(t, pid))
}

// OpenLog creates the log file for this process under dir and returns its
// path together with the open file. The file is opened for appending so that
// two processes starting in the same minute never truncate each other's log.
func OpenLog(dir string, t time.Time, pid int) (string, *os.File, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, okerr.Wrap(err, okerr.KindIO, "无法创建日志目录",
			"无法创建日志目录 %s: %v", dir, err)
	}
	path := LogPath(dir, t, pid)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return "", nil, okerr.Wrap(err, okerr.KindIO, "无法创建日志文件",
			"无法创建日志文件 %s: %v", path, err)
	}
	return path, f, nil
}

// SetupLogging opens this process's log file under dir, points the
// process-wide logger at it and applies level. It replaces
// Initializer.ConfigLogger, minus the DebuggerTarget: that target only
// mirrored output to an attached debugger, which has no Go counterpart.
//
// It returns the path of the file that was opened and the open file itself.
// The caller owns the file and should close it when the process shuts down,
// both to flush the last records and so that the file can be deleted on
// Windows. When the file cannot be opened the error is returned and the logger
// keeps writing to its previous destination, so logging still works from
// stderr.
func SetupLogging(dir, level string) (string, *os.File, error) {
	log.SetLevel(level)
	path, f, err := OpenLog(dir, time.Now(), os.Getpid())
	if err != nil {
		return "", nil, err
	}
	log.SetOutput(f)
	return path, f, nil
}

// The two age thresholds of the legacy cleanup rule: a log file is deleted
// when it is older than six calendar months, or older than seven days and
// smaller than one kilobyte (an empty or truncated run). Both comparisons are
// strict, matching the legacy DateTime and FileInfo.Length tests.
const (
	logSmallAge = 7 * 24 * time.Hour
	logSmallMax = 1024
)

// ClearOldLogs deletes old files from dir following Initializer.ClearOldLogs.
// now is passed in rather than read from the clock so the rule is testable.
//
// Two deliberate differences from the legacy code change which files are
// removed:
//
//   - Only names that follow the OKE_{time}_{pid}.log pattern are considered.
//     The legacy code deleted every file in the log directory, which could
//     remove something the operator put there.
//   - A file that cannot be deleted only produces a warning; the legacy code
//     let the exception propagate and abandoned the rest of the cleanup.
//
// A missing directory is not an error: the legacy code checked
// Directory.Exists first.
func ClearOldLogs(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return okerr.Wrap(err, okerr.KindIO, "无法读取日志目录",
			"无法读取日志目录 %s: %v", dir, err)
	}
	cutoff := monthsBefore(now, 6)
	for _, e := range entries {
		if e.IsDir() || !IsLegacyLogName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			// The file disappeared between ReadDir and Info; nothing to do.
			continue
		}
		if !logExpired(info, cutoff, now) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if err := os.Remove(path); err != nil {
			log.Warn("无法删除旧日志", "file", path, "err", err)
		}
	}
	return nil
}

// logExpired applies the legacy predicate:
//
//	LastWriteTime < Now.AddMonths(-6)
//	|| (LastWriteTime < Now.AddDays(-7) && Length < 1024)
func logExpired(info os.FileInfo, sixMonthsAgo, now time.Time) bool {
	modified := info.ModTime()
	if modified.Before(sixMonthsAgo) {
		return true
	}
	return modified.Before(now.Add(-logSmallAge)) && info.Size() < logSmallMax
}

// monthsBefore subtracts whole months from t, clamping the day to the end of
// the target month exactly like DateTime.AddMonths. Go's AddDate normalises
// instead (31 March minus six months becomes 1 October, not 30 September), so
// the clamped arithmetic is spelled out to keep the cleanup boundary identical
// to the legacy one.
func monthsBefore(t time.Time, months int) time.Time {
	year, month, day := t.Date()
	total := int(month) - 1 - months
	year += total / 12
	month = time.Month(total%12 + 1)
	if total%12 < 0 {
		year--
		month += 12
	}
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
}

// IsLegacyLogName reports whether base follows the OKE_<time>_<hex pid>.log
// pattern, so cleanup only considers files this program created.
func IsLegacyLogName(base string) bool {
	if !strings.HasPrefix(base, logNamePrefix) || !strings.HasSuffix(base, logNameExt) {
		return false
	}
	rest := base[len(logNamePrefix) : len(base)-len(logNameExt)]
	if len(rest) < len(logTimeLayout)+2 || rest[len(logTimeLayout)] != '_' {
		return false
	}
	if _, err := time.ParseInLocation(logTimeLayout, rest[:len(logTimeLayout)], time.Local); err != nil {
		return false
	}
	_, err := strconv.ParseUint(rest[len(logTimeLayout)+1:], 16, 32)
	return err == nil
}
