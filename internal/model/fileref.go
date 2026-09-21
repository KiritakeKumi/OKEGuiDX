// Package model holds the pure data structures shared by every other package.
//
// Nothing in this package may touch the filesystem, spawn processes or depend
// on a GUI toolkit. Structures here are JSON-serializable because the task
// queue is persisted to disk (crash recovery) and, later, shipped between
// cluster nodes.
package model

import (
	"path/filepath"
	"strings"
)

// LocalVolume is the implicit volume used in standalone (single-machine) mode.
// Resolving a FileRef on this volume yields the plain filesystem path the user
// expects.
const LocalVolume = "local"

// FileRef is a logical reference to a file: a volume identifier plus a path
// relative to that volume's root.
//
// Rationale (see CLUSTER.md §2): absolute paths break as soon as several
// machines mount the same share at different mount points. Everything internal
// uses FileRef; only the point where a real path is needed calls Resolve.
//
// In standalone mode there is exactly one volume, LocalVolume, whose root is
// the filesystem root. Resolve then degrades to the absolute path, so users
// never see the difference.
//
// JSON form: a FileRef serializes as the single string "volume/path" (see
// MarshalText), which keeps API payloads and the persisted queue compact and
// readable.
type FileRef struct {
	// Volume is the logical volume id. Empty means LocalVolume.
	Volume string `json:"-"`
	// Rel is the path relative to the volume root, using forward slashes.
	Rel string `json:"-"`
}

// NewFileRef builds a FileRef on the local volume from an OS path.
func NewFileRef(path string) FileRef {
	return FileRef{Volume: LocalVolume, Rel: normalizeRel(path)}
}

// IsZero reports whether the reference is empty.
func (r FileRef) IsZero() bool { return r.Rel == "" }

// String returns a stable, human-readable identity such as "local/dir/file.mkv".
func (r FileRef) String() string {
	v := r.Volume
	if v == "" {
		v = LocalVolume
	}
	return v + "/" + strings.TrimPrefix(strings.ReplaceAll(r.Rel, "\\", "/"), "/")
}

// Base returns the final element of the reference (the file name).
func (r FileRef) Base() string {
	return filepath.Base(strings.ReplaceAll(r.Rel, "/", string(filepath.Separator)))
}

// Dir returns the parent directory of the reference, as a FileRef.
func (r FileRef) Dir() FileRef {
	rel := strings.ReplaceAll(r.Rel, "\\", "/")
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		return FileRef{Volume: r.Volume, Rel: rel[:i]}
	}
	return FileRef{Volume: r.Volume, Rel: ""}
}

// Ext returns the file extension, including the leading dot, lowercased.
func (r FileRef) Ext() string {
	return strings.ToLower(filepath.Ext(strings.ReplaceAll(r.Rel, "/", string(filepath.Separator))))
}

// WithExt returns a copy whose extension is replaced by ext (which must include
// the dot).
func (r FileRef) WithExt(ext string) FileRef {
	base := r.Base()
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	dir := r.Dir()
	rel := stem + ext
	if dir.Rel != "" {
		rel = dir.Rel + "/" + rel
	}
	return FileRef{Volume: r.Volume, Rel: rel}
}

// Join returns a copy with name appended to the reference path.
func (r FileRef) Join(name string) FileRef {
	rel := strings.TrimSuffix(r.Rel, "/")
	if rel == "" {
		return FileRef{Volume: r.Volume, Rel: name}
	}
	return FileRef{Volume: r.Volume, Rel: rel + "/" + name}
}

