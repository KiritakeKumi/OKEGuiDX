package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
)

func TestLogFileName(t *testing.T) {
	t.Parallel()
	// Mirrors ConfigLogger: log\OKE_{yyyyMMdd-HHmm}_{pid:X}.log, with the
	// process id in upper-case hexadecimal and no padding.
	cases := []struct {
		name string
		t    time.Time
		pid  int
		want string
	}{
		{
			"typical",
			time.Date(2026, 9, 19, 10, 30, 0, 0, time.Local),
			500,
			"OKE_20260919-1030_1F4.log",
		},
		{
			"single digit pid",
			time.Date(2026, 1, 2, 3, 4, 0, 0, time.Local),
			9,
			"OKE_20260102-0304_9.log",
		},
		{
			"pid one",
			time.Date(2026, 12, 31, 23, 59, 0, 0, time.Local),
			1,
			"OKE_20261231-2359_1.log",
		},
		{
			"letters above nine",
			time.Date(2025, 6, 7, 8, 9, 0, 0, time.Local),
			255,
			"OKE_20250607-0809_FF.log",
		},
		{
			"four digit pid",
			time.Date(2024, 2, 29, 0, 0, 0, 0, time.Local),
			65535,
			"OKE_20240229-0000_FFFF.log",
		},
		{
			"seconds are dropped",
			time.Date(2026, 9, 19, 10, 30, 59, 999, time.Local),
			500,
			"OKE_20260919-1030_1F4.log",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := LogFileName(tc.t, tc.pid); got != tc.want {
				t.Errorf("LogFileName(%s, %d) = %q, want %q",
					tc.t.Format(time.RFC3339), tc.pid, got, tc.want)
			}
		})
	}
}

func TestLogPath(t *testing.T) {
	t.Parallel()
	ts := time.Date(2026, 9, 19, 10, 30, 0, 0, time.Local)
	got := LogPath("log", ts, 500)
	want := filepath.Join("log", "OKE_20260919-1030_1F4.log")
	if got != want {
		t.Errorf("LogPath() = %q, want %q", got, want)
	}
}

