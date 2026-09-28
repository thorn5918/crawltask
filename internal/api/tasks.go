package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"pyscheduler/internal/config"
	"pyscheduler/internal/runner"
	"pyscheduler/internal/store"
)

// taskView 任务列表视图（附带环境名、调度描述、最近运行）。
type taskView struct {
	store.Task
	EnvName      string `json:"env_name"`
	ScheduleDesc string `json:"schedule_desc"`
	LastStatus   string `json:"last_status"`
	LastEndTime  string `json:"last_end_time"`
	LastDuration int64  `json:"last_duration_ms"`
	Running      int    `json:"running"`
}

type taskPayload struct {
	Name            string `json:"name"`
	Command         string `json:"command"`
	Project         string `json:"project"`
	Workdir         string `json:"workdir"`
	EnvID           int64  `json:"env_id"`
	ScheduleType    string `json:"schedule_type"`
	IntervalSeconds int64  `json:"interval_seconds"`
	RunDate         string `json:"run_date"`
	CronExpr        string `json:"cron_expr"`
	MaxConcurrent   int    `json:"max_concurrent"`
	Tags            string `json:"tags"`
}

func scheduleDesc(t *store.Task) string {
	switch t.ScheduleType {
	case "manual":
		return "手动执行"
	case "interval":
		d := time.Duration(t.IntervalSeconds) * time.Second
		return "间隔执行 · " + d.Truncate(time.Second).String()
	case "date":
		return "一次性 · " + strings.Replace(t.RunDate, "T", " ", 1)
	case "cron":
		return "Cron · " + t.CronExpr
	}
	return t.ScheduleType
}

func validateTask(p *taskPayload) error {
	p.Name = strings.TrimSpace(p.Name)
	p.Command = strings.TrimSpace(p.Command)
	p.Project = strings.TrimSpace(p.Project)
	p.Workdir = strings.TrimSpace(p.Workdir)
	p.CronExpr = strings.TrimSpace(p.CronExpr)
	p.Tags = strings.TrimSpace(p.Tags)
	p.RunDate = strings.TrimSpace(p.RunDate)

	if p.Name == "" {
		return fmt.Errorf("任务名称不能为空")
	}
	if p.Command == "" {
		return fmt.Errorf("命令不能为空")
	}
	switch p.ScheduleType {
	case "manual":
	case "interval":
		if p.IntervalSeconds < 1 {
			return fmt.Errorf("间隔秒数需 ≥ 1")
		}
	case "date":
		if _, err := time.ParseInLocation("2006-01-02T15:04", p.RunDate, time.Local); err != nil {
			return fmt.Errorf("执行时间格式不合法")
		}
	case "cron":
		if _, err := cron.ParseStandard(p.CronExpr); err != nil {
			return fmt.Errorf("Cron 表达式不合法：%v", err)
		}
	default:
		return fmt.Errorf("调度方式不合法：%s", p.ScheduleType)
	}
	if p.MaxConcurrent < 1 {
		p.MaxConcurrent = 1
	}
	if p.MaxConcurrent > 100 {
		p.MaxConcurrent = 100
	}
	if p.Workdir != "" {
		p.Workdir = filepath.ToSlash(filepath.Clean(p.Workdir))
		if p.Workdir == ".." || strings.HasPrefix(p.Workdir, "../") {
			return fmt.Errorf("工作路径不能超出项目目录")
		}
	}
	return nil
}

func (s *Server) taskToView(t *store.Task, envNames map[int64]string, last map[int64]*store.Run, running map[int64]int) *taskView {
	v := &taskView{Task: *t, EnvName: envNames[t.EnvID], Running: running[t.ID], ScheduleDesc: scheduleDesc(t)}
	if lr, ok := last[t.ID]; ok {
		v.LastStatus = lr.Status
		v.LastEndTime = lr.EndTime
		v.LastDuration = lr.DurationMS
	}
	return v
}

func (s *Server) buildViews(tasks []*store.Task) []*taskView {
	envNames := map[int64]string{}
	if envs, err := s.st.ListEnvs(); err == nil {
		for _, e := range envs {
			envNames[e.ID] = e.Name
		}
	}
	last := map[int64]*store.Run{}
	if m, err := s.st.LastRunPerTask(); err == nil {
		last = m
	}
	running := s.rn.RunningCounts()
	out := make([]*taskView, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, s.taskToView(t, envNames, last, running))
	}
	return out
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.st.ListTasks()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": s.buildViews(tasks)})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var p taskPayload
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	if err := validateTask(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t := &store.Task{
		Name: p.Name, Command: p.Command, Project: p.Project, Workdir: p.Workdir,
		EnvID: p.EnvID, ScheduleType: p.ScheduleType, IntervalSeconds: p.IntervalSeconds,
		RunDate: p.RunDate, CronExpr: p.CronExpr, MaxConcurrent: p.MaxConcurrent,
		Enabled: true, Tags: p.Tags,
	}
	id, err := s.st.CreateTask(t)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	t.ID = id
	s.sch.Sync(id)
	writeJSON(w, http.StatusOK, map[string]any{"task": s.buildViews([]*store.Task{t})[0]})
}

func (s *Server) updateTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "任务 id 不合法")
		return
	}
	old, err := s.st.GetTask(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	var p taskPayload
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	if err := validateTask(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	t := *old
	t.Name, t.Command, t.Project, t.Workdir = p.Name, p.Command, p.Project, p.Workdir
	t.EnvID, t.ScheduleType, t.IntervalSeconds = p.EnvID, p.ScheduleType, p.IntervalSeconds
	t.RunDate, t.CronExpr, t.MaxConcurrent, t.Tags = p.RunDate, p.CronExpr, p.MaxConcurrent, p.Tags
	if err := s.st.UpdateTask(&t); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.sch.Sync(id)
	writeJSON(w, http.StatusOK, map[string]any{"task": s.buildViews([]*store.Task{&t})[0]})
}

func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "任务 id 不合法")
		return
	}
	if _, err := s.st.GetTask(id); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.rn.KillTask(id)
	s.sch.Sync(id) // 停止调度
	if err := s.st.DeleteTask(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	_ = os.RemoveAll(filepath.Join(config.TaskLogsDir, fmt.Sprint(id)))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) runTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "任务 id 不合法")
		return
	}
	t, err := s.st.GetTask(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	runID, err := s.rn.StartRun(t, "manual")
	if err == runner.ErrBusy {
		writeErr(w, http.StatusConflict, "上一轮尚未结束（并发已满），请稍后再试或先终止")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID})
}

func (s *Server) pauseTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "任务 id 不合法")
		return
	}
	if err := s.st.SetTaskEnabled(id, false); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.sch.Sync(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) resumeTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "任务 id 不合法")
		return
	}
	if err := s.st.SetTaskEnabled(id, true); err != nil {
		writeStoreErr(w, err)
		return
	}
	s.sch.Sync(id)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) killTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "任务 id 不合法")
		return
	}
	n := s.rn.KillTask(id)
	writeJSON(w, http.StatusOK, map[string]any{"killed": n})
}

func (s *Server) taskRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "任务 id 不合法")
		return
	}
	items, total, err := s.st.ListRuns(store.RunFilter{
		TaskID:   id,
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
