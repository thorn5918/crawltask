// Package runner 子进程运行器：日志采集、进程树终止、失败通知。
package runner

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"pyscheduler/internal/config"
	"pyscheduler/internal/notify"
	"pyscheduler/internal/projects"
	"pyscheduler/internal/store"
)

// ErrBusy 并发已满（上一轮尚未结束）。
var ErrBusy = errors.New("上一轮任务尚未结束，本次触发已跳过")

// Runner 管理全部运行中的子进程。
type Runner struct {
	st    *store.Store
	mu    sync.Mutex
	procs map[int64]*Proc // runID -> proc
	wg    sync.WaitGroup
}

// Proc 一次运行对应的子进程。
type Proc struct {
	runID  int64
	taskID int64
	cmd    *exec.Cmd
	mu     sync.Mutex
	killed bool
}

// New 创建运行器。
func New(st *store.Store) *Runner {
	return &Runner{st: st, procs: map[int64]*Proc{}}
}

// RunningCount 任务当前运行中的实例数。
func (r *Runner) RunningCount(taskID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.procs {
		if p.taskID == taskID {
			n++
		}
	}
	return n
}

// RunningTotal 全部运行中的实例数。
func (r *Runner) RunningTotal() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.procs)
}

// RunningCounts 各任务运行中的实例数。
func (r *Runner) RunningCounts() map[int64]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[int64]int{}
	for _, p := range r.procs {
		out[p.taskID]++
	}
	return out
}

// LogPath 运行日志文件路径（确定性规则：logs/tasks/<任务id>/<运行id>.log）。
func LogPath(taskID, runID int64) string {
	return filepath.Join(config.TaskLogsDir, fmt.Sprint(taskID), fmt.Sprint(runID)+".log")
}

// StartRun 创建运行记录并启动子进程。
// 并发已满返回 ErrBusy；环境/目录等启动前错误会生成一条 failed 运行记录并返回其 id。
func (r *Runner) StartRun(t *store.Task, trigger string) (int64, error) {
	r.mu.Lock()
	running := 0
	for _, p := range r.procs {
		if p.taskID == t.ID {
			running++
		}
	}
	maxc := t.MaxConcurrent
	if maxc < 1 {
		maxc = 1
	}
	if running >= maxc {
		r.mu.Unlock()
		return 0, ErrBusy
	}
	r.mu.Unlock()

	// 先创建运行记录拿到 id
	run := &store.Run{TaskID: t.ID, TaskName: t.Name, Trigger: trigger}
	runID, err := r.st.CreateRun(run)
	if err != nil {
		return 0, err
	}
	logPath := LogPath(t.ID, runID)

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return runID, err
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return runID, err
	}

	fail := func(msg string) (int64, error) {
		fmt.Fprintln(logFile, "[PyScheduler] "+msg)
		logFile.Close()
		end := time.Now()
		code := -1
		_ = r.st.FinishRun(runID, "failed", end.Format(config.TimeLayout), 0, &code)
		return runID, nil
	}

	// 解析工作目录
	workdir := config.BaseDir
	if t.Project != "" {
		root := filepath.Join(config.ProjectsDir, t.Project)
		if t.Workdir == "" {
			workdir = root
		} else {
			wd, err := projects.SafeJoin(root, t.Workdir)
			if err != nil {
				return fail("工作路径非法：" + err.Error())
			}
			workdir = wd
		}
	}
	if st, err := os.Stat(workdir); err != nil || !st.IsDir() {
		return fail("工作目录不存在：" + workdir)
	}

	// 解析虚拟环境 PATH
	var extraEnv []string
	if t.EnvID > 0 {
		env, err := r.st.GetEnv(t.EnvID)
		if err != nil {
			return fail(fmt.Sprintf("虚拟环境不存在（env_id=%d）", t.EnvID))
		}
		if env.Status != "ready" || env.ScriptsDir == "" {
			return fail("虚拟环境未就绪：" + env.Name + "（status=" + env.Status + "）")
		}
		if _, err := os.Stat(env.ScriptsDir); err != nil {
			return fail("虚拟环境目录缺失：" + env.ScriptsDir)
		}
		venvRoot := filepath.Dir(env.ScriptsDir)
		extraEnv = []string{
			"PATH=" + env.ScriptsDir + string(os.PathListSeparator) + os.Getenv("PATH"),
			"VIRTUAL_ENV=" + venvRoot,
		}
	}

	cmd := buildCmd(t.Command)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setSysProcAttr(cmd)

	if err := cmd.Start(); err != nil {
		fmt.Fprintln(logFile, "[PyScheduler] 启动失败："+err.Error())
		logFile.Close()
		end := time.Now()
		code := -1
		_ = r.st.FinishRun(runID, "failed", end.Format(config.TimeLayout), 0, &code)
		return runID, nil
	}

	p := &Proc{runID: runID, taskID: t.ID, cmd: cmd}
	r.mu.Lock()
	r.procs[runID] = p
	r.mu.Unlock()
	r.wg.Add(1)

	start := time.Now()
	go func() {
		defer r.wg.Done()
		waitErr := cmd.Wait()
		logFile.Close()

		p.mu.Lock()
		killed := p.killed
		p.mu.Unlock()

		code := 0
		if waitErr != nil {
			code = -1
			if ee, ok := waitErr.(*exec.ExitError); ok {
				code = ee.ExitCode()
			}
		}
		status := "success"
		switch {
		case killed:
			status = "killed"
		case waitErr != nil:
			status = "failed"
		}
		end := time.Now()
		_ = r.st.FinishRun(runID, status, end.Format(config.TimeLayout), end.Sub(start).Milliseconds(), &code)

		r.mu.Lock()
		delete(r.procs, runID)
		r.mu.Unlock()

		r.maybeNotify(t, runID, logPath, status, start, end, code)
	}()

	return runID, nil
}

