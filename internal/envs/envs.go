// Package envs 虚拟环境管理：venv 创建、pip 包管理、python-build-standalone 在线下载。
package envs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"pyscheduler/internal/config"
	"pyscheduler/internal/store"
)

// ErrEnvBusy 环境有操作进行中。
var ErrEnvBusy = errors.New("该环境有操作正在进行中，请稍候")

// pbsTag 内置兜底的 python-build-standalone 发布 tag。
const pbsTag = "20260924"

// BuiltinVersions 内置可用版本列表（对应 pbsTag，断网时的兜底）。
var BuiltinVersions = []string{"3.14.7", "3.13.15", "3.12.14", "3.11.16", "3.10.21"}

var envNameRe = regexp.MustCompile(`^[\w.-]{1,64}$`)
var versionRe = regexp.MustCompile(`^3\.\d{1,2}\.\d{1,2}$`)

// Interp 本机解释器。
type Interp struct {
	Path    string `json:"path"`
	Version string `json:"version"`
}

// Pkg pip 包。
type Pkg struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Manager 环境管理器。
type Manager struct {
	st *store.Store

	mu   sync.Mutex
	busy map[int64]bool  // envID -> 操作中
	urls map[string]string // version -> 下载地址（GitHub API 发现）
}

// New 创建环境管理器。
func New(st *store.Store) *Manager {
	return &Manager{st: st, busy: map[int64]bool{}, urls: map[string]string{}}
}

// Busy 环境是否操作中。
func (m *Manager) Busy(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.busy[id]
}

func (m *Manager) tryBusy(id int64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy[id] {
		return false
	}
	m.busy[id] = true
	return true
}

func (m *Manager) setBusy(id int64, v bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v {
		m.busy[id] = true
	} else {
		delete(m.busy, id)
	}
}

// EnvLogPath 环境日志文件路径。
func EnvLogPath(id int64) string {
	return filepath.Join(config.EnvLogsDir, fmt.Sprint(id)+".log")
}

// ---------- 日志辅助 ----------

func resetLog(p string) { _ = os.WriteFile(p, nil, 0o644) }

func appendLog(p, line string) {
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("15:04:05"), line)
}

// logWriter 把子进程输出逐块追加到日志文件。
type logWriter struct{ path string }

func (w logWriter) Write(b []byte) (int, error) {
	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return len(b), nil
	}
	defer f.Close()
	f.Write(b)
	return len(b), nil
}

// ---------- 环境创建 ----------

func venvScriptsDir(venv string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venv, "Scripts")
	}
	return filepath.Join(venv, "bin")
}

func venvPython(venv string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venv, "Scripts", "python.exe")
	}
	return filepath.Join(venv, "bin", "python")
}

// CreateLocal 用本机解释器创建虚拟环境（异步，进度写入环境日志）。
func (m *Manager) CreateLocal(name, interpreter string) (*store.Env, error) {
	name = strings.TrimSpace(name)
	if !envNameRe.MatchString(name) {
		return nil, fmt.Errorf("环境名不合法（1-64 位字母数字下划线点横线）")
	}
	if interpreter = strings.TrimSpace(interpreter); interpreter == "" {
		return nil, fmt.Errorf("请指定本机解释器路径")
	}
	if _, err := os.Stat(interpreter); err != nil {
		return nil, fmt.Errorf("解释器不存在：%s", interpreter)
	}
	if _, err := m.st.GetEnvByName(name); err == nil {
		return nil, fmt.Errorf("环境名已存在：%s", name)
	}
	if _, err := os.Stat(filepath.Join(config.VenvsDir, name)); err == nil {
		return nil, fmt.Errorf("venvs 目录下已有同名文件夹")
	}
	version := probeVersion(interpreter)
	if version == "" {
		return nil, fmt.Errorf("无法执行该解释器（%s），请确认是有效的 Python", interpreter)
	}
	id, err := m.st.CreateEnv(&store.Env{Name: name, PythonVersion: version, Source: "local", Status: "creating"})
	if err != nil {
		return nil, err
	}
	go func() {
		m.setBusy(id, true)
		defer m.setBusy(id, false)
		logPath := EnvLogPath(id)
		resetLog(logPath)
		logf := func(line string) { appendLog(logPath, line) }
		logf("使用本机解释器创建虚拟环境：" + interpreter + "（Python " + version + "）")
		m.createVenv(id, interpreter, name, version, logf)
	}()
	return m.st.GetEnv(id)
}

