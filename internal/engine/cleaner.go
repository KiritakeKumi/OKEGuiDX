package engine

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/wildcard"
)

// DefaultRemoveSuffixes are the file suffixes Clean deletes. Taken verbatim
// from the Cleaner() constructor in Utils/Cleaner.cs.
var DefaultRemoveSuffixes = []string{
	"flac", "alac", "aac", "ac3", "dts", "eac3", "sup",
	"Log.txt", "eac3to.log", "vpy", "qpf", "lwi", "rpc", "pyc",
}

// DefaultRenameSuffixes are the suffixes Rename backs up. Taken verbatim from
// the Cleaner() constructor in Utils/Cleaner.cs.
var DefaultRenameSuffixes = []string{"hevc", "mkv", "mp4", "mka", "h264"}

// backupTimeLayout is TIME_FMT in Cleaner.cs. Go's reference time reads the
// same fields in the same order as .NET's "ddHHmm".
const backupTimeLayout = "021504"

// Cleaner deletes the intermediate files a previous run left next to a source
// file and can back up the finished ones. Ported from Utils/Cleaner.cs.
//
// The search pattern is a Win32 one, so matching goes through
// internal/wildcard rather than filepath.Match; see that package for why the
// two disagree.
type Cleaner struct {
	// RemoveSuffixes are the suffixes Remove matches.
	RemoveSuffixes []string
	// RenameSuffixes are the suffixes Rename matches.
	RenameSuffixes []string
}

// NewCleaner returns a Cleaner with the legacy default suffix lists.
func NewCleaner() *Cleaner {
	return NewCleanerWith(DefaultRemoveSuffixes, DefaultRenameSuffixes)
}

// NewCleanerWith returns a Cleaner with explicit suffix lists. Both slices are
// copied so the caller keeps ownership.
func NewCleanerWith(remove, rename []string) *Cleaner {
	return &Cleaner{
		RemoveSuffixes: append([]string(nil), remove...),
		RenameSuffixes: append([]string(nil), rename...),
	}
}

// Clean deletes the intermediate files of inputFile, after making sure the
// input itself can never be deleted. Mirrors Cleaner.Clean.
//
// The legacy code has the Rename call commented out (Cleaner.cs:34-35), so
// Clean only removes. Rename is implemented and tested but deliberately not
// called from here.
func (c *Cleaner) Clean(inputFile string, whiteList []string) ([]string, error) {
	// Copy: the legacy code appended to the caller's List<string> in place,
	// which is a side effect nobody wants to inherit.
	protected := append([]string(nil), whiteList...)
	if !containsString(protected, inputFile) {
		protected = append(protected, inputFile)
	}
	return c.Remove(inputFile, protected)
}

// Remove deletes every file below the input's directory whose name matches
// `<stem>*.*` and ends with one of the remove suffixes, skipping the white
// list. It returns the deleted paths, in the order they were removed.
//
// The search is recursive, exactly like Directory.GetFiles(..., AllDirectories)
// in Cleaner.Remove. That is the legacy behaviour and it is a real hazard: a
// file in a subdirectory of the working directory is deleted too, including
// one in an output directory that happens to sit below it. The white list is
// the only protection the caller has.
//
// The first deletion failure stops the walk; the files removed before it are
// still returned.
func (c *Cleaner) Remove(inputFile string, whiteList []string) ([]string, error) {
	matches, err := c.collect(inputFile, c.RemoveSuffixes, whiteList, true)
	if err != nil {
		return nil, err
	}
	removed := make([]string, 0, len(matches))
	for _, file := range matches {
		if err := os.Remove(file); err != nil {
			return removed, okerr.Wrap(err, okerr.KindIO, "无法删除中间文件",
				"%s: %v", file, err).WithFile(file)
		}
		removed = append(removed, file)
	}
	return removed, nil
}