func TestOpenLogCreatesAndAppends(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ts := time.Date(2026, 9, 19, 10, 30, 0, 0, time.Local)

	path, f, err := OpenLog(dir, ts, 500)
	if err != nil {
		t.Fatalf("OpenLog() error = %v", err)
	}
	if _, err := f.WriteString("first\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Two processes starting in the same minute share a name; the second must
	// append rather than truncate.
	_, f2, err := OpenLog(dir, ts, 500)
	if err != nil {
		t.Fatalf("second OpenLog() error = %v", err)
	}
	if _, err := f2.WriteString("second\n"); err != nil {
		t.Fatal(err)
	}
	if err := f2.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "first\nsecond\n" {
		t.Errorf("log content = %q, want %q", raw, "first\nsecond\n")
	}
}

func TestOpenLogCreatesDirectory(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "nested", LogDirName)
	ts := time.Date(2026, 9, 19, 10, 30, 0, 0, time.Local)
	path, f, err := OpenLog(dir, ts, 42)
	if err != nil {
		t.Fatalf("OpenLog() error = %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("log file was not created: %v", err)
	}
}

func TestIsLegacyLogName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		want bool
	}{
		{"OKE_20260919-1030_1F4.log", true},
		{"OKE_20260102-0304_9.log", true},
		{"OKE_20240229-0000_FFFF.log", true},
		// ParseUint is case-insensitive, so a lower-case name is recognised
		// too. Nothing writes one, but accepting it costs nothing.
		{"OKE_20260919-1030_1f4.log", true},
		{"OKE_20260919-1030_1F4.txt", false},
		{"oke_20260919-1030_1F4.log", false},
		{"OKE_20260919-1030_1F4", false},
		{"OKE_2026091-1030_1F4.log", false},  // short date
		{"OKE_20261319-1030_1F4.log", false}, // month 13
		{"OKE_20260919-2560_1F4.log", false}, // hour 25
		{"OKE_20260919-1030_ZZZ.log", false}, // pid is not hex
		{"OKE_20260919-1030_.log", false},
		{"OKE_20260919-1030_1F4.log.bak", false},
		{"OKE_20260919-1030_1F4.log.old.log", false},
		{"OKE_20260919_1030_1F4.log", false}, // wrong separator
		{"OKEGuiConfig.json", false},
		{"random.txt", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsLegacyLogName(tc.name); got != tc.want {
				t.Errorf("IsLegacyLogName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// touch creates a log-named file with the given modification time and size.
func touch(t *testing.T, dir, name string, mod time.Time, size int) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestClearOldLogsBoundaries(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.Local)
	sixMonthsAgo := monthsBefore(now, 6) // 2026-03-19 12:00
	// AddDays(-7) and Add(-7*24h) are the same amount of absolute time, which
	// is what the code compares against; AddDate would differ across a DST
	// transition.
	sevenDaysAgo := now.Add(-logSmallAge)

	// The legacy predicate is
	//   mod < now-6mo || (mod < now-7d && size < 1024)
	// Both comparisons are strict, so the exact boundaries are kept unless
	// the other branch fires. The six-month boundary is only observable for
	// files of 1 KiB or more: a smaller file older than seven days is removed
	// by the second branch long before it reaches six months.
	cases := []struct {
		name string
		mod  time.Time
		size int
		gone bool
	}{
		// Older than six months: always deleted.
		{"older than six months, tiny", sixMonthsAgo.Add(-time.Second), 0, true},
		{"older than six months, large", sixMonthsAgo.Add(-24 * time.Hour), 4096, true},

		// Exactly six months: kept when large, deleted when small because the
		// seven-day branch fires.
		{"exactly six months old, 1 KiB", sixMonthsAgo, 1024, false},
		{"exactly six months old, empty", sixMonthsAgo, 0, true},

		// Just under six months: kept when large, still deleted when small.
		{"just under six months, 1 KiB", sixMonthsAgo.Add(time.Second), 1024, false},
		{"just under six months, empty", sixMonthsAgo.Add(time.Second), 0, true},

		// Older than seven days and smaller than 1 KiB: deleted.
		{"eight days old, empty", now.AddDate(0, 0, -8), 0, true},
		{"eight days old, one byte", now.AddDate(0, 0, -8), 1, true},
		{"eight days old, 1023 bytes", now.AddDate(0, 0, -8), 1023, true},

		// Exactly seven days: strict comparison, so kept.
		{"exactly seven days old, empty", sevenDaysAgo, 0, false},
		{"exactly seven days old, 1023 bytes", sevenDaysAgo, 1023, false},
		{"exactly seven days old, 1 KiB", sevenDaysAgo, 1024, false},

		// Exactly 1 KiB and older than seven days: not small enough, kept.
		{"eight days old, exactly 1 KiB", now.AddDate(0, 0, -8), 1024, false},
		{"eight days old, 2 KiB", now.AddDate(0, 0, -8), 2048, false},

		// Recent files are never touched, whatever their size.
		{"today, empty", now, 0, false},
		{"yesterday, empty", now.AddDate(0, 0, -1), 0, false},
		{"six days old, empty", now.AddDate(0, 0, -6), 0, false},
		{"one month old, 1 KiB", now.AddDate(0, -1, 0), 1024, false},
		{"one month old, empty", now.AddDate(0, -1, 0), 0, true},
		{"one month old, large", now.AddDate(0, -1, 0), 4096, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			// The file name is irrelevant to the rule as long as it matches
			// the pattern; the timestamp inside it is not what is compared.
			path := touch(t, dir, "OKE_20200101-0000_1.log", tc.mod, tc.size)
			if err := ClearOldLogs(dir, now); err != nil {
				t.Fatalf("ClearOldLogs() error = %v", err)
			}
			_, statErr := os.Stat(path)
			gone := os.IsNotExist(statErr)
			if gone != tc.gone {
				t.Errorf("file removed = %v, want %v (mod=%s size=%d)",
					gone, tc.gone, tc.mod.Format(time.RFC3339), tc.size)
			}
		})
	}
}

func TestClearOldLogsIgnoresForeignFiles(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.Local)
	dir := t.TempDir()
	old := now.AddDate(-2, 0, 0)
	keep := []string{
		"notes.txt",
		"OKEGuiConfig.json",
		"OKE_20200101-0000_1.log.bak",
		"OKE_broken.log",
	}
	for _, name := range keep {
		touch(t, dir, name, old, 0)
	}
	if err := ClearOldLogs(dir, now); err != nil {
		t.Fatalf("ClearOldLogs() error = %v", err)
	}
	for _, name := range keep {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
}

func TestClearOldLogsLeavesDirectoriesAlone(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.Local)
	dir := t.TempDir()
	sub := filepath.Join(dir, "OKE_20200101-0000_1.log")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ClearOldLogs(dir, now); err != nil {
		t.Fatalf("ClearOldLogs() error = %v", err)
	}
	if info, err := os.Stat(sub); err != nil || !info.IsDir() {
		t.Errorf("directory was removed: %v", err)
	}
}

func TestSetupLoggingWritesToLegacyName(t *testing.T) {
	// Not parallel: it redirects the process-wide logger and restores it.
	// Sequential tests run before the parallel ones in this file, so the
	// window in which other tests could log is closed.
	dir := t.TempDir()
	path, f, err := SetupLogging(dir, "TRACE")
	if err != nil {
		t.Fatalf("SetupLogging() error = %v", err)
	}
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		_ = f.Close()
	})

	if filepath.Dir(path) != dir {
		t.Errorf("log written to %q, want under %q", path, dir)
	}
	if !IsLegacyLogName(filepath.Base(path)) {
		t.Errorf("log name %q does not follow the legacy pattern", filepath.Base(path))
	}
	// The minute in the name must be the current local time.
	if want := LogFileName(time.Now(), os.Getpid()); filepath.Base(path) != want {
		t.Errorf("log name = %q, want %q", filepath.Base(path), want)
	}

	log.Info("setup logging test")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log file was not created: %v", err)
	}
	if !strings.Contains(string(raw), "setup logging test") {
		t.Errorf("log file does not contain the message: %q", raw)
	}
}

