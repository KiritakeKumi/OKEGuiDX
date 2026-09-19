package api

import (
	"net/http"

	"github.com/KiritakeKumi/OKEGuiDX/internal/model"
	"github.com/KiritakeKumi/OKEGuiDX/internal/node"
	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
)

// statusResponse is the payload of GET /api/v1/status: what this node can do
// and what its queue looks like. It is the one call the Web UI makes before it
// draws anything.
type statusResponse struct {
	Node  node.Capabilities `json:"node"`
	Pool  poolStatus        `json:"pool"`
	Queue queueStatus       `json:"queue"`
}

// poolStatus is the worker pool's half of the status.
type poolStatus struct {
	Running bool `json:"running"`
	// Workers is how many workers are registered, running or not.
	Workers int `json:"workers"`
	// ActiveWorkers is how many are currently allowed to take work.
	ActiveWorkers int `json:"active_workers"`
	// StopTimeoutSeconds is how long POST /pool/stop waits before answering
	// "stopping" instead of "stopped".
	StopTimeoutSeconds int          `json:"stop_timeout_seconds"`
	WorkerList         []workerInfo `json:"worker_list"`
}

// workerInfo is engine.Worker with JSON names; the engine type has no tags
// because it is not a wire format.
type workerInfo struct {
	Wid  int    `json:"wid"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// queueStatus counts the queue by state. The counts come from one snapshot, so
// they always add up to Total even while tasks move.
type queueStatus struct {
	Total    int `json:"total"`
	Waiting  int `json:"waiting"`
	Running  int `json:"running"`
	Error    int `json:"error"`
	Finished int `json:"finished"`
	// Enabled counts tasks whose checkbox is ticked, which includes finished
	// ones; Active counts enabled tasks that are still waiting.
	Enabled int `json:"enabled"`
	Active  int `json:"active"`
}

// handleStatus implements GET /api/v1/status.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	caps, err := s.exec.Capabilities(ctx)
	if err != nil {
		writeError(w, errorStatus(err), err)
		return
	}

	writeJSON(w, http.StatusOK, statusResponse{
		Node:  caps,
		Pool:  s.poolStatus(),
		Queue: queueStatusOf(s.tasks.Snapshot()),
	})
}

// poolStatus reads the pool once.
func (s *Server) poolStatus() poolStatus {
	workers := s.pool.Workers()
	list := make([]workerInfo, 0, len(workers))
	for _, worker := range workers {
		list = append(list, workerInfo{Wid: worker.Wid, Name: worker.Name, Type: worker.WType.String()})
	}
	return poolStatus{
		Running:            s.pool.IsRunning(),
		Workers:            s.pool.GetWorkerCount(),
		ActiveWorkers:      s.pool.GetBGWorkerCount(),
		StopTimeoutSeconds: int(s.stopTimeout.Seconds()),
		WorkerList:         list,
	}
}

// queueStatusOf counts a queue snapshot by state.
func queueStatusOf(tasks []model.Task) queueStatus {
	var st queueStatus
	st.Total = len(tasks)
	for i := range tasks {
		t := &tasks[i]
		switch t.Status.Progress {
		case model.TaskRunning:
			st.Running++
		case model.TaskError:
			st.Error++
		case model.TaskFinished:
			st.Finished++
		default:
			st.Waiting++
		}
		if t.Status.Enabled {
			st.Enabled++
			if t.Status.Progress == model.TaskWaiting {
				st.Active++
			}
		}
	}
	return st
}

// lookupTask resolves the {id} path value. A malformed id is a client error
// (400) rather than a missing resource, so the two are reported differently.
func (s *Server) lookupTask(w http.ResponseWriter, r *http.Request) (model.TaskID, bool) {
	raw := r.PathValue("id")
	id, err := model.ParseTaskID(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, okerr.Wrap(err, okerr.KindConfig,
			"任务 ID 不合法", "%q 不是合法的任务 ID。", raw))
		return "", false
	}
	return id, true
}

// getTask returns a copy of the task, answering 404 when it is unknown.
func (s *Server) getTask(w http.ResponseWriter, id model.TaskID) (model.Task, bool) {
	task, ok := s.tasks.Task(id)
	if !ok {
		writeError(w, http.StatusNotFound, notFoundTask(id))
		return model.Task{}, false
	}
	return task, true
}

// notFoundTask is the error every endpoint reports for an unknown id.
func notFoundTask(id model.TaskID) error {
	return okerr.New(okerr.KindNotFound, "找不到任务", "任务 %s 不在队列中。", id)
}
