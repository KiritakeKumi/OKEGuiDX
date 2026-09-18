package model

// TaskProgress is the coarse lifecycle state of a task.
type TaskProgress int

// Task lifecycle states. The numeric values are persisted, so they must not be
// reordered.
const (
	TaskWaiting TaskProgress = iota
	TaskRunning
	TaskError
	TaskFinished
)

// String implements fmt.Stringer.
func (p TaskProgress) String() string {
	switch p {
	case TaskRunning:
		return "RUNNING"
	case TaskError:
		return "ERROR"
	case TaskFinished:
		return "FINISHED"
	default:
		return "WAITING"
	}
}

// MarshalText implements encoding.TextMarshaler.
func (p TaskProgress) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (p *TaskProgress) UnmarshalText(b []byte) error {
	switch string(b) {
	case "RUNNING":
		*p = TaskRunning
	case "ERROR":
		*p = TaskError
	case "FINISHED":
		*p = TaskFinished
	default:
		*p = TaskWaiting
	}
	return nil
}

// TaskType distinguishes normal tasks from re-encode tasks.
type TaskType int

// Task types.
const (
	TaskTypeNormal TaskType = iota
	TaskTypeReEncode
)

// String implements fmt.Stringer.
func (t TaskType) String() string {
	if t == TaskTypeReEncode {
		return "ReEncode"
	}
	return "Normal"
}

// ChapterStatus tracks what the chapter service found for a task. The names are
// persisted in the task queue.
type ChapterStatus int

// Chapter statuses. Values are persisted; do not reorder.
const (
	ChapterNo ChapterStatus = iota
	ChapterYes
	ChapterAdded
	ChapterMaybe
	ChapterMKV
	ChapterWarn
)

// String implements fmt.Stringer.
func (c ChapterStatus) String() string {
	switch c {
	case ChapterYes:
		return "Yes"
	case ChapterAdded:
		return "Added"
	case ChapterMaybe:
		return "Maybe"
	case ChapterMKV:
		return "MKV"
	case ChapterWarn:
		return "Warn"
	default:
		return "No"
	}
}

// MarshalText implements encoding.TextMarshaler.
func (c ChapterStatus) MarshalText() ([]byte, error) { return []byte(c.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (c *ChapterStatus) UnmarshalText(b []byte) error {
	switch string(b) {
	case "Yes":
		*c = ChapterYes
	case "Added":
		*c = ChapterAdded
	case "Maybe":
		*c = ChapterMaybe
	case "MKV":
		*c = ChapterMKV
	case "Warn":
		*c = ChapterWarn
	default:
		*c = ChapterNo
	}
	return nil
}

// RPCStatus mirrors the legacy RpcStatus enum. The original member names are
// Chinese and appear in the UI; they are preserved verbatim so that existing
// state files and UI text stay identical.
type RPCStatus int

// RPC states. Values are persisted; do not reorder.
const (
	RPCWaiting RPCStatus = iota
	RPCSkipped
	RPCError
	RPCFailed
	RPCPassed
)

// String implements fmt.Stringer.
func (r RPCStatus) String() string {
	switch r {
	case RPCSkipped:
		return "跳过"
	case RPCError:
		return "错误"
	case RPCFailed:
		return "未通过"
	case RPCPassed:
		return "通过"
	default:
		return "等待中"
	}
}

// MarshalText implements encoding.TextMarshaler.
func (r RPCStatus) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (r *RPCStatus) UnmarshalText(b []byte) error {
	switch string(b) {
	case "跳过":
		*r = RPCSkipped
	case "错误":
		*r = RPCError
	case "未通过":
		*r = RPCFailed
	case "通过":
		*r = RPCPassed
	default:
		*r = RPCWaiting
	}
	return nil
}

// CanOpenRPCResult reports whether the RPC result window may be opened, i.e.
// the check finished either way.
func (r RPCStatus) CanOpenRPCResult() bool {
	return r == RPCFailed || r == RPCPassed
}

// TaskStatus is the observable state of one task. Every field is serializable
// so the queue can be persisted and, later, streamed between nodes.
type TaskStatus struct {
	ID       TaskID       `json:"id"`
	Name     string       `json:"name"`
	Input    FileRef      `json:"input"`
	Output   FileRef      `json:"output"`
	Enabled  bool         `json:"enabled"`
	Progress TaskProgress `json:"progress"`
	// Status is the human-readable description of the current step.
	Status string `json:"status"`
	// ProgressValue is the current sub-job progress in percent; negative means
	// "unknown".
	ProgressValue float64 `json:"progress_value"`
	// ProgressUnknown is set when ProgressValue is negative.
	ProgressUnknown bool   `json:"progress_unknown"`
	Speed           string `json:"speed"`
	BitRate         string `json:"bit_rate"`
	// TimeRemainSeconds is the estimated remaining time; persisted as seconds
	// because JSON has no duration type.
	TimeRemainSeconds float64       `json:"time_remain_seconds"`
	TaskType          TaskType      `json:"task_type"`
	WorkerName        string        `json:"worker_name"`
	Chapter           ChapterStatus `json:"chapter_status"`
	RPC               RPCStatus     `json:"rpc_status"`
	RPCOutput         string        `json:"rpc_output"`
	// InputSize is the size of the input file in bytes, when known.
	InputSize int64 `json:"input_size"`
	// LengthMS is the source duration in milliseconds, when known.
	LengthMS int64 `json:"length_ms"`
	// Frames is the source frame count, when known.
	Frames int64 `json:"frames"`
}