// CreateDownload 在线下载 Python 并创建虚拟环境（异步）。
func (m *Manager) CreateDownload(name, version string) (*store.Env, error) {
	name = strings.TrimSpace(name)
	if !envNameRe.MatchString(name) {
		return nil, fmt.Errorf("环境名不合法（1-64 位字母数字下划线点横线）")
	}
	if !versionRe.MatchString(version) {
		return nil, fmt.Errorf("Python 版本号不合法：%s", version)
	}
	if _, err := m.st.GetEnvByName(name); err == nil {
		return nil, fmt.Errorf("环境名已存在：%s", name)
	}
	if _, err := os.Stat(filepath.Join(config.VenvsDir, name)); err == nil {
		return nil, fmt.Errorf("venvs 目录下已有同名文件夹")
	}
	id, err := m.st.CreateEnv(&store.Env{Name: name, PythonVersion: version, Source: "download", Status: "creating"})
	if err != nil {
		return nil, err
	}
	go func() {
		m.setBusy(id, true)
		defer m.setBusy(id, false)
		logPath := EnvLogPath(id)
		resetLog(logPath)
		logf := func(line string) { appendLog(logPath, line) }
		logf("准备 Python " + version + " 运行时（python-build-standalone，需访问 GitHub）")
		exe, err := m.ensureRuntime(version, logf)
		if err != nil {
			logf("运行时准备失败：" + err.Error())
			_ = m.st.SetEnvStatus(id, "error")
			return
		}
		m.createVenv(id, exe, name, version, logf)
	}()
	return m.st.GetEnv(id)
}

// CreateOfficial 下载 python.org 官方安装包（国内镜像直连）并静默安装、创建虚拟环境（异步，仅 Windows）。
func (m *Manager) CreateOfficial(name, version string) (*store.Env, error) {
	name = strings.TrimSpace(name)
	if !envNameRe.MatchString(name) {
		return nil, fmt.Errorf("环境名不合法（1-64 位字母数字下划线点横线）")
	}
	if !versionRe.MatchString(version) {
		return nil, fmt.Errorf("Python 版本号不合法：%s", version)
	}
	if runtime.GOOS != "windows" {
		return nil, fmt.Errorf("官方安装包方式仅支持 Windows（Linux/macOS 请用 python-build-standalone 或本机解释器）")
	}
	if _, err := m.st.GetEnvByName(name); err == nil {
		return nil, fmt.Errorf("环境名已存在：%s", name)
	}
	if _, err := os.Stat(filepath.Join(config.VenvsDir, name)); err == nil {
		return nil, fmt.Errorf("venvs 目录下已有同名文件夹")
	}
	id, err := m.st.CreateEnv(&store.Env{Name: name, PythonVersion: version, Source: "official", Status: "creating"})
	if err != nil {
		return nil, err
	}
	go func() {
		m.setBusy(id, true)
		defer m.setBusy(id, false)
		logPath := EnvLogPath(id)
		resetLog(logPath)
		logf := func(line string) { appendLog(logPath, line) }
		logf("准备 Python " + version + "（python.org 官方安装包 · 国内镜像直连）")
		exe, err := m.ensureOfficialRuntime(version, logf)
		if err != nil {
			logf("运行时准备失败：" + err.Error())
			_ = m.st.SetEnvStatus(id, "error")
			return
		}
		m.createVenv(id, exe, name, version, logf)
	}()
	return m.st.GetEnv(id)
}