// TriggerScheduled 调度触发：并发已满时静默跳过（coalesce 语义）。
func (r *Runner) TriggerScheduled(t *store.Task) {
	_, err := r.StartRun(t, "schedule")
	if errors.Is(err, ErrBusy) {
		log.Printf("[scheduler] 任务[%s]上一轮未结束，跳过本次调度", t.Name)
	} else if err != nil {
		log.Printf("[scheduler] 任务[%s]触发失败: %v", t.Name, err)
	}
}

func (r *Runner) maybeNotify(t *store.Task, runID int64, logPath, status string, start, end time.Time, code int) {
	s := notify.Load(r.st)
	if !s.Enabled || s.Webhook == "" {
		return
	}
	if s.NotifyOn != "all" && status != "failed" {
		return
	}
	text := fmt.Sprintf("任务名称：%s\n运行编号：%d\n状态：%s\n开始时间：%s\n耗时：%s\n退出码：%d\n触发方式：调度器",
		t.Name, runID, statusText(status), start.Format(config.TimeLayout), end.Sub(start).Round(time.Millisecond), code)
	if tail := logTail(logPath, 600); tail != "" {
		text += "\n日志摘要：\n" + tail
	}
	title := "【PyScheduler】任务" + statusText(status)
	if _, err := notify.Send(s, title, text); err != nil {
		log.Printf("[notify] 发送失败: %v", err)
	}
}

func statusText(s string) string {
	switch s {
	case "success":
		return "成功"
	case "failed":
		return "失败"
	case "killed":
		return "已终止"
	default:
		return s
	}
}

func logTail(path string, n int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	s := string(b)
	if len(s) > n {
		s = s[len(s)-n:]
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
	}
	return strings.TrimRight(s, "\n")
}

// KillTask 终止任务全部运行中的进程树，返回终止数量。
func (r *Runner) KillTask(taskID int64) int {
	r.mu.Lock()
	var targets []*Proc
	for _, p := range r.procs {
		if p.taskID == taskID {
			targets = append(targets, p)
		}
	}
	r.mu.Unlock()
	for _, p := range targets {
		r.killProc(p)
	}
	return len(targets)
}

// KillRun 终止指定运行。
func (r *Runner) KillRun(runID int64) bool {
	r.mu.Lock()
	p, ok := r.procs[runID]
	r.mu.Unlock()
	if !ok {
		return false
	}
	r.killProc(p)
	return true
}

func (r *Runner) killProc(p *Proc) {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	if p.cmd.Process != nil {
		if err := killTree(p.cmd.Process.Pid); err != nil {
			log.Printf("[runner] 终止进程树失败 pid=%d: %v", p.cmd.Process.Pid, err)
		}
	}
}

func (r *Runner) killAll() {
	r.mu.Lock()
	targets := make([]*Proc, 0, len(r.procs))
	for _, p := range r.procs {
		targets = append(targets, p)
	}
	r.mu.Unlock()
	for _, p := range targets {
		r.killProc(p)
	}
}

// WaitAll 等待运行中的任务结束，超时后强制终止。
func (r *Runner) WaitAll(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		r.killAll()
	}
}

func buildCmd(command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.Command("cmd", "/c", command)
	}
	return exec.Command("sh", "-c", command)
}
