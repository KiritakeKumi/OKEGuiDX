// Package wizard assembles one task per source file from a task profile. It is
// the port of the legacy new-task wizard's finishing step,
// WizardWindow.WizardFinish (Gui/WizardWindow.xaml.cs:202-385).
//
// The rest of the engine consumes a profile that already carries InputScript,
// WorkingPathPrefix and OutputPathPrefix (see engine.validateForRun). A profile
// file on disk normally does not: the wizard derived those three from the
// project directory and the source path, wrote a per-episode .vpy and only then
// handed the task to the queue. This package is that step, so a headless front
// end can replace the wizard end to end.
//
// The package owns the path arithmetic and the script text. It does not
// validate the profile, clean up earlier runs, detect chapters, load the
// per-episode config or queue anything.
//
// # Caller's remaining steps
//
// The legacy WizardFinish did more than derive paths. Everything below is the
// caller's job, in the order the legacy loop did it:
//
//  1. profile.Validate, which needs the VapourSynth installation, the toolchain
//     and the source files. The wizard ran the equivalent check before it
//     showed the finish button (AddTaskService.LoadJsonAsProfile).
//  2. EpisodeConfigPath plus profile.LoadEpisodeConfig for the per-source
//     config, if there is one. The wizard loaded `<input>.json|.yaml|.yml`,
//     set the profile's Config and IsReEncode from it, and refused a re-encode
//     whose container was not MKV.
//  3. engine.Cleaner, which removed the leftovers of an earlier run.
//  4. Chapter detection (chapter.Service.UpdateChapterStatus), which filled the
//     task's chapter status.
//  5. profile.ToModel plus the queue. The wizard handed a TaskDetail to the
//     task manager; Task.Profile here is what that TaskDetail's Taskfile was.
//
// Behaviour is a direct port, including the parts that look odd:
//
//   - The strip list (BDBOX/BDROM/BD/BDMV/STREAM/BD_VIDEO) is hardcoded in the
//     legacy code with a "FIXME: do not hardcode this" comment; see
//     StripComponents.
//   - The generated .vpy name appends the timestamp to the working prefix
//     instead of replacing an extension, so the file is called
//     "00000.m2ts-09200905.vpy" and not "00000.vpy".
//   - The output prefix is derived from the working prefix by rewriting a path
//     level that looks like ".<anything>_" into "output". The escaped drive
//     level ("D_") is what that rule actually catches in practice, which is how
//     the deliverable ends up in an "output" directory next to the profile.
//   - reducePath shortening keys on a CRC32 of the path prefix, and the last
//     path level keeps its own name only when it carries a volume number.
package wizard