func TestSetupLoggingReportsUnwritableDirectory(t *testing.T) {
	// Not parallel: SetupLogging calls log.SetLevel, which is process-wide.
	// A path whose parent is a file can never become a directory.
	base := t.TempDir()
	blocker := filepath.Join(base, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SetupLogging(filepath.Join(blocker, "log"), "DEBUG"); err == nil {
		t.Fatal("SetupLogging() succeeded on an impossible path")
	}
}

func TestClearOldLogsMissingDirectoryIsNotAnError(t *testing.T) {
	t.Parallel()
	if err := ClearOldLogs(filepath.Join(t.TempDir(), "no-such-dir"), time.Now()); err != nil {
		t.Errorf("ClearOldLogs() on a missing directory error = %v, want nil", err)
	}
}

func TestMonthsBeforeClampsToEndOfMonth(t *testing.T) {
	t.Parallel()
	// DateTime.AddMonths clamps: 31 August minus six months is 28/29 February,
	// not 2 or 3 March.
	cases := []struct {
		name string
		from time.Time
		want time.Time
	}{
		{
			"31 august to february",
			time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 2, 28, 12, 0, 0, 0, time.UTC),
		},
		{
			"31 august to leap february",
			time.Date(2024, 8, 31, 12, 0, 0, 0, time.UTC),
			time.Date(2024, 2, 29, 12, 0, 0, 0, time.UTC),
		},
		{
			"31 march to september",
			time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC),
			time.Date(2025, 9, 30, 12, 0, 0, 0, time.UTC),
		},
		{
			"31 october to april",
			time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
			time.Date(2026, 4, 30, 12, 0, 0, 0, time.UTC),
		},
		{
			"15 may to november",
			time.Date(2026, 5, 15, 6, 30, 0, 0, time.UTC),
			time.Date(2025, 11, 15, 6, 30, 0, 0, time.UTC),
		},
		{
			"march first crosses the year boundary",
			time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := monthsBefore(tc.from, 6); !got.Equal(tc.want) {
				t.Errorf("monthsBefore(%s, 6) = %s, want %s",
					tc.from.Format(time.RFC3339), got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}