// createVenv 由指定解释器创建 venv 并回写数据库（由调用方管理 busy 与日志）。
func (m *Manager) createVenv(id int64, interpreter, name, version string, logf func(string)) {
	venv := filepath.Join(config.VenvsDir, name)
	logf("开始创建虚拟环境 → " + venv)
	cmd := exec.Command(interpreter, "-m", "venv", venv)
	cmd.Stdout = logWriter{path: EnvLogPath(id)}
	cmd.Stderr = logWriter{path: EnvLogPath(id)}
	if err := cmd.Run(); err != nil {
		logf("虚拟环境创建失败：" + err.Error())
		_ = m.st.SetEnvStatus(id, "error")
		return
	}
	py := venvPython(venv)
	if _, err := os.Stat(py); err != nil {
		logf("虚拟环境异常：未找到解释器 " + py)
		_ = m.st.SetEnvStatus(id, "error")
		return
	}
	scripts := venvScriptsDir(venv)
	if err := m.st.FinishEnvCreate(id, py, scripts, version, "ready"); err != nil {
		logf("数据库更新失败：" + err.Error())
		return
	}
	logf("环境创建完成。任务命令中的 python 将指向：" + scripts)
}

// Delete 删除虚拟环境（不删除 pythons/ 下可复用的运行时）。
func (m *Manager) Delete(id int64) error {
	env, err := m.st.GetEnv(id)
	if err != nil {
		return store.ErrNotFound
	}
	if m.Busy(id) {
		return ErrEnvBusy
	}
	_ = os.RemoveAll(filepath.Join(config.VenvsDir, env.Name))
	return m.st.DeleteEnv(id)
}

// ---------- Python 运行时在线下载 ----------

func platformID() string {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "windows/amd64":
		return "x86_64-pc-windows-msvc"
	case "windows/arm64":
		return "aarch64-pc-windows-msvc"
	case "linux/amd64":
		return "x86_64-unknown-linux-gnu"
	case "linux/arm64":
		return "aarch64-unknown-linux-gnu"
	case "darwin/amd64":
		return "x86_64-apple-darwin"
	case "darwin/arm64":
		return "aarch64-apple-darwin"
	default:
		return "x86_64-unknown-linux-gnu"
	}
}

// runtimePythonExe 运行时解释器路径。extractTarGz 会剥掉 tarball 顶层的 python/
// 目录，因此运行时内容直接位于 runtimeDir 下（Windows: python.exe；Unix: bin/python3）。
func runtimePythonExe(runtimeDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(runtimeDir, "python.exe")
	}
	return filepath.Join(runtimeDir, "bin", "python3")
}

// ensureRuntime 确保指定版本的运行时存在（已存在则复用），返回解释器路径。
func (m *Manager) ensureRuntime(version string, logf func(string)) (string, error) {
	runtimeDir := filepath.Join(config.PythonsDir, "cpython-"+version)
	exe := runtimePythonExe(runtimeDir)
	if _, err := os.Stat(exe); err == nil {
		logf("检测到已有运行时，直接复用：" + exe)
		return exe, nil
	}
	_ = os.MkdirAll(runtimeDir, 0o755)

	url := m.downloadURL(version)
	logf("下载地址：" + url)

	client := &http.Client{Timeout: 30 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("下载失败：%w（GitHub 不可达时，可在「创建环境」弹窗中配置下载加速前缀）", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("版本 %s 在发布源中不存在（可用版本请查看版本列表）", version)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载失败：HTTP %d", resp.StatusCode)
	}

	tmp := filepath.Join(config.PythonsDir, ".download-cpython-"+version+".tar.gz")
	out, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	pw := &progressWriter{total: resp.ContentLength, logf: logf, last: time.Now()}
	_, err = io.Copy(out, io.TeeReader(resp.Body, pw))
	out.Close()
	if err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("下载中断：%w", err)
	}
	logf("下载完成（" + humanBytes(pw.read) + "），开始解压…")

	if err := extractTarGz(tmp, runtimeDir, logf); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("解压失败：%w", err)
	}
	os.Remove(tmp)
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("解压后未找到解释器：%s", exe)
	}
	logf("运行时就绪：" + exe + "（保留在 pythons/ 下，可复用）")
	return exe, nil
}

