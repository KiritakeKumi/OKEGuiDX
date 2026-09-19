package engine

// This file holds the helpers the stages share: driving a processor, resolving
// a profile, and the path and unit conversions the legacy code inlined.

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/KiritakeKumi/OKEGuiDX/internal/jobproc"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// canceled converts a context error into the shape the worker pool recognises.
func canceled(err error, t *model.Task) error {
	if errors.Is(err, context.Canceled) {
		return okerr.Wrap(err, okerr.KindCanceled, "任务已取消", "任务 %s 已被终止", t.ID)
	}
	return okerr.Wrap(err, okerr.KindCanceled, "任务超时", "任务 %s 超过了时限", t.ID)
}

// missingProfile is the LoadProfile a pipeline gets when the caller supplied
// none and the task carries no typed profile. Reporting it beats running with a
// zero profile, which would fail much later and much less clearly.
func missingProfile(t *model.Task) (*profile.Profile, *profile.EpisodeConfig, error) {
	return nil, nil, okerr.New(okerr.KindConfig, "找不到配置文件",
		"任务 %s 没有可用的配置：请设置 PipelineOptions.LoadProfile，或让任务携带已解析的 Profile", t.ID)
}

// LoadProfileFromDisk reads a profile and its episode config from a config path.
//
// It is the body a caller's PipelineOptions.LoadProfile usually wants: the queue
// owns the path (TaskManager.ConfigPath), so the caller closes over its queue and
// delegates the reading here. A profile that names its episode config inline uses
// it; otherwise there is none, because the standalone `<input>.json` path the
// wizard resolved is not recoverable from a queued task.
func LoadProfileFromDisk(configPath string) (*profile.Profile, *profile.EpisodeConfig, error) {
	if configPath == "" {
		return nil, nil, okerr.New(okerr.KindConfig, "找不到配置文件", "任务没有关联的 json 文件")
	}
	p, err := profile.Load(configPath)
	if err != nil {
		return nil, nil, err
	}
	if p.Config != nil {
		return p, p.Config, nil
	}
	return p, nil, nil
}

// runProcessor drives one jobproc.Processor and closes it. Every stage goes
// through here so that Close is never forgotten and a cancellation is always
// reported in the same shape.
func runProcessor(ctx context.Context, p jobproc.Processor, sink jobproc.ProgressSink) error {
	if err := ctx.Err(); err != nil {
		return okerr.Wrap(err, okerr.KindCanceled, "任务已取消", "%s 已被终止", p.Name())
	}
	err := p.Run(ctx, sink)
	if closeErr := p.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return okerr.Wrap(ctx.Err(), okerr.KindCanceled, "任务已取消", "%s 已被终止", p.Name())
	}
	return err
}

// fileSizeOf is the volume-aware size function MediaFile.TotalFileSize takes. It
// returns a file's size in bytes, or 0 when the file does not exist yet.
func (st *runState) fileSizeOf(ref model.FileRef) int64 {
	info, err := os.Stat(ref.Resolve(st.opts.Caps.Volumes))
	if err != nil {
		return 0
	}
	return info.Size()
}

// replaceExt swaps a path's extension, mirroring Path.ChangeExtension. The
// extension must include the dot. Like .NET's method, a dot that sits before the
// last separator is not an extension.
func replaceExt(path, ext string) string {
	e := filepath.Ext(path)
	if e == "" {
		return path + ext
	}
	return strings.TrimSuffix(path, e) + ext
}

// ensureFileExists reports whether path is a readable regular file.
func ensureFileExists(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return okerr.New(okerr.KindNotFound, "找不到文件", "%s 不存在", path).WithFile(path)
		}
		return okerr.Wrap(err, okerr.KindIO, "无法读取文件", "%s: %v", path, err).WithFile(path)
	}
	if st.IsDir() {
		return okerr.New(okerr.KindNotFound, "找不到文件", "%s 是一个目录", path).WithFile(path)
	}
	return nil
}

// numaFrom returns the NUMA node the worker pool assigned to this task, or 0
// when the task is not running under the pool (a test or a direct call).
func numaFrom(ctx context.Context) int {
	if node, ok := NUMANodeFrom(ctx); ok {
		return node
	}
	return 0
}
