package mkvepisode

import "strings"

// The legacy pipeline built the deliverable's path with System.IO.Path and
// FileInfo, then split it with Path.GetDirectoryName. Those methods have
// precise, slightly surprising behaviour around roots and trailing separators,
// so they are ported here rather than approximated with filepath.Dir: a profile
// written on Windows must resolve to the same path no matter which platform the
// engine runs on.
//
// One deliberate difference: .NET on Windows rewrites "/" to "\" while
// normalising. The port keeps whichever separator the input used, because the
// engine also runs on Linux and a Windows-style separator would be a literal
// character there.

func isSep(c byte) bool { return c == '/' || c == '\\' }

// baseName returns FileInfo.Name for a path: everything after the last
// separator. A path that ends in a separator has no name and yields "".
func baseName(path string) string {
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// dirName returns Path.GetDirectoryName(path), or "" for the cases where .NET
// returns null (an empty path, a bare root, or a path that is all separator).
//
// The shape of the algorithm is .NET's GetDirectoryNameOffset: find the last
// separator, then drop any separators that precede it. The root length matters
// because "D:\" is a root with nothing above it, while "D:\a" has "D:\".
func dirName(path string) string {
	if path == "" {
		return ""
	}
	root := rootLength(path)
	end := len(path)
	if end <= root {
		return ""
	}
	// Walk back to the last separator.
	for end > root {
		end--
		if isSep(path[end]) {
			break
		}
	}
	// Drop any further separators, so "D:\a\\b" yields "D:\a".
	for end > root && isSep(path[end-1]) {
		end--
	}
	return path[:end]
}

// rootLength returns PathInternal.GetRootLength for a non-device path:
//
//	"\\server\share\..." -> through the share name
//	"\..." or "/..."     -> 1
//	"D:\..." or "D:..."  -> 3, the drive, the colon and one separator slot
//	anything else        -> 0
//
// The drive form is 3 rather than 2 so that "D:\" is itself a root and has no
// directory name above it.
func rootLength(path string) int {
	if path == "" {
		return 0
	}
	if isSep(path[0]) {
		if len(path) > 1 && isSep(path[1]) {
			// UNC: skip the server name, then the share name.
			i := 2
			for i < len(path) && !isSep(path[i]) {
				i++
			}
			i++ // step over the separator before the share
			for i < len(path) && !isSep(path[i]) {
				i++
			}
			return i
		}
		return 1
	}
	if len(path) > 1 && path[1] == ':' {
		return 3
	}
	return 0
}

// joinPath reproduces Path.Combine for two elements: an empty element is
// ignored, a rooted second element replaces the first, and otherwise the two
// are joined with the separator the first element already uses.
func joinPath(dir, name string) string {
	switch {
	case name == "":
		return dir
	case dir == "":
		return name
	case isRooted(name):
		return name
	}
	if isSep(dir[len(dir)-1]) {
		return dir + name
	}
	if strings.ContainsRune(dir, '\\') {
		return dir + `\` + name
	}
	return dir + "/" + name
}

// isRooted mirrors Path.IsPathRooted for the paths this package handles.
func isRooted(path string) bool {
	if path == "" {
		return false
	}
	if isSep(path[0]) {
		return true
	}
	return len(path) >= 2 && path[1] == ':'
}
