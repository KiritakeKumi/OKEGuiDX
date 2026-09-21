package engine

import (
	"encoding/json"

	"github.com/KiritakeKumi/OKEGuiDX/internal/log"
	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// loadProfile is the first stage: recover the profile and the episode config for
// the task being run.
//
// It is a stage rather than a constructor step because the worker pool hands the
// pipeline a task it read from the queue, and model.Task deliberately carries the
// profile as an opaque `any`: the queue only knows a path, which lives in
// TaskManager. PipelineOptions.LoadProfile is that mapping, and the caller closes
// over its own queue to supply it.
//
// When the task still holds the typed values profile.ToModel put there (the
// in-process case), they are used directly and the loader is not called at all.
func (p *Pipeline) loadProfile(t *model.Task, rep *reporter) (*runState, error) {
	rep.step(statusFetchInfo, -1)

	prof, cfg, err := p.profileFor(t)
	if err != nil {
		return nil, err
	}
	if prof == nil {
		return nil, okerr.New(okerr.KindConfig, "找不到配置文件", "任务 %s 没有可用的配置", t.ID)
	}

	// The wizard was the only place that ever set IsReEncode, and it did so from
	// the episode config's EnableReEncode. The two are reconciled here so that a
	// task recovered from the queue behaves like one a wizard produced. The
	// result stays in the run state: the profile belongs to the queue, which
	// shares it with every snapshot it hands out.
	isReEncode := prof.IsReEncode || (cfg != nil && cfg.EnableReEncode)
	if isReEncode && cfg == nil {
		return nil, okerr.New(okerr.KindConfig, "参数不完整",
			"ReEncode 任务 %s 没有关联的 episode 配置（ReEncodeOldFile 与 ReEncodeSliceArray）", t.ID)
	}

	// Re-check what the pipeline depends on. The queue may hold a task whose
	// profile was edited afterwards, and a re-encode is the case where a wrong
	// field produces a wrong release rather than a failed run. The returned
	// slices are a private copy, already sorted and merged, so the run works on
	// them instead of on the queue's array.
	slices, err := validateForRun(prof, cfg)
	if err != nil {
		return nil, err
	}

	st := &runState{
		opts:           &p.opts,
		t:              t,
		p:              prof,
		cfg:            cfg,
		isReEncode:     isReEncode,
		reEncodeSlices: slices,
		media:          model.NewMediaFile(),
		mka:            model.NewMediaFile(),
	}
	t.IsReEncode = isReEncode
	log.Info("-------------------------------------------------------------------")
	log.Info("开始处理任务", "input", st.inputPath())
	return st, nil
}

// profileFor resolves the typed profile for a task, preferring what the task
// already carries.
func (p *Pipeline) profileFor(t *model.Task) (*profile.Profile, *profile.EpisodeConfig, error) {
	if t == nil {
		return nil, nil, okerr.New(okerr.KindConfig, "任务为空", "无法为 nil 任务加载配置")
	}
	if prof, ok := t.Profile.(*profile.Profile); ok && prof != nil {
		if cfg, ok := t.Config.(*profile.EpisodeConfig); ok {
			return prof, cfg, nil
		}
		return prof, nil, nil
	}
	// A task read back from queue.json holds these as generic JSON values. The
	// queue's copy is the assembled profile, so it is the one to use; see
	// reviveStored. The caller's loader is the fallback, for a task that
	// carries nothing at all.
	if prof, cfg, ok := reviveStored(t); ok {
		return prof, cfg, nil
	}
	return p.opts.LoadProfile(t)
}

// reviveStored decodes the profile and episode config a queued task carries.
//
// model.Task holds both as `any` because the model package cannot import
// profile (profile imports model). A task that never left this process still
// holds the typed values, which profileFor uses directly. A task read back from
// queue.json holds them as map[string]any instead, and re-decoding is the only
// way to use them.
//
// Using them is the point: the queue stores the *assembled* profile, which
// carries the generated InputScript and the two derived path prefixes, while the
// profile file on disk does not, because the wizard derived those and never
// wrote them back. Re-reading the file instead - which is what happened before
// this existed - fails validateForRun with "工作目录没有指定", so a task that
// survived a restart could never run again.
func reviveStored(t *model.Task) (*profile.Profile, *profile.EpisodeConfig, bool) {
	if t == nil || t.Profile == nil {
		return nil, nil, false
	}
	raw, err := json.Marshal(t.Profile)
	if err != nil {
		return nil, nil, false
	}
	prof := &profile.Profile{}
	// UnmarshalJSON is the tolerant decoder the profile format needs, and it
	// rejects a payload that is not a profile at all.
	if err := prof.UnmarshalJSON(raw); err != nil {
		return nil, nil, false
	}
	var cfg *profile.EpisodeConfig
	if t.Config != nil {
		rawCfg, err := json.Marshal(t.Config)
		if err != nil {
			return nil, nil, false
		}
		c := &profile.EpisodeConfig{}
		if err := json.Unmarshal(rawCfg, c); err != nil {
			return nil, nil, false
		}
		cfg = c
	}
	return prof, cfg, true
}

// validateForRun re-applies the checks the pipeline relies on. It is deliberately
// narrower than profile.Validate: that function needs the VapourSynth
// installation and the source files, which cannot be re-checked on every run,
// while these checks are pure profile arithmetic.
//
// It returns the re-encode slices to work on: a copy of the episode config's
// array, normalized the way profile.ValidateEpisodeConfig leaves it. That
// function sorts and merges its argument in place, so handing it the config's
// own array would write to a value the queue shares with its snapshots.
func validateForRun(p *profile.Profile, cfg *profile.EpisodeConfig) ([]model.SliceInfo, error) {
	if p.InputScript == "" {
		return nil, okerr.New(okerr.KindConfig, "vpy文件找不到",
			"配置 %s 没有指定 InputScript", p.ConfigFilePath)
	}
	if p.WorkingPathPrefix == "" {
		return nil, okerr.New(okerr.KindConfig, "工作目录没有指定",
			"配置 %s 没有工作路径前缀，无法为任务生成中间文件", p.ConfigFilePath)
	}
	if p.OutputPathPrefix == "" {
		return nil, okerr.New(okerr.KindConfig, "输出目录没有指定",
			"配置 %s 没有输出路径前缀，无法确定成品位置", p.ConfigFilePath)
	}
	switch profile.EncoderType(p.EncoderType) {
	case profile.EncoderX264, profile.EncoderX265, profile.EncoderSVTAV1:
	default:
		return nil, okerr.New(okerr.KindConfig, "编码器版本错误",
			"EncoderType 请填写 x264/x265/svtav1（当前 %q）", p.EncoderType)
	}
	if p.FpsNum <= 0 || p.FpsDen <= 0 {
		return nil, okerr.New(okerr.KindConfig, "帧率没有指定诶",
			"配置 %s 的 FpsNum/FpsDen 不合法（%d/%d）", p.ConfigFilePath, p.FpsNum, p.FpsDen)
	}
	if cfg == nil {
		return nil, nil
	}

	slices := append([]model.SliceInfo(nil), cfg.ReEncodeSliceArray...)
	clone := *cfg
	clone.ReEncodeSliceArray = slices
	if err := profile.ValidateEpisodeConfig(&clone); err != nil {
		return nil, err
	}
	// The validator replaces the array with the merged one, so the normalized
	// value is the clone's, not the copy handed to it.
	return clone.ReEncodeSliceArray, nil
}
