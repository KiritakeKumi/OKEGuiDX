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
	if root == "" {
		return rel
	}
	return filepath.Join(root, rel)
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
func (r *FileRef) UnmarshalText(b []byte) error {
	s := string(b)
	volume, rel, found := strings.Cut(s, "/")
	if !found {
		// A bare path with no volume prefix.
		*r = NewFileRef(s)
		return nil
	}
	if volume == "" || strings.Contains(volume, ":") {
		// Not a "volume/rel" form after all; treat the whole value as a path.
		*r = NewFileRef(s)
		return nil
	}
	*r = FileRef{Volume: volume, Rel: "/" + strings.TrimPrefix(rel, "/")}
	return nil
}

func normalizeRel(path string) string {
	p := strings.ReplaceAll(path, "\\", "/")
	// Collapse any Windows volume prefix ("C:/foo") to "/foo" so that the
	// local volume root can be a drive letter without duplicating it.
	if len(p) >= 2 && p[1] == ':' {
		p = p[2:]
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