// downloadURL 取版本下载地址：优先用 GitHub API 发现的最新地址，否则用内置 tag 构造。
// 配置了加速前缀时按 ghproxy 形式拼接：前缀 + "/" + GitHub 原始地址。
func (m *Manager) downloadURL(version string) string {
	m.mu.Lock()
	u, ok := m.urls[version]
	m.mu.Unlock()
	if !ok {
		u = fmt.Sprintf("https://github.com/astral-sh/python-build-standalone/releases/download/%s/cpython-%s+%s-%s-install_only.tar.gz",
			pbsTag, version, pbsTag, platformID())
	}
	if p := m.DownloadMirror(); p != "" {
		return p + "/" + u
	}
	return u
}

// DownloadMirror 读取 GitHub 下载加速前缀（空串 = 直连）。
func (m *Manager) DownloadMirror() string {
	v, _ := m.st.GetSetting("pb_mirror")
	return strings.TrimRight(strings.TrimSpace(v), "/")
}

// SetDownloadMirror 设置下载加速前缀（ghproxy 形式，如 https://ghfast.top）；空串表示直连 GitHub。
func (m *Manager) SetDownloadMirror(prefix string) error {
	prefix = strings.TrimRight(strings.TrimSpace(prefix), "/")
	if prefix != "" && !strings.HasPrefix(prefix, "http://") && !strings.HasPrefix(prefix, "https://") {
		return fmt.Errorf("加速前缀需以 http:// 或 https:// 开头")
	}
	return m.st.SetSetting("pb_mirror", prefix)
}

// ---------- python.org 官方安装包（国内镜像直连，仅 Windows） ----------

// officialMirrors python.org 官方安装包镜像站（下载时依次尝试，全部失败才报错）。
var officialMirrors = []string{
	"https://mirrors.huaweicloud.com",   // 华为云（官方文档推荐）
	"https://repo.huaweicloud.com",      // 华为云备用域名（同源）
	"https://mirrors.ustc.edu.cn",       // 中科大
	"https://cdn.npmmirror.com/binaries", // npmmirror（淘宝二进制镜像）
}

func officialRuntimeDir(version string) string {
	return filepath.Join(config.PythonsDir, "official-"+version)
}