// Rename backs up the finished files of inputFile by inserting a `_b_<time>`
// marker before the extension. Mirrors Cleaner.Rename.
//
// Unlike Remove this only looks at the input's own directory, again matching
// the legacy SearchOption.TopDirectoryOnly.
//
// One deliberate difference: .NET's File.Move fails when the destination
// exists, while Go's os.Rename replaces it. The destination is therefore
// checked first and an existing one takes the legacy failure path (log, then
// delete the source). That path is destructive by design in the original: the
// file could not be backed up, and the pipeline is about to write a new file
// under that name.
func (c *Cleaner) Rename(inputFile string, whiteList []string) ([]string, error) {
	return c.renameAt(inputFile, whiteList, time.Now())
}

// renameAt is Rename with an injected clock, so the timestamp in the new name
// is testable.
func (c *Cleaner) renameAt(inputFile string, whiteList []string, now time.Time) ([]string, error) {
	matches, err := c.collect(inputFile, c.RenameSuffixes, whiteList, false)
	if err != nil {
		return nil, err
	}
	stamp := now.Format(backupTimeLayout)
	renamed := make([]string, 0, len(matches))
	for _, oldFile := range matches {
		// The legacy loop recomputes both parts from the matched file, not
		// from the input, so a match like `ep01.v2.mkv` keeps `ep01.v2`.
		dir := filepath.Dir(oldFile)
		stem := trimExt(filepath.Base(oldFile))
		ext := filepath.Ext(oldFile)
		newFile := filepath.Join(dir, stem+"_b_"+stamp+ext)

		if _, statErr := os.Lstat(newFile); statErr == nil {
			// File.Move would have thrown here; the legacy handler logs and
			// deletes instead.
			log.Error("无法备份文件，直接删除", "file", oldFile, "reason", "目标已存在")
			if err := os.Remove(oldFile); err != nil {
				return renamed, removeError(oldFile, err)
			}
			continue
		}
		if err := os.Rename(oldFile, newFile); err != nil {
			log.Error("无法备份文件，直接删除", "file", oldFile, "err", err)
			if delErr := os.Remove(oldFile); delErr != nil {
				return renamed, removeError(oldFile, delErr)
			}
			continue
		}
		renamed = append(renamed, newFile)
	}
	return renamed, nil
}

// collect walks the input's directory and returns the files that match the
// legacy pattern, are not white-listed and end with one of the suffixes.
func (c *Cleaner) collect(inputFile string, suffixes, whiteList []string, recursive bool) ([]string, error) {
	dir := filepath.Dir(inputFile)
	pattern := trimExt(filepath.Base(inputFile)) + "*.*"

	var matches []string
	walkErr := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if !recursive && path != dir {
				// TopDirectoryOnly: do not descend into subdirectories.
				return fs.SkipDir
			}
			return nil
		}
		if !wildcard.Match(entry.Name(), pattern) {
			return nil
		}
		if containsString(whiteList, path) {
			return nil
		}
		if !hasAnySuffix(entry.Name(), suffixes) {
			return nil
		}
		matches = append(matches, path)
		return nil
	})
	if walkErr != nil {
		return nil, searchError(dir, walkErr)
	}
	return matches, nil
}

func searchError(dir string, err error) error {
	if os.IsNotExist(err) {
		return okerr.Wrap(err, okerr.KindNotFound, "找不到目录", "%s 不存在", dir).WithFile(dir)
	}
	return okerr.Wrap(err, okerr.KindIO, "无法枚举目录", "%s: %v", dir, err).WithFile(dir)
}

func removeError(file string, err error) error {
	return okerr.Wrap(err, okerr.KindIO, "无法删除中间文件", "%s: %v", file, err).WithFile(file)
}

// hasAnySuffix reports whether the file name ends with one of the suffixes.
//
// The comparison is the one string.EndsWith does in the legacy code: ordinal
// and case-sensitive. `ep01.FLAC` is therefore kept while `ep01.flac` is
// deleted, and `ep01_flac` is deleted because the check is a plain suffix
// test, not an extension comparison.
func hasAnySuffix(name string, suffixes []string) bool {
	for _, sfx := range suffixes {
		if strings.HasSuffix(name, sfx) {
			return true
		}
	}
	return false
}

// trimExt returns the file name without its directory and extension, mirroring
// Path.GetFileNameWithoutExtension.
func trimExt(path string) string {
	base := filepath.Base(path)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
