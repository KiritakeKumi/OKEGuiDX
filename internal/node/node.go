// Package node describes what a machine is able to do.
//
// The description is a structured value rather than scattered
// `if runtime.GOOS == ...` checks because two existing decisions depend on it
// (PLAN.md §2.2 and §2.0):
//
//   - AAC output is refused on platforms without qaac.
//   - x265 resolves to the Asuna build on win-x64 and to upstream elsewhere.
//
// The same value becomes the scheduler's input once cluster support arrives
// (CLUSTER.md §2, reservation 4), which is why it lives in its own package.
package node

import (
	"os"
	"runtime"
	"sort"
)

// Role selects what a running instance does. Only standalone is implemented in
// this rewrite; the other two are reserved so that the surrounding code does not
// have to change later (CLUSTER.md §0).
type Role string

// Roles.
const (
	RoleStandalone  Role = "standalone"
	RoleCoordinator Role = "coordinator"
	RoleWorker      Role = "worker"
)

// ParseRole converts a command-line value to a Role, defaulting to standalone.
func ParseRole(s string) Role {
	switch Role(s) {
	case RoleCoordinator:
		return RoleCoordinator
	case RoleWorker:
		return RoleWorker
	default:
		return RoleStandalone
	}
}

// Feature names used in Capabilities.Features. They are plain strings so that a
// future node can advertise a feature this build has never heard of.
const (
	// FeatureAAC means the platform can produce AAC via qaac.
	FeatureAAC = "aac"
	// FeatureEac3to means the eac3to demuxer is available.
	FeatureEac3to = "eac3to"
	// FeatureNUMA means per-socket process pinning is available.
	FeatureNUMA = "numa"
	// FeatureFdkAAC is never advertised: the project deliberately does not use
	// libfdk-aac (PLAN.md §2.2). It exists so that a misconfigured node is
	// detectable rather than silently accepted.
	FeatureFdkAAC = "fdk-aac"
)

// ToolInfo describes one external tool as found on this machine.
type ToolInfo struct {
	// Path is the absolute path to the executable.
	Path string `json:"path"`
	// Version is the tool's reported version, when it could be determined.
	Version string `json:"version"`
	// Variant distinguishes builds of the same tool, such as "asuna" versus
	// "upstream" for x265 (PLAN.md §2.0).
	Variant string `json:"variant"`
}

// Capabilities is the machine-readable description of a node.
type Capabilities struct {
	// NodeID is stable for a given installation; standalone uses the host name.
	NodeID string `json:"node_id"`
	// Role is what this instance is currently doing.
	Role Role `json:"role"`
	// OS and Arch are the Go runtime identifiers, e.g. "windows"/"amd64".
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// Tools maps a logical tool name ("x265", "ffmpeg", ...) to its details.
	Tools map[string]ToolInfo `json:"tools"`
	// Features lists the optional capabilities that are available here.
	Features []string `json:"features"`
	// NUMANodes is the number of NUMA nodes, 1 when unknown or when pinning is
	// disabled by configuration.
	NUMANodes int `json:"numa_nodes"`
	// CPUs is the number of usable logical processors.
	CPUs int `json:"cpus"`
	// Volumes maps a logical volume id to its local root path. Standalone has
	// exactly one entry, model.LocalVolume (CLUSTER.md §2, reservation 2).
	Volumes map[string]string `json:"volumes"`
}

// NewCapabilities returns a Capabilities describing the current machine, with
// tools and features still to be filled in by the toolchain.
func NewCapabilities(role Role) Capabilities {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return Capabilities{
		NodeID:    host,
		Role:      role,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		Tools:     make(map[string]ToolInfo),
		Features:  []string{},
		NUMANodes: 1,
		CPUs:      runtime.NumCPU(),
		Volumes:   make(map[string]string),
	}
}

// HasFeature reports whether the feature is advertised.
func (c Capabilities) HasFeature(feature string) bool {
	for _, f := range c.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// AddFeature advertises a feature, ignoring duplicates.
func (c *Capabilities) AddFeature(feature string) {
	if c.HasFeature(feature) {
		return
	}
	c.Features = append(c.Features, feature)
	sort.Strings(c.Features)
}

// Tool returns the tool's details and whether it is present.
func (c Capabilities) Tool(name string) (ToolInfo, bool) {
	t, ok := c.Tools[name]
	return t, ok
}

// HasTool reports whether a tool was found.
func (c Capabilities) HasTool(name string) bool {
	_, ok := c.Tools[name]
	return ok
}

// IsWindows reports whether this node runs Windows. Used only where behaviour
// genuinely differs, such as the AAC decision; prefer Features elsewhere.
func (c Capabilities) IsWindows() bool { return c.OS == "windows" }