// ensureOfficialRuntime 下载官方安装包（国内镜像）并静默安装到 pythons/official-<版本>/。
func (m *Manager) ensureOfficialRuntime(version string, logf func(string)) (string, error) {
	runtimeDir := officialRuntimeDir(version)
	exe := filepath.Join(runtimeDir, "python.exe")
	if _, err := os.Stat(exe); err == nil {
		logf("检测到已有官方运行时，直接复用：" + exe)
		return exe, nil
	}
	_ = os.MkdirAll(runtimeDir, 0o755)

	// 候选文件名：arm64 优先，回退 amd64（Windows ARM64 可模拟运行 x64）
	names := []string{fmt.Sprintf("python-%s-amd64.exe", version)}
	if runtime.GOARCH == "arm64" {
		names = []string{fmt.Sprintf("python-%s-arm64.exe", version), names[0]}
	}
	tmp := filepath.Join(config.PythonsDir, ".download-official-"+version+".exe")
	client := &http.Client{Timeout: 30 * time.Minute}
	var lastErr error
outer:
	for _, fname := range names {
		for i, mirror := range officialMirrors {
			url := mirror + "/python/" + version + "/" + fname
			logf(fmt.Sprintf("尝试镜像（%d/%d）：%s", i+1, len(officialMirrors), mirror))
			resp, err := client.Get(url)
			if err != nil {
				lastErr = fmt.Errorf("镜像不可达：%w", err)
				continue
			}
			if resp.StatusCode == http.StatusNotFound {
				resp.Body.Close()
				lastErr = fmt.Errorf("镜像中不存在 %s", fname)
				continue
			}
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
				continue
			}
			logf("下载地址：" + url)
			out, err := os.Create(tmp)
			if err != nil {
				resp.Body.Close()
				return "", err
			}
			pw := &progressWriter{total: resp.ContentLength, logf: logf, last: time.Now()}
			_, err = io.Copy(out, io.TeeReader(resp.Body, pw))
			out.Close()
			resp.Body.Close()
			if err != nil {
				os.Remove(tmp)
				lastErr = fmt.Errorf("下载中断：%w", err)
				continue
			}
			logf("下载完成（" + humanBytes(pw.read) + "），开始静默安装 → " + runtimeDir)
			break outer
		}
	}
	if _, err := os.Stat(tmp); err != nil {
		return "", fmt.Errorf("全部镜像下载失败，最后一个错误：%v", lastErr)
	}
	defer os.Remove(tmp)

	// 静默安装（python.org exe 标准参数，per-user，装到指定目录，不写 PATH/快捷方式）
	cmd := exec.Command(tmp, "/quiet",
		"InstallAllUsers=0",
		"TargetDir="+runtimeDir,
		"Include_pip=1", "Include_test=0", "Include_doc=0",
		"Include_launcher=0", "InstallLauncherAllUsers=0",
		"PrependPath=0", "Shortcuts=0", "AssociateFiles=0",
	)
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 3010 {
			return "", fmt.Errorf("静默安装失败：%w", err)
		}
	}
	if _, err := os.Stat(exe); err != nil {
		return "", fmt.Errorf("安装结束但未找到解释器：%s", exe)
	}
	logf("官方运行时就绪：" + exe + "（保留在 pythons/ 下，可复用）")
	return exe, nil
}

// FetchOfficialVersions 从华为云镜像解析官方可用版本（取最近的若干个），失败时返回内置兜底。
func FetchOfficialVersions() ([]string, string) {
	client := &http.Client{Timeout: 6 * time.Second}
	resp, err := client.Get("https://mirrors.huaweicloud.com/python/")
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
			re := regexp.MustCompile(`href="?(\d+\.\d+\.\d+)/"?`)
			seen := map[string]bool{}
			var list []string
			for _, mm := range re.FindAllStringSubmatch(string(b), -1) {
				v := mm[1]
				if strings.HasPrefix(v, "3.") && !seen[v] {
					seen[v] = true
					list = append(list, v)
				}
			}
			sort.Slice(list, func(i, j int) bool { return versionLess(list[i], list[j]) })
			if len(list) > 15 {
				list = list[len(list)-15:]
			}
			if len(list) > 0 {
				return list, "mirror"
			}
		}
	}
	return append([]string(nil), BuiltinVersions...), "builtin"
}

type progressWriter struct {
	total int64
	read  int64
	logf  func(string)
	last  time.Time
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.read += int64(len(b))
	if time.Since(p.last) >= 2*time.Second {
		p.last = time.Now()
		if p.total > 0 {
			p.logf(fmt.Sprintf("下载进度：%s / %s（%.1f%%）", humanBytes(p.read), humanBytes(p.total),
				float64(p.read)*100/float64(p.total)))
		} else {
			p.logf("已下载：" + humanBytes(p.read))
		}
	}
	return len(b), nil
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// extractTarGz 解压 python-build-standalone 的 install_only 包（tarball 根为 python/）。
func extractTarGz(src, destDir string, logf func(string)) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := path.Clean(hdr.Name)
		if name == "python" || strings.HasPrefix(name, "python/") {
			name = strings.TrimPrefix(name, "python/")
			if name == "" {
				continue
			}
			target := filepath.Join(destDir, filepath.FromSlash(name))
			if rel, err := filepath.Rel(destDir, target); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				continue
			}
			switch hdr.Typeflag {
			case tar.TypeDir:
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
			case tar.TypeReg:
				if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
					return err
				}
				mode := os.FileMode(hdr.Mode) & 0o777
				if mode == 0 {
					mode = 0o644
				}
				out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
				if err != nil {
					return err
				}
				if _, err := io.Copy(out, tr); err != nil {
					out.Close()
					return err
				}
				out.Close()
			case tar.TypeSymlink, tar.TypeLink:
				// 以复制目标文件代替链接（兼容 Windows 与无符号链接权限场景）
				link := hdr.Linkname
				if link != "" && !filepath.IsAbs(link) {
					srcPath := filepath.Join(filepath.Dir(target), filepath.FromSlash(link))
					if b, err := os.ReadFile(srcPath); err == nil {
						_ = os.WriteFile(target, b, 0o755)
					}
				}
			}
		}
	}
	return nil
}

