package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"pyscheduler/internal/projects"
	"pyscheduler/internal/store"
)

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	list, err := s.projm.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []*store.Project{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": list})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Name string `json:"name"`
		Tags string `json:"tags"`
	}
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	if err := s.projm.Create(p.Name, strings.TrimSpace(p.Tags)); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	proj, _ := s.st.GetProject(strings.TrimSpace(p.Name))
	writeJSON(w, http.StatusOK, map[string]any{"project": proj})
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var p struct {
		Tags string `json:"tags"`
	}
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	if err := s.projm.UpdateTags(name, strings.TrimSpace(p.Tags)); err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, projects.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "项目不存在")
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.projm.Delete(name); err != nil {
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, projects.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "项目不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sub := r.URL.Query().Get("path")
	entries, err := s.projm.ListFiles(name, sub)
	if err != nil {
		writeProjectErr(w, err)
		return
	}
	if entries == nil {
		entries = []projects.FileInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": sub, "entries": entries})
}

func writeProjectErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, projects.ErrNotFound), errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "目标不存在")
	case errors.Is(err, projects.ErrTraversal):
		writeErr(w, http.StatusBadRequest, "路径不合法（禁止目录穿越）")
	case errors.Is(err, projects.ErrNotText):
		writeErr(w, http.StatusBadRequest, "文件不是 UTF-8 文本或超出 1MB 限制")
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) uploadFiles(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "上传解析失败（总大小需 ≤ 512MB）："+err.Error())
		return
	}
	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		writeErr(w, http.StatusBadRequest, "没有收到文件")
		return
	}
	count := 0
	var errs []string
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			errs = append(errs, fh.Filename+": 打开失败")
			continue
		}
		_, err = s.projm.SaveUploadFile(name, fh.Filename, f)
		f.Close()
		if err != nil {
			errs = append(errs, fh.Filename+": "+err.Error())
			continue
		}
		count++
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": count, "errors": errs})
}

func (s *Server) mkdirProject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var p struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &p); err != nil || strings.TrimSpace(p.Path) == "" {
		writeErr(w, http.StatusBadRequest, "请输入文件夹名称")
		return
	}
	if err := s.projm.Mkdir(name, p.Path); err != nil {
		writeProjectErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) readFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	q := r.URL.Query()
	sub := q.Get("path")
	if q.Get("download") == "1" {
		b, err := s.projm.ReadFileBytes(name, sub)
		if err != nil {
			writeProjectErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`,
			filepath.Base(filepath.ToSlash(sub))))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, strings.NewReader(string(b)))
		return
	}
	content, size, err := s.projm.ReadFile(name, sub)
	if err != nil {
		writeProjectErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"content": content, "size": size})
}

func (s *Server) writeFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	if err := s.projm.WriteFile(name, p.Path, p.Content); err != nil {
		writeProjectErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) deletePath(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sub := r.URL.Query().Get("path")
	if err := s.projm.DeletePath(name, sub); err != nil {
		writeProjectErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
