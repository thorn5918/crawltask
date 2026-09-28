package api

import (
	"errors"
	"net/http"

	"pyscheduler/internal/config"
	"pyscheduler/internal/envs"
	"pyscheduler/internal/store"
)

// envView 环境视图（含 busy 状态）。
type envView struct {
	*store.Env
	Busy bool `json:"busy"`
}

func (s *Server) envViews(list []*store.Env) []envView {
	out := make([]envView, 0, len(list))
	for _, e := range list {
		out = append(out, envView{Env: e, Busy: s.envm.Busy(e.ID)})
	}
	return out
}

func (s *Server) listEnvs(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListEnvs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []*store.Env{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"envs": s.envViews(list)})
}

func (s *Server) getEnv(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "环境 id 不合法")
		return
	}
	env, err := s.st.GetEnv(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"env": envView{Env: env, Busy: s.envm.Busy(id)}})
}

func (s *Server) createEnv(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Name        string `json:"name"`
		Source      string `json:"source"` // local | download
		Interpreter string `json:"interpreter"`
		Version     string `json:"version"`
	}
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	var env *store.Env
	var err error
	switch p.Source {
	case "local":
		env, err = s.envm.CreateLocal(p.Name, p.Interpreter)
	case "download":
		env, err = s.envm.CreateDownload(p.Name, p.Version)
	case "official":
		env, err = s.envm.CreateOfficial(p.Name, p.Version)
	default:
		err = errors.New("source 需为 local、download 或 official")
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"env": envView{Env: env, Busy: true}})
}

func (s *Server) deleteEnv(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "环境 id 不合法")
		return
	}
	if err := s.envm.Delete(id); err != nil {
		if errors.Is(err, envs.ErrEnvBusy) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "环境不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// envLog 增量读取环境日志（创建/pip 操作）。
func (s *Server) envLog(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "环境 id 不合法")
		return
	}
	env, err := s.st.GetEnv(id)
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	offset := queryInt64(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	content, size := readLogFrom(envs.EnvLogPath(id), offset)
	writeJSON(w, http.StatusOK, map[string]any{
		"size":    size,
		"content": content,
		"busy":    s.envm.Busy(id),
		"status":  env.Status,
	})
}

func (s *Server) envPackages(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "环境 id 不合法")
		return
	}
	pkgs, err := s.envm.PipList(id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if pkgs == nil {
		pkgs = []envs.Pkg{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"packages": pkgs})
}

func (s *Server) envInstall(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "环境 id 不合法")
		return
	}
	var p struct {
		Packages string `json:"packages"`
		Index    string `json:"index"`
	}
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	if err := s.envm.PipInstall(id, p.Packages, p.Index); err != nil {
		if errors.Is(err, envs.ErrEnvBusy) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (s *Server) envUninstall(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt64(r, "id")
	if !ok {
		writeErr(w, http.StatusBadRequest, "环境 id 不合法")
		return
	}
	name := r.URL.Query().Get("name")
	if err := s.envm.PipUninstall(id, name); err != nil {
		if errors.Is(err, envs.ErrEnvBusy) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (s *Server) pythonVersions(w http.ResponseWriter, r *http.Request) {
	versions, source := s.envm.FetchVersions()
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions, "source": source})
}

// officialVersions python.org 官方安装包可用版本（来自国内镜像目录）。
func (s *Server) officialVersions(w http.ResponseWriter, r *http.Request) {
	versions, source := envs.FetchOfficialVersions()
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions, "source": source})
}

func (s *Server) interpreters(w http.ResponseWriter, r *http.Request) {
	list := envs.DiscoverInterpreters()
	if list == nil {
		list = []envs.Interp{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"interpreters": list})
}

func (s *Server) mirrors(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"mirrors": config.Mirrors})
}

// getDownloadMirror 返回 Python 运行时下载加速前缀（空 = 直连 GitHub）。
func (s *Server) getDownloadMirror(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"mirror": s.envm.DownloadMirror()})
}

// saveDownloadMirror 保存下载加速前缀（ghproxy 形式；空串表示直连）。
func (s *Server) saveDownloadMirror(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Mirror string `json:"mirror"`
	}
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	if err := s.envm.SetDownloadMirror(p.Mirror); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mirror": s.envm.DownloadMirror()})
}
