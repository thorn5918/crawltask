package api

import (
	"io"
	"net/http"
	"os"

	"pyscheduler/internal/runner"
	"pyscheduler/internal/store"
)

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	taskID := queryInt64(r, "task_id", 0)
	items, total, err := s.st.ListRuns(store.RunFilter{
		TaskID:   taskID,
		Status:   q.Get("status"),
		DateFrom: q.Get("date_from"),
		DateTo:   q.Get("date_to"),
		Keyword:  q.Get("keyword"),
		Page:     queryInt(r, "page", 1),
		PageSize: queryInt(r, "page_size", 20),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if items == nil {
		items = []*store.Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "items": items})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "运行 id 不合法")
		return
	}
	run, err := s.st.GetRun(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

// runLog 增量读取运行日志：?offset=N 返回 N 之后的内容与总大小。
func (s *Server) runLog(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "运行 id 不合法")
		return
	}
	run, err := s.st.GetRun(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	offset := queryInt64(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	content, size := readLogFrom(runner.LogPath(run.TaskID, run.ID), offset)
	writeJSON(w, http.StatusOK, map[string]any{
		"size":    size,
		"content": content,
		"status":  run.Status,
	})
}

// readLogFrom 从 offset 处读取日志（单次最多 8MB），返回内容与文件总大小。
func readLogFrom(path string, offset int64) (string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0
	}
	if offset > st.Size() {
		offset = st.Size()
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", st.Size()
	}
	b, err := io.ReadAll(io.LimitReader(f, 8<<20))
	if err != nil {
		return "", st.Size()
	}
	return string(b), st.Size()
}

// deleteRun 删除单条运行记录（含日志文件）；运行中的返回 409。
func (s *Server) deleteRun(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "运行 id 不合法")
		return
	}
	logFile, err := s.st.DeleteRun(id)
	if err != nil {
		if err.Error() == "运行中的记录不允许删除" {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeStoreErr(w, err)
		return
	}
	if logFile != "" {
		_ = os.Remove(logFile)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// cleanupRuns 按筛选条件批量删除运行记录（排除运行中的），并清理对应日志文件。
func (s *Server) cleanupRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, logs, err := s.st.DeleteRunsBy(store.RunFilter{
		TaskID:   queryInt64(r, "task_id", 0),
		Status:   q.Get("status"),
		DateFrom: q.Get("date_from"),
		DateTo:   q.Get("date_to"),
		Keyword:  q.Get("keyword"),
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, lf := range logs {
		_ = os.Remove(lf)
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}
