// Package projects 项目文件管理：目录浏览、上传、在线编辑（含路径穿越防护）。
package projects

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"pyscheduler/internal/config"
	"pyscheduler/internal/store"
)

// ErrTraversal 路径穿越被拒绝。
var ErrTraversal = errors.New("路径不合法（禁止目录穿越）")

// ErrNotText 非文本文件。
var ErrNotText = errors.New("文件不是 UTF-8 文本或超出大小限制")

// ErrNotFound 目标不存在。
var ErrNotFound = errors.New("目标不存在")

var nameRe = regexp.MustCompile(`^[^/\\:*?"<>|\x00-\x1f]{1,64}$`)

// FileInfo 目录条目。
type FileInfo struct {
	Name  string `json:"name"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

// Manager 项目管理器。
type Manager struct {
	st *store.Store
}

// New 创建项目管理器。
func New(st *store.Store) *Manager { return &Manager{st: st} }

// Root 项目根目录。
func Root(name string) string { return filepath.Join(config.ProjectsDir, name) }

// SafeJoin 把相对路径安全地限制在 root 之内。
func SafeJoin(root, rel string) (string, error) {
	rel = filepath.ToSlash(rel)
	rel = path.Clean("/" + rel) // 以 / 开头后 .. 会在根部坍缩，无法逃逸
	p := filepath.Join(root, rel)
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	absP, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if absP != absRoot && !strings.HasPrefix(absP, absRoot+string(os.PathSeparator)) {
		return "", ErrTraversal
	}
	return absP, nil
}

func validName(name string) bool {
	return nameRe.MatchString(strings.TrimSpace(name)) && strings.TrimSpace(name) != "." && strings.TrimSpace(name) != ".."
}

// List 项目列表（磁盘文件夹 + 数据库标签）。
func (m *Manager) List() ([]*store.Project, error) {
	dbProjects, err := m.st.ListProjects()
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	created := map[string]string{}
	for _, p := range dbProjects {
		tags[p.Name] = p.Tags
		created[p.Name] = p.CreatedAt
	}
	entries, err := os.ReadDir(config.ProjectsDir)
	if err != nil {
		return nil, err
	}
	var out []*store.Project
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		out = append(out, &store.Project{
			Name:      name,
			Tags:      tags[name],
			CreatedAt: created[name],
		})
	}
	// 数据库中有记录但文件夹已被人手动删除的项目：保留记录展示，便于排查
	for _, p := range dbProjects {
		if _, err := os.Stat(filepath.Join(config.ProjectsDir, p.Name)); err != nil {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Create 新建项目（文件夹 + 记录）。
func (m *Manager) Create(name, tags string) error {
	name = strings.TrimSpace(name)
	if !validName(name) {
		return fmt.Errorf("项目名不合法（1-64 字符，不能包含 / \\ : * ? \" < > |）")
	}
	if _, err := os.Stat(Root(name)); err == nil {
		return fmt.Errorf("项目已存在：%s", name)
	}
	if err := os.MkdirAll(Root(name), 0o755); err != nil {
		return err
	}
	return m.st.UpsertProject(name, tags)
}

// UpdateTags 更新项目标签。
func (m *Manager) UpdateTags(name, tags string) error {
	if _, err := os.Stat(Root(name)); err != nil {
		return ErrNotFound
	}
	return m.st.UpsertProject(name, tags)
}

// Delete 删除项目（递归删除文件夹 + 记录）。
func (m *Manager) Delete(name string) error {
	if _, err := os.Stat(Root(name)); err != nil {
		return ErrNotFound
	}
	if err := os.RemoveAll(Root(name)); err != nil {
		return err
	}
	return m.st.DeleteProject(name)
}

// ListFiles 浏览项目内目录。
func (m *Manager) ListFiles(name, sub string) ([]FileInfo, error) {
	dir, err := SafeJoin(Root(name), sub)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var out []FileInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileInfo{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  info.Size(),
			Mtime: info.ModTime().Format("2006-01-02 15:04"),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Mkdir 新建文件夹。
func (m *Manager) Mkdir(name, sub string) error {
	p, err := SafeJoin(Root(name), sub)
	if err != nil {
		return err
	}
	return os.MkdirAll(p, 0o755)
}

// sanitizeUploadName 处理上传文件名：可能是相对路径（webkitdirectory）或纯文件名。
func sanitizeUploadName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	// 去掉 Windows 旧浏览器的 C:\fakepath\ 前缀
	if i := strings.LastIndex(name, ":"); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimLeft(name, "/")
	return name
}

// SaveUploadFile 保存一个上传文件（rel 为项目内相对路径）。
func (m *Manager) SaveUploadFile(project, filename string, r io.Reader) (string, error) {
	rel := sanitizeUploadName(filename)
	if rel == "" || rel == "." || rel == ".." {
		return "", fmt.Errorf("文件名为空")
	}
	target, err := SafeJoin(Root(project), rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(target)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := io.Copy(f, r); err != nil {
		return "", err
	}
	return rel, nil
}

// ReadFile 读取项目内文本文件（UTF-8、≤1MB）。
func (m *Manager) ReadFile(name, sub string) (string, int64, error) {
	p, err := SafeJoin(Root(name), sub)
	if err != nil {
		return "", 0, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return "", 0, ErrNotFound
	}
	if st.IsDir() {
		return "", 0, fmt.Errorf("目标是文件夹")
	}
	if st.Size() > config.MaxEditSize {
		return "", 0, ErrNotText
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", 0, err
	}
	if !utf8.Valid(b) {
		return "", 0, ErrNotText
	}
	return string(b), st.Size(), nil
}

// ReadFileBytes 读取项目内文件原始内容（下载用，限制 64MB）。
func (m *Manager) ReadFileBytes(name, sub string) ([]byte, error) {
	p, err := SafeJoin(Root(name), sub)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(p)
	if err != nil || st.IsDir() {
		return nil, ErrNotFound
	}
	if st.Size() > 64<<20 {
		return nil, fmt.Errorf("文件过大")
	}
	return os.ReadFile(p)
}

// WriteFile 保存项目内文本文件。
func (m *Manager) WriteFile(name, sub, content string) error {
	if len(content) > config.MaxEditSize {
		return ErrNotText
	}
	p, err := SafeJoin(Root(name), sub)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(content), 0o644)
}

// DeletePath 删除项目内文件或文件夹。
func (m *Manager) DeletePath(name, sub string) error {
	p, err := SafeJoin(Root(name), sub)
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); err != nil {
		return ErrNotFound
	}
	if p == Root(name) {
		return fmt.Errorf("不能删除项目根目录，请使用「删除项目」")
	}
	return os.RemoveAll(p)
}