// Resolve turns the logical reference into a local filesystem path for the
// given volume roots. Unknown volumes fall back to the local root so that a
// standalone run of a cluster-authored profile still works.
func (r FileRef) Resolve(roots map[string]string) string {
	v := r.Volume
	if v == "" {
		v = LocalVolume
	}
	root, ok := roots[v]
	if !ok {
		root = roots[LocalVolume]
	}
	rel := strings.ReplaceAll(r.Rel, "/", string(filepath.Separator))
	// A drive-qualified Rel is already the whole path: the drive is per path,
	// not per volume, so no root can be prefixed without duplicating it.
	if driveQualified(rel) {
		return strings.TrimPrefix(rel, string(filepath.Separator))
	}
	if root == "" {
		return rel
	}
	return filepath.Join(root, rel)
}

// driveQualified reports whether an OS path starts with a Windows drive letter,
// such as `D:\a\b`. The leading separator is skipped because normalizeRel keeps
// one in front of the drive.
func driveQualified(path string) bool {
	p := strings.TrimPrefix(path, string(filepath.Separator))
	return len(p) >= 2 && p[1] == ':'
}

// ResolveLocal resolves the reference against a single local root. Convenience
// wrapper for the standalone case.
func (r FileRef) ResolveLocal(root string) string {
	return r.Resolve(map[string]string{LocalVolume: root})
}

// MarshalText keeps FileRef readable when used as a map key or in log output.
func (r FileRef) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText accepts the textual form produced by MarshalText, so a FileRef
// can be stored either as a structured object or as a single string.
//
// Ambiguity: MarshalText writes "volume/rel" with no separator between the
// volume id and the path, so "mnt/media/ep01.mkv" is equally readable as volume
// "mnt" plus "/media/ep01.mkv" or as the single local path "mnt/media/ep01.mkv".
// Guessing a volume from any leading segment invents a volume identity that a
// bare relative path never had, which is wrong for the cluster design in
// CLUSTER.md §2 (a path must not claim a volume it does not belong to).
//
// Rule: the prefix before the first "/" is a volume id only when it is exactly
// LocalVolume ("local"). Everything else — a bare relative path, an absolute
// POSIX path, a drive-qualified path, a UNC path — is fed through NewFileRef as
// a plain path and lands on the local volume.
//
// A Windows drive prefix is deliberately NOT a volume: "D:/a/b" is the local
// path D:\a\b, not volume "D:", because a drive belongs to each path and not to
// a volume (see Resolve and normalizeRel). Treating it as a volume would drop
// the drive, the very regression TestFileRefResolveKeepsDriveLetter guards.
// The empty prefix ("/mnt/...", from an absolute POSIX path) is a path by the
// same rule.
//
// Consequently the text form cannot name an arbitrary cluster volume such as
// "nas/..."; that is a deliberate boundary until cluster volumes are
// implemented, at which point a wire form that distinguishes a volume from a
// path (for example a structured object, or a reserved volume-id syntax) is
// needed.
func (r *FileRef) UnmarshalText(b []byte) error {
	s := string(b)
	prefix, rel, found := strings.Cut(s, "/")
	if !found || !isVolumePrefix(prefix) {
		// A plain path with no volume prefix (or not one we recognize).
		*r = NewFileRef(s)
		return nil
	}
	*r = FileRef{Volume: prefix, Rel: "/" + strings.TrimPrefix(rel, "/")}
	return nil
}

// isVolumePrefix reports whether the segment before the first "/" in a textual
// FileRef is a volume id rather than the first segment of a path. Only the
// implicit local volume qualifies; see UnmarshalText for why an arbitrary name
// (and a drive prefix) must not.
func isVolumePrefix(prefix string) bool {
	return prefix == LocalVolume
}

func normalizeRel(path string) string {
	p := strings.ReplaceAll(path, "\\", "/")
	// Keep a Windows drive letter instead of collapsing it. The local volume's
	// root is empty on Windows, because the drive belongs to each path rather
	// than to the volume, so Rel has to carry it for Resolve to rebuild an
	// absolute path. Cluster volumes, whose root is a real mount point, do not
	// reach this branch: their paths are rooted and carry no drive.
	if len(p) >= 2 && p[1] == ':' {
		p = "/" + p
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
