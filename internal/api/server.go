// Package api HTTP API 层：路由注册与通用辅助。
package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"pyscheduler/internal/envs"
	"pyscheduler/internal/projects"
	"pyscheduler/internal/runner"
	"pyscheduler/internal/scheduler"
	"pyscheduler/internal/store"
)

// Server 聚合全部依赖。
type Server struct {
	st    *store.Store
	sch   *scheduler.Scheduler
	rn    *runner.Runner
	envm  *envs.Manager
	projm *projects.Manager
}

// New 创建 API 服务。
func New(st *store.Store, sch *scheduler.Scheduler, rn *runner.Runner, envm *envs.Manager, projm *projects.Manager) *Server {
	return &Server{st: st, sch: sch, rn: rn, envm: envm, projm: projm}
}

// Routes 注册全部 API 路由（Go 1.22 方法+路径模式）。
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	// 任务
	mux.HandleFunc("GET /api/tasks", s.listTasks)
	mux.HandleFunc("POST /api/tasks", s.createTask)
	mux.HandleFunc("PUT /api/tasks/{id}", s.updateTask)
	mux.HandleFunc("DELETE /api/tasks/{id}", s.deleteTask)
	mux.HandleFunc("POST /api/tasks/{id}/run", s.runTask)
	mux.HandleFunc("POST /api/tasks/{id}/pause", s.pauseTask)
	mux.HandleFunc("POST /api/tasks/{id}/resume", s.resumeTask)
	mux.HandleFunc("POST /api/tasks/{id}/kill", s.killTask)
	mux.HandleFunc("GET /api/tasks/{id}/runs", s.taskRuns)

	// 运行记录
	mux.HandleFunc("GET /api/runs", s.listRuns)
	mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	mux.HandleFunc("GET /api/runs/{id}/log", s.runLog)

	// 环境
	mux.HandleFunc("GET /api/envs", s.listEnvs)
	mux.HandleFunc("POST /api/envs", s.createEnv)
	mux.HandleFunc("GET /api/envs/{id}", s.getEnv)
	mux.HandleFunc("DELETE /api/envs/{id}", s.deleteEnv)
	mux.HandleFunc("GET /api/envs/{id}/log", s.envLog)
	mux.HandleFunc("GET /api/envs/{id}/packages", s.envPackages)
	mux.HandleFunc("POST /api/envs/{id}/packages", s.envInstall)
	mux.HandleFunc("DELETE /api/envs/{id}/packages", s.envUninstall)
	mux.HandleFunc("GET /api/python-versions", s.pythonVersions)
	mux.HandleFunc("GET /api/interpreters", s.interpreters)
	mux.HandleFunc("GET /api/mirrors", s.mirrors)
	mux.HandleFunc("GET /api/download-mirror", s.getDownloadMirror)
	mux.HandleFunc("POST /api/download-mirror", s.saveDownloadMirror)

	// 项目
	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("PUT /api/projects/{name}", s.updateProject)
	mux.HandleFunc("DELETE /api/projects/{name}", s.deleteProject)
	mux.HandleFunc("GET /api/projects/{name}/files", s.listFiles)
	mux.HandleFunc("POST /api/projects/{name}/upload", s.uploadFiles)
	mux.HandleFunc("POST /api/projects/{name}/mkdir", s.mkdirProject)
	mux.HandleFunc("GET /api/projects/{name}/file", s.readFile)
	mux.HandleFunc("PUT /api/projects/{name}/file", s.writeFile)
	mux.HandleFunc("DELETE /api/projects/{name}/file", s.deletePath)

	// 仪表盘
	mux.HandleFunc("GET /api/dashboard/overview", s.overview)
	mux.HandleFunc("GET /api/dashboard/chart", s.chart)

	// 通知设置
	mux.HandleFunc("GET /api/settings/notify", s.getNotify)
	mux.HandleFunc("POST /api/settings/notify", s.saveNotify)
	mux.HandleFunc("POST /api/settings/notify/test", s.testNotify)

	return mux
}

// ---------- 通用辅助 ----------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	if err == store.ErrNotFound {
		writeErr(w, http.StatusNotFound, "记录不存在")
		return
	}
	writeErr(w, http.StatusBadRequest, err.Error())
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(v)
}

func pathInt64(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

func queryInt(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func queryInt64(r *http.Request, name string, def int64) int64 {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return n
}