// ---------- 版本发现 / 解释器发现 ----------

// FetchVersions 在线获取可用版本（GitHub API），失败时返回内置列表。
func (m *Manager) FetchVersions() ([]string, string) {
	client := &http.Client{Timeout: 6 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/astral-sh/python-build-standalone/releases/latest", nil)
	if err != nil {
		return append([]string{}, BuiltinVersions...), "builtin"
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return append([]string{}, BuiltinVersions...), "builtin"
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return append([]string{}, BuiltinVersions...), "builtin"
	}
	var rel struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return append([]string{}, BuiltinVersions...), "builtin"
	}
	re := regexp.MustCompile(`^cpython-(3\.\d+\.\d+)\+\S+-` + platformID() + `-install_only\.tar\.gz$`)
	seen := map[string]bool{}
	var out []string
	m.mu.Lock()
	for _, a := range rel.Assets {
		if match := re.FindStringSubmatch(a.Name); match != nil && !seen[match[1]] {
			seen[match[1]] = true
			out = append(out, match[1])
			m.urls[match[1]] = a.URL
		}
	}
	m.mu.Unlock()
	if len(out) == 0 {
		return append([]string{}, BuiltinVersions...), "builtin"
	}
	sort.Slice(out, func(i, j int) bool { return versionLess(out[j], out[i]) })
	return out, "github"
}

func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		xa, _ := strconv.Atoi(pa[i])
		xb, _ := strconv.Atoi(pb[i])
		if xa != xb {
			return xa < xb
		}
	}
	return len(pa) < len(pb)
}

// DiscoverInterpreters 发现本机已安装的 Python 解释器。
func DiscoverInterpreters() []Interp {
	var candidates []string
	if runtime.GOOS == "windows" {
		candidates = windowsCandidates()
	} else {
		candidates = unixCandidates()
	}
	seen := map[string]bool{}
	var out []Interp
	for _, c := range candidates {
		if c == "" || seen[strings.ToLower(c)] {
			continue
		}
		seen[strings.ToLower(c)] = true
		if ver := probeVersion(c); ver != "" {
			out = append(out, Interp{Path: c, Version: ver})
		}
	}
	return out
}

func probeVersion(p string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p, "-c", "import sys; print('.'.join(map(str, sys.version_info[:3])))")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func windowsCandidates() []string {
	var out []string
	for _, name := range []string{"python", "python3"} {
		if p, err := exec.LookPath(name); err == nil {
			out = append(out, p)
		}
	}
	// py 启动器：py -0p 列出全部已注册解释器
	if p, err := exec.LookPath("py"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if outBytes, err := exec.CommandContext(ctx, p, "-0p").Output(); err == nil {
			re := regexp.MustCompile(`\(([^()]+\.exe)\)`)
			for _, m := range re.FindAllStringSubmatch(string(outBytes), -1) {
				out = append(out, m[1])
			}
		}
	}
	globs := []string{
		`C:\Python3*\python.exe`,
		`C:\Program Files\Python3*\python.exe`,
		filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Python", "Python3*", "python.exe"),
	}
	for _, g := range globs {
		if matches, _ := filepath.Glob(g); matches != nil {
			out = append(out, matches...)
		}
	}
	return out
}