import (
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// StripComponents are the path levels removed from a source path before it is
// mirrored under the project directory.
//
// The legacy code hardcodes this list:
//
//	const string stripCommonPathComponents = "BDBOX/BDROM/BD/BDMV/STREAM/BD_VIDEO"; // FIXME: do not hardcode this.
//
// It is reproduced verbatim (commit d6604d1 reverted the configurable version
// and hardcoded it "for now"), because changing it would move every existing
// working directory. A later release may turn it into a configuration field;
// this package deliberately does not.
var StripComponents = []string{"BDBOX", "BDROM", "BD", "BDMV", "STREAM", "BD_VIDEO"}

// volNumberPattern is the legacy `.*Vol[.\- ]?(?<vol>\d+).*`, case-insensitive.
// A last path level matching it keeps its own name instead of being hashed, so
// "Vol.1", "Vol 2" and "Vol-3" stay readable.
var volNumberPattern = regexp.MustCompile(`(?i).*Vol[.\- ]?(\d+).*`)

// timestamp renders a time the way the legacy `time.ToString("MMddHHmm")` did:
// month, day, hour and minute, each two digits, with no separators.
//
// It is spelled out rather than done with time.Format because Go's reference-time
// layout shares no tokens with .NET's custom format strings — a Go layout of
// "MMddHHmm" would be emitted literally.
func timestamp(t time.Time) string {
	return fmt.Sprintf("%02d%02d%02d%02d", int(t.Month()), t.Day(), t.Hour(), t.Minute())
}

// outputDirName is the level the legacy code substituted for a "._" (or "D_")
// level: the directory a deliverable ends up in, next to the profile.
const outputDirName = "output"

// reducePathMapName is the file the CRC32-to-prefix mapping is appended to.
const reducePathMapName = "ReducePathMap.log"

// Permissions for the two things this package creates. Directories are
// world-readable because the working tree is shared with the other tools; the
// generated script is private, like the engine's other generated files.
const (
	dirPerm    = 0o755
	scriptPerm = 0o600
)

// Options configures one assembly pass.
type Options struct {
	// ProjectFile is the path of the profile the tasks come from. Its directory
	// is the root of both the working tree and the output tree. When empty, the
	// profile's own ConfigFilePath is used, which profile.Load filled in.
	ProjectFile string
	// ReducePath enables the CRC32 shortening of long source paths. It mirrors
	// Initializer.Config.reducePath, whose default is true.
	ReducePath bool
	// Now is the timestamp used in the generated .vpy names. The zero value
	// means time.Now(). Set it to pin file names in a test or a preview.
	Now time.Time
}

// Task is one assembled queue entry: one source file, its generated script and
// the two path prefixes the pipeline needs.
type Task struct {
	// Name is the task's display name, mirroring
	// TaskDetail.TaskName (WizardWindow.xaml.cs:326):
	//
	//	string.IsNullOrEmpty(json.ProjectName) ? finfo.Name : json.ProjectName + "-" + finfo.Name
	//
	// The file name keeps its extension, so a demo episode is
	// "Demo - 1080p-00000.m2ts".
	Name string
	// InputFile is the source file the task encodes, resolved to an absolute
	// path like the wizard's file dialog produced.
	InputFile string
	// VpyFile is the generated per-episode script and the value the profile's
	// InputScript must carry for the pipeline to run it.
	VpyFile string
	// Script is the text of the generated script, ready to be written to
	// VpyFile.
	Script string
	// WorkingPathPrefix is the profile's WorkingPathPrefix: intermediate files
	// are named after it.
	WorkingPathPrefix string
	// OutputPathPrefix is the profile's OutputPathPrefix; the deliverable goes
	// to its directory.
	OutputPathPrefix string
	// Profile is a copy of the input profile with the three path fields above
	// filled in, ready for profile.ToModel. Only those fields are overridden:
	// the track and input slices are shared with the input profile, which is
	// never modified.
	Profile *profile.Profile
}

// ReducePathMap is one line of ReducePathMap.log: a hashed prefix and the
// original path it stands for.
type ReducePathMap struct {
	// CRC is the standard IEEE CRC-32 of the original prefix, the value
	// ToString("X8") rendered in the legacy log.
	CRC uint32
	// Prefix is the original path prefix. It is joined with backslashes on
	// every platform so that the log and the derived names do not depend on
	// where the task was assembled; see PathSuffix.
	Prefix string
}

// Result is the outcome of one assembly pass.
type Result struct {
	// Tasks holds one entry per profile input file, in profile order.
	Tasks []Task
	// ReducePathMap holds the prefixes that were shortened, keyed by CRC32. A
	// repeated CRC is recorded once, like the legacy Dictionary. Entries are in
	// first-encounter order, which is the order the legacy Dictionary
	// enumerated in practice (nothing was ever removed from it).
	ReducePathMap []ReducePathMap
	// MapFile is where ReducePathMap.log is kept: the "output" directory next
	// to the profile. It is set even when nothing was reduced and the file does
	// not exist yet.
	MapFile string
}

// Derive computes the assembly for every input file of the profile without
// writing anything. It reads the profile's .vpy script, because the script text
// is part of what a task needs.
//
// The profile is neither validated nor re-read: the caller is expected to have
// run profile.Validate already, because its checks need facts only the caller
// has (the VapourSynth installation, the toolchain, the source files). A
// profile with no input files yields no tasks and no error, which is the
// caller's cue to report "nothing to do".
func Derive(p *profile.Profile, opts Options) (*Result, error) {
	if p == nil {
		return nil, okerr.New(okerr.KindConfig, "配置为空", "无法为一个空的 profile 装配任务。")
	}
	projectDir, err := projectDirOf(p, opts)
	if err != nil {
		return nil, err
	}
	script, err := rewriteScript(p, projectDir)
	if err != nil {
		return nil, err
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	// The legacy code read DateTime.Now once per source file, so a batch that
	// straddled a minute boundary produced two stamps. One stamp per pass is
	// deterministic and matches every batch that does not straddle one.
	stamp := timestamp(now)

	res := &Result{
		Tasks:   make([]Task, 0, len(p.InputFiles)),
		MapFile: filepath.Join(projectDir, outputDirName, reducePathMapName),
	}
	seen := make(map[uint32]struct{}, len(p.InputFiles))

	for _, raw := range p.InputFiles {
		input := resolveInput(projectDir, raw)
		suffix, mapping, err := PathSuffix(input, opts.ReducePath)
		if err != nil {
			return nil, err
		}
		working := combineLikeDotNet(projectDir, suffix)
		output := outputPath(working)
		vpy, err := buildVpy(script, input)
		if err != nil {
			return nil, err
		}

		clone := *p
		clone.InputScript = vpyName(working, stamp)
		clone.WorkingPathPrefix = working
		clone.OutputPathPrefix = output

		res.Tasks = append(res.Tasks, Task{
			Name:              taskName(p.ProjectName, input),
			InputFile:         input,
			VpyFile:           clone.InputScript,
			Script:            vpy,
			WorkingPathPrefix: working,
			OutputPathPrefix:  output,
			Profile:           &clone,
		})

		if mapping.Prefix == "" {
			continue
		}
		if _, dup := seen[mapping.CRC]; dup {
			continue
		}
		seen[mapping.CRC] = struct{}{}
		res.ReducePathMap = append(res.ReducePathMap, mapping)
	}
	return res, nil
}

// Assemble is Derive plus the filesystem work the wizard did: the parent
// directory of every derived prefix is created, each generated script is
// written, and ReducePathMap.log is appended to when the pass shortened
// anything.
//
// Nothing is written until every task has been derived, so a profile whose
// second source cannot be derived leaves no half-built tree behind. The legacy
// loop wrote as it went and stopped in the middle; this ordering is the only
// deliberate difference.
func Assemble(p *profile.Profile, opts Options) (*Result, error) {
	res, err := Derive(p, opts)
	if err != nil {
		return nil, err
	}
	for _, t := range res.Tasks {
		// The legacy code created the parent of the prefix, not the prefix
		// itself: the prefix is a file name stem, so "<dir>/<stem>" names a file
		// and its parent is the directory that has to exist.
		if err := makeDir(filepath.Dir(t.WorkingPathPrefix)); err != nil {
			return nil, err
		}
		if err := makeDir(filepath.Dir(t.OutputPathPrefix)); err != nil {
			return nil, err
		}
		if err := os.WriteFile(t.VpyFile, []byte(t.Script), scriptPerm); err != nil {
			return nil, okerr.Wrap(err, okerr.KindIO, "无法生成vpy文件",
				"写入 %q 失败: %v", t.VpyFile, err)
		}
		log.Debug("装配任务", "input", t.InputFile,
			"working", t.WorkingPathPrefix, "output", t.OutputPathPrefix, "vpy", t.VpyFile)
	}
	if len(res.ReducePathMap) > 0 {
		if err := appendReducePathMap(res.MapFile, res.ReducePathMap); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// PathSuffix mirrors the legacy inputSuffixPath computation: the source path
// with its drive letter escaped, the common BD levels stripped, and the
// remaining levels either kept whole or shortened with a CRC32 of the prefix.
//
// The result is relative to the project directory unless the source itself is
// rooted outside it (see combineLikeDotNet) and uses the OS separator. The
// second result carries the reducePath mapping when one was recorded.
//
// The CRC is computed over the prefix joined with backslashes, and the mapping
// records that same form. The legacy code ran on Windows only, where that was
// the separator anyway; keeping it fixed makes the derived directory names
// identical on every platform, which matters as soon as a cluster mixes nodes.
func PathSuffix(inputFile string, reducePath bool) (string, ReducePathMap, error) {
	if strings.TrimSpace(inputFile) == "" {
		return "", ReducePathMap{}, okerr.New(okerr.KindConfig, "输入文件不合法",
			"输入文件路径为空，无法推导工作目录。")
	}
	suffix := stripDriveColon(filepath.Clean(normalizeSeparators(inputFile)))
	for _, comp := range StripComponents {
		suffix = stripComponent(suffix, comp)
	}

	components := splitComponents(suffix)
	if len(components) <= 3 || !reducePath {
		return suffix, ReducePathMap{}, nil
	}

	// components[0] is the drive, the last is the file name, and everything
	// between them is the effective path the legacy code reduced.
	last := components[len(components)-2]
	file := components[len(components)-1]

	if volNumberPattern.MatchString(last) {
		// A volume number already makes the level unique, so it is kept as the
		// effective path. The middle levels are dropped either way: the legacy
		// code rebuilt the path from [drive, effectivePath, file] in both
		// branches, and only the effective path differed.
		return strings.Join([]string{components[0], last, file}, string(filepath.Separator)),
			ReducePathMap{}, nil
	}

	prefix := strings.Join(components[1:len(components)-2], `\`)
	sum := crc32.ChecksumIEEE([]byte(prefix))
	shortened := upperHex8(sum) + "-" + last
	reduced := strings.Join([]string{components[0], shortened, file}, string(filepath.Separator))
	return reduced, ReducePathMap{CRC: sum, Prefix: prefix}, nil
}

// EpisodeConfigPath returns the per-episode configuration that sits next to a
// source file, if there is one.
//
// The wizard looked for `<input>.json`, then `.yaml`, then `.yml`, and loaded
// the first that existed (WizardWindow.xaml.cs:250-266). Loading is the
// caller's job — profile.LoadEpisodeConfig reads and validates it — because
// this package does not duplicate profile parsing.
//
// inputFile is expected to be the resolved source path, which is what Task
// carries.
func EpisodeConfigPath(inputFile string) (string, bool) {
	if inputFile == "" {
		return "", false
	}
	for _, suffix := range []string{".json", ".yaml", ".yml"} {
		path := inputFile + suffix
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path, true
		}
	}
	return "", false
}

// taskName is the display name TaskDetail.TaskName got: the project name and
// the source file name joined with a dash, or just the file name when the
// profile has no project name. The file keeps its extension, and the source is
// used as given, not as a FileInfo.Name (they are the same for a resolved
// absolute path).
func taskName(projectName, inputFile string) string {
	base := filepath.Base(normalizeSeparators(inputFile))
	if projectName == "" {
		return base
	}
	return projectName + "-" + base
}

// projectDirOf resolves the directory that is the root of the working tree.
func projectDirOf(p *profile.Profile, opts Options) (string, error) {
	projectFile := opts.ProjectFile
	if projectFile == "" {
		projectFile = p.ConfigFilePath
	}
	if projectFile == "" {
		return "", okerr.New(okerr.KindConfig, "找不到配置文件",
			"装配任务需要一个 profile 文件路径，它的目录是工作目录与输出目录的根。")
	}
	dir, err := filepath.Abs(filepath.Dir(projectFile))
	if err != nil {
		return "", okerr.Wrap(err, okerr.KindConfig, "配置文件路径不合法",
			"%q: %v", projectFile, err)
	}
	return dir, nil
}

// resolveInput turns a profile InputFiles entry into the absolute path the
// wizard worked with.
//
// AddTaskService.LoadInputFiles resolved every entry against the profile's
// directory and normalised it with FileInfo.FullName; the wizard then used that
// absolute path for the generated script and for the derived directories. An
// entry that is already absolute is returned unchanged, like
// PathUtils.GetFullPath did.
func resolveInput(dir, raw string) string {
	path := normalizeSeparators(raw)
	if isRooted(path) {
		return path
	}
	return filepath.Join(dir, path)
}

// combineLikeDotNet mirrors Path.Combine: a second argument that is rooted wins
// over the first. Go's filepath.Join would instead append it, which would move
// a source tree that already lives outside the project directory underneath it.
//
// "Rooted" means the platform's own notion, plus a Windows drive prefix, which
// .NET's Path.IsPathRooted also treats as rooted. Keeping the drive case means a
// profile authored on Windows derives the same relative directory name on every
// platform; the resulting "D_/..." suffix is still relative off Windows, so it
// lands under the project directory either way.
func combineLikeDotNet(dir, suffix string) string {
	if isRooted(suffix) {
		return suffix
	}
	return filepath.Join(dir, suffix)
}

// isRooted reports whether path is absolute for the current platform, or starts
// with a Windows drive.
func isRooted(path string) bool {
	return filepath.IsAbs(path) || hasDrivePrefix(path)
}

// hasDrivePrefix reports whether path starts with a Windows drive, "D:" or "d:".
// It deliberately does not require a separator after the colon, because
// "D:relative" is a rooted path on Windows too.
func hasDrivePrefix(path string) bool {
	if len(path) < 2 || path[1] != ':' {
		return false
	}
	c := path[0]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// stripDriveColon escapes a Windows drive colon the way the legacy
// `inputFile.Replace(':', '_')` did.
//
// The legacy call replaced every colon, which only ever mattered for a drive
// letter: a colon is not a legal name character on Windows, and the paths it
// ran on were absolute Windows paths (the wizard normalised every dropped file
// with FileInfo.FullName). On a platform where a colon is an ordinary name
// character, and on a UNC path, which has no drive, replacing every colon would
// rename a legitimate level, so only a drive-shaped prefix is escaped. The
// resulting "D_" level is what the legacy code produced for the same path.
func stripDriveColon(path string) string {
	if !hasDrivePrefix(path) {
		return path
	}
	return string(path[0]) + "_" + path[2:]
}

// stripComponent removes one path level, mirroring the legacy
// `Regex.Replace(inputSuffixPath, @"[/\\]" + comp + @"[/\\]", "\\")`.
//
// The .NET engine scanned matches left to right without overlapping, so a
// removal consumes the separator that follows the level and the next search
// resumes after it. Two consecutive levels are therefore not both removed:
// "a\BD\BD\b" loses one, while "a\BD\BD\BD\b" loses two, because the third
// separator becomes available again. The scan here reproduces that exactly.
//
// The match was case-sensitive, so a tree that spells a level differently
// ("bdmv") keeps it. That is faithful; the strip list is written the way real
// disc trees spell those levels.
func stripComponent(path, comp string) string {
	sep := string(filepath.Separator)
	needle := sep + comp + sep
	var b strings.Builder
	for i := 0; i < len(path); {
		j := strings.Index(path[i:], needle)
		if j < 0 {
			b.WriteString(path[i:])
			break
		}
		b.WriteString(path[i : i+j])
		b.WriteString(sep)
		i += j + len(needle)
	}
	return b.String()
}

// outputPath mirrors the legacy
// `Regex.Replace(newPath, @"[/\\]._[/\\]", "\\output\\")`.
//
// In that pattern "." is a wildcard and "_" is literal, so it rewrites every
// level whose name is exactly one character followed by an underscore — which
// is what the escaped drive level ("D_") is. That is how the drive level turns
// into the "output" directory. Only a level surrounded by separators is
// rewritten, and, as in stripComponent, the scan resumes after each match, so
// "._\._\x" becomes "output\._\x" while "._\._\._\x" becomes
// "output\._\output\x".
func outputPath(newPath string) string {
	sep := string(filepath.Separator)
	replacement := sep + outputDirName + sep
	var b strings.Builder
	for i := 0; i < len(newPath); {
		j := underscoreLevel(newPath, i)
		if j < 0 {
			b.WriteString(newPath[i:])
			break
		}
		b.WriteString(newPath[i:j])
		b.WriteString(replacement)
		i = j + 4
	}
	return b.String()
}

// underscoreLevel returns the index of the leftmost match of .NET's
// `[/\\]._[/\\]` at or after from, or -1. The match is four bytes long, so the
// caller resumes after it.
func underscoreLevel(path string, from int) int {
	sep := byte(filepath.Separator)
	for i := from; i+4 <= len(path); i++ {
		if path[i] == sep && path[i+2] == '_' && path[i+3] == sep {
			return i
		}
	}
	return -1
}

// splitComponents splits a path into its non-empty levels on either separator.
// It mirrors the legacy `Split(new[] {'/', '\\'}, RemoveEmptyEntries)`.
func splitComponents(path string) []string {
	return strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
}

// normalizeSeparators rewrites both separators to the OS separator. .NET
// treated both as separators, and a profile is usually authored on Windows, so
// a source entry may use either one.
func normalizeSeparators(path string) string {
	sep := string(filepath.Separator)
	if sep == `\` {
		return strings.ReplaceAll(path, "/", `\`)
	}
	return strings.ReplaceAll(path, `\`, "/")
}

// makeDir creates a directory and reports the failure with its path.
func makeDir(dir string) error {
	if dir == "" {
		return okerr.New(okerr.KindIO, "无法创建工作目录", "无法确定要创建的目录。")
	}
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法创建工作目录", "创建 %q 失败: %v", dir, err)
	}
	return nil
}

// appendReducePathMap appends the mapping to ReducePathMap.log.
//
// The legacy code wrote it with File.AppendAllText, which created the file but
// not its directory; it created the directory here as well, because a profile
// whose sources are all relative never produces an "output" level and the
// legacy append would have thrown DirectoryNotFoundException there.
func appendReducePathMap(path string, entries []ReducePathMap) error {
	if err := makeDir(filepath.Dir(path)); err != nil {
		return err
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(upperHex8(e.CRC))
		b.WriteByte(' ')
		b.WriteString(e.Prefix)
		// The legacy code used Environment.NewLine, i.e. CRLF on Windows. A log
		// written on every platform uses LF here.
		b.WriteByte('\n')
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, scriptPerm)
	if err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入路径映射文件",
			"打开 %q 失败: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(b.String()); err != nil {
		return okerr.Wrap(err, okerr.KindIO, "无法写入路径映射文件",
			"写入 %q 失败: %v", path, err)
	}
	return nil
}

// upperHex8 renders a 32-bit value as the eight uppercase hex digits
// ToString("X8") produced.
func upperHex8(v uint32) string {
	const digits = "0123456789ABCDEF"
	var buf [8]byte
	for i := len(buf) - 1; i >= 0; i-- {
		buf[i] = digits[v&0xF]
		v >>= 4
	}
	return string(buf[:])
}
