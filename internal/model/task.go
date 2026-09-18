package model

// Task is a unit of work: one source file set turned into one output file. It is
// the granularity the future cluster dispatcher works with (CLUSTER.md §3), so
// the whole structure must stay JSON-serializable and free of handles.
type Task struct {
	ID     TaskID     `json:"id"`
	Name   string     `json:"name"`
	Status TaskStatus `json:"status"`
	Inputs []FileRef  `json:"inputs"`
	Output FileRef    `json:"output"`

	// Profile is the parsed task profile. It is an opaque value to the model
	// package; keeping it as any avoids a dependency cycle with internal/profile.
	Profile any `json:"profile,omitempty"`
	// Config is the per-episode configuration (re-encode slices, vspipe args).
	Config any `json:"config,omitempty"`

	AudioTracks    []AudioInfo `json:"audio_tracks"`
	SubtitleTracks []Info      `json:"subtitle_tracks"`

	// ChapterFile is the chapter source selected for this task, when any.
	ChapterFile FileRef `json:"chapter_file"`
	// ChapterLanguage is the language tag written into the chapter track.
	ChapterLanguage string `json:"chapter_language"`

	// WorkingDir and OutputDir are the resolved directories for this task.
	WorkingDir FileRef `json:"working_dir"`
	OutputDir  FileRef `json:"output_dir"`

	// LengthMS is the source duration, Frames the source frame count.
	LengthMS int64 `json:"length_ms"`
	Frames   int64 `json:"frames"`

	// IsReEncode marks re-encode tasks; SliceParts holds their per-slice output.
	IsReEncode bool        `json:"is_reencode"`
	SliceParts []SliceInfo `json:"slice_parts"`

	// CreatedAt is a Unix timestamp so the queue file is stable across runs.
	CreatedAt int64 `json:"created_at"`
}

// StatusEvent is one progress report emitted while a task runs. It is the
// payload of the WebSocket progress stream and, later, of the cluster status
// channel, so it must remain small and serializable.
type StatusEvent struct {
	TaskID   TaskID       `json:"task_id"`
	Progress TaskProgress `json:"progress"`
	// Step names the job currently executing, e.g. "x265".
	Step string `json:"step"`
	// Percent is the current job's progress in percent; negative means unknown.
	Percent float64 `json:"percent"`
	// Speed is a preformatted human string such as "12.34 fps".
	Speed string `json:"speed"`
	// BitRate is a preformatted human string such as "1234.56 kb/s".
	BitRate string `json:"bit_rate"`
	// TimeRemainSeconds is the estimated remaining time for the current job.
	TimeRemainSeconds float64 `json:"time_remain_seconds"`
	// FramesDone and FramesTotal are set by video encoders.
	FramesDone  int64 `json:"frames_done"`
	FramesTotal int64 `json:"frames_total"`
	// Error carries a structured error summary when Progress is TaskError.
	Error *ErrorInfo `json:"error,omitempty"`
}

// ErrorInfo is the serializable form of a structured error, used in events and
// API responses.
type ErrorInfo struct {
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
	File    string `json:"file,omitempty"`
}