func unixCandidates() []string {
	var out []string
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			out = append(out, p)
		}
	}
	re := regexp.MustCompile(`^python3\.\d+$`)
	for _, dir := range []string{"/usr/bin", "/usr/local/bin"} {
		if matches, _ := filepath.Glob(filepath.Join(dir, "python3*")); matches != nil {
			for _, p := range matches {
				if re.MatchString(filepath.Base(p)) {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

// ---------- pip 包管理 ----------

// PipList 已安装包列表（同步）。
func (m *Manager) PipList(envID int64) ([]Pkg, error) {
	env, err := m.st.GetEnv(envID)
	if err != nil {
		return nil, store.ErrNotFound
	}
	if env.Status != "ready" || env.Interpreter == "" {
		return nil, fmt.Errorf("环境未就绪（status=%s）", env.Status)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, env.Interpreter, "-m", "pip", "list", "--format=json")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	if err != nil && buf.Len() == 0 {
		return nil, fmt.Errorf("pip list 执行失败：%v", err)
	}
	var pkgs []Pkg
	if err := json.Unmarshal(buf.Bytes(), &pkgs); err != nil {
		return nil, fmt.Errorf("pip list 输出解析失败：%s", truncate(buf.String(), 200))
	}
	sort.Slice(pkgs, func(i, j int) bool { return strings.ToLower(pkgs[i].Name) < strings.ToLower(pkgs[j].Name) })
	return pkgs, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// PipInstall 安装包（异步，输出追加到环境日志）。
func (m *Manager) PipInstall(envID int64, packages, index string) error {
	env, err := m.st.GetEnv(envID)
	if err != nil {
		return store.ErrNotFound
	}
	if env.Status != "ready" || env.Interpreter == "" {
		return fmt.Errorf("环境未就绪")
	}
	packages = strings.TrimSpace(packages)
	if packages == "" {
		return fmt.Errorf("请输入要安装的包（如 pandas==2.2.0 requests）")
	}
	if !m.tryBusy(envID) {
		return ErrEnvBusy
	}
	go m.pipOp(envID, "install", packages, index)
	return nil
}

// PipUninstall 卸载包（异步）。
func (m *Manager) PipUninstall(envID int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || strings.ContainsAny(name, " \t\r\n") {
		return fmt.Errorf("包名不合法")
	}
	env, err := m.st.GetEnv(envID)
	if err != nil {
		return store.ErrNotFound
	}
	if env.Status != "ready" || env.Interpreter == "" {
		return fmt.Errorf("环境未就绪")
	}
	if !m.tryBusy(envID) {
		return ErrEnvBusy
	}
	go m.pipOp(envID, "uninstall", name, "")
	return nil
}

func (m *Manager) pipOp(envID int64, op, packages, index string) {
	defer m.setBusy(envID, false)
	env, err := m.st.GetEnv(envID)
	if err != nil {
		return
	}
	logPath := EnvLogPath(envID)
	resetLog(logPath)
	logf := func(line string) { appendLog(logPath, line) }

	args := []string{"-m", "pip", op}
	if op == "uninstall" {
		args = append(args, "-y")
	}
	args = append(args, strings.Fields(packages)...)
	if index != "" {
		args = append(args, "-i", index)
	}
	logf("执行：" + env.Interpreter + " " + strings.Join(args, " "))
	cmd := exec.Command(env.Interpreter, args...)
	cmd.Stdout = logWriter{path: logPath}
	cmd.Stderr = logWriter{path: logPath}
	if err := cmd.Run(); err != nil {
		logf("=== pip " + op + " 失败（" + err.Error() + "）===")
	} else {
		logf("=== pip " + op + " 完成 ===")
	}
}
