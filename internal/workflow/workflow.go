// Package workflow 流程编排引擎：多个任务按顺序执行，支持 Cron 定时触发与失败续跑策略。
package workflow

import (
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"pyscheduler/internal/config"
	"pyscheduler/internal/notify"
	"pyscheduler/internal/runner"
	"pyscheduler/internal/store"
)

// ErrRunning 流程已有运行中的实例。
var ErrRunning = errors.New("流程已有运行中的实例")

const (
	pollInterval = 500 * time.Millisecond // 步骤完成轮询间隔
	busyRetry    = 2 * time.Second        // 任务并发已满时的重试间隔
)

// Engine 管理流程的定时调度与顺序执行。
type Engine struct {
	st *store.Store
	rn *runner.Runner

	mu    sync.Mutex
	stops map[int64]chan struct{} // workflowID -> 调度循环停止信号
	kills map[int64]chan struct{} // workflowID -> 当前运行实例的终止信号
	wg    sync.WaitGroup
}

// New 创建流程引擎。
func New(st *store.Store, rn *runner.Runner) *Engine {
	return &Engine{st: st, rn: rn, stops: map[int64]chan struct{}{}, kills: map[int64]chan struct{}{}}
}

// Start 服务启动时：清理遗留的运行实例、恢复启用了定时的流程调度、宽限期内补跑错过的调度点。
func (e *Engine) Start() {
	if err := e.st.FailStaleWorkflowRuns(); err != nil {
		log.Printf("[workflow] 清理遗留运行实例失败: %v", err)
	}
	wfs, err := e.st.ListWorkflows()
	if err != nil {
		log.Printf("[workflow] 读取流程失败: %v", err)
		return
	}
	now := time.Now()
	for _, wf := range wfs {
		if !wf.Enabled || wf.CronExpr == "" {
			continue
		}
		sched, err := cron.ParseStandard(wf.CronExpr)
		if err != nil {
			log.Printf("[workflow] 流程[%s] Cron 无效（%s），跳过调度", wf.Name, wf.CronExpr)
			continue
		}
		if lf, err := time.ParseInLocation(config.TimeLayout, wf.LastFire, time.Local); err == nil && !lf.IsZero() {
			missed := sched.Next(lf)
			if !missed.IsZero() && !missed.After(now) && now.Sub(missed) <= config.MisfireGrace {
				log.Printf("[workflow] 流程[%s]补跑错过的一次调度（计划 %s）", wf.Name, missed.Format(config.TimeLayout))
				_ = e.st.SetWorkflowLastFire(wf.ID, missed)
				e.TriggerScheduled(wf)
			}
		}
		e.startLoop(wf.ID)
	}
}

// Stop 停止全部调度循环并终止运行中的流程实例。
func (e *Engine) Stop() {
	e.mu.Lock()
	stops := make([]chan struct{}, 0, len(e.stops))
	for id, ch := range e.stops {
		stops = append(stops, ch)
		delete(e.stops, id)
	}
	kills := make([]chan struct{}, 0, len(e.kills))
	for id, ch := range e.kills {
		kills = append(kills, ch)
		delete(e.kills, id)
	}
	e.mu.Unlock()
	for _, ch := range stops {
		close(ch)
	}
	for _, ch := range kills {
		close(ch)
	}
	e.wg.Wait()
}

// Sync 流程增删改/启停后同步调度循环。
func (e *Engine) Sync(wfID int64) {
	e.stopLoop(wfID)
	wf, err := e.st.GetWorkflow(wfID)
	if err != nil || wf == nil || !wf.Enabled || wf.CronExpr == "" {
		return
	}
	if _, err := cron.ParseStandard(wf.CronExpr); err != nil {
		log.Printf("[workflow] 流程[%s] Cron 无效（%s），不启动调度", wf.Name, wf.CronExpr)
		return
	}
	e.startLoop(wfID)
}

// Kill 终止流程当前运行中的实例（终止正在执行的步骤进程，剩余步骤标记跳过）。
// 返回是否存在运行中的实例。
func (e *Engine) Kill(wfID int64) bool {
	e.mu.Lock()
	k, ok := e.kills[wfID]
	if ok {
		delete(e.kills, wfID)
	}
	e.mu.Unlock()
	if !ok {
		return false
	}
	close(k)
	return true
}

// releaseKill 仅当映射仍指向自己的信号时才清除（避免误删后续实例的信号）。
func (e *Engine) releaseKill(wfID int64, kill <-chan struct{}) {
	e.mu.Lock()
	if cur, ok := e.kills[wfID]; ok && cur == kill {
		delete(e.kills, wfID)
	}
	e.mu.Unlock()
}

func (e *Engine) startLoop(wfID int64) {
	stop := make(chan struct{})
	e.mu.Lock()
	if _, exists := e.stops[wfID]; exists {
		e.mu.Unlock()
		return
	}
	e.stops[wfID] = stop
	e.mu.Unlock()
	e.wg.Add(1)
	go e.runLoop(wfID, stop)
}

func (e *Engine) stopLoop(wfID int64) {
	e.mu.Lock()
	stop, exists := e.stops[wfID]
	if exists {
		delete(e.stops, wfID)
	}
	e.mu.Unlock()
	if exists {
		close(stop)
	}
}

func (e *Engine) runLoop(wfID int64, stop chan struct{}) {
	defer e.wg.Done()
	for {
		wf, err := e.st.GetWorkflow(wfID)
		if err != nil || wf == nil || !wf.Enabled || wf.CronExpr == "" {
			return
		}
		sched, err := cron.ParseStandard(wf.CronExpr)
		if err != nil {
			log.Printf("[workflow] 流程[%s] Cron 无效（%s），停止调度", wf.Name, wf.CronExpr)
			return
		}
		next := sched.Next(time.Now())
		timer := time.NewTimer(time.Until(next))
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
		}

		cur, err := e.st.GetWorkflow(wfID)
		if err != nil || cur == nil || !cur.Enabled {
			return
		}
		_ = e.st.SetWorkflowLastFire(wfID, next)
		e.TriggerScheduled(cur)
	}
}

// TriggerScheduled 调度触发：已有实例运行中则跳过（coalesce 语义）。
func (e *Engine) TriggerScheduled(wf *store.Workflow) {
	if err := e.Trigger(wf, "schedule"); err != nil {
		if errors.Is(err, ErrRunning) {
			log.Printf("[workflow] 流程[%s]上一实例未结束，跳过本次调度", wf.Name)
		} else {
			log.Printf("[workflow] 流程[%s]触发失败: %v", wf.Name, err)
		}
	}
}

// Trigger 创建流程运行实例并开始顺序执行各步骤。
func (e *Engine) Trigger(wf *store.Workflow, trigger string) error {
	if running, err := e.st.HasRunningWorkflowRun(wf.ID); err != nil {
		return err
	} else if running {
		return ErrRunning
	}

	stepRuns := e.snapshotSteps(wf.ID, store.ParseWorkflowSteps(wf.Steps))
	run := &store.WorkflowRun{
		WorkflowID: wf.ID, WorkflowName: wf.Name,
		Trigger: trigger, Steps: marshalSteps(stepRuns),
	}
	runID, err := e.st.CreateWorkflowRun(run)
	if err != nil {
		return err
	}

	kill := make(chan struct{})
	e.mu.Lock()
	e.kills[wf.ID] = kill
	e.mu.Unlock()
	e.wg.Add(1)
	go e.execRun(runID, wf.ID, wf.Name, stepRuns, kill)
	return nil
}

// snapshotSteps 生成步骤快照（从本流程的步骤任务取名称，任务缺失时标记错误）。
func (e *Engine) snapshotSteps(wfID int64, steps []store.WorkflowStep) []store.WorkflowStepRun {
	tasks, _ := e.st.ListTasksByWorkflow(wfID)
	names := map[int64]string{}
	for _, t := range tasks {
		names[t.ID] = t.Name
	}
	out := make([]store.WorkflowStepRun, 0, len(steps))
	for _, st := range steps {
		sr := store.WorkflowStepRun{
			TaskID:            st.TaskID,
			TaskName:          names[st.TaskID],
			ContinueOnFailure: st.ContinueOnFailure,
			Status:            "pending",
		}
		if sr.TaskName == "" {
			sr.Error = "任务不存在或已删除"
		}
		out = append(out, sr)
	}
	return out
}

func marshalSteps(stepRuns []store.WorkflowStepRun) string {
	if stepRuns == nil {
		stepRuns = []store.WorkflowStepRun{}
	}
	b, err := json.Marshal(stepRuns)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// execRun 顺序执行各步骤：失败按 continue_on_failure 决定中断或续跑；kill 信号随时整体终止。
func (e *Engine) execRun(runID, wfID int64, wfName string, stepRuns []store.WorkflowStepRun, kill <-chan struct{}) {
	defer e.wg.Done()
	defer e.releaseKill(wfID, kill)

	start := time.Now()
	overall := "success"

	for i := 0; i < len(stepRuns); i++ {
		// 整体终止：当前及之后步骤全部跳过
		select {
		case <-kill:
			for j := i; j < len(stepRuns); j++ {
				stepRuns[j].Status = "skipped"
			}
			overall = "killed"
			goto finish
		default:
		}

		stepRuns[i].StartTime = nowLocal()
		if stepRuns[i].Error != "" { // 任务不存在（快照阶段已发现）
			stepRuns[i].Status = "failed"
		} else {
			stepRuns[i].Status = "running"
			_ = e.st.UpdateWorkflowRunSteps(runID, marshalSteps(stepRuns))
			e.runStep(&stepRuns[i], kill)
		}

		_ = e.st.UpdateWorkflowRunSteps(runID, marshalSteps(stepRuns))

		// 整体终止信号优先于步骤级失败策略：kill 时无论 continue_on_failure 都停止
		select {
		case <-kill:
			for j := i + 1; j < len(stepRuns); j++ {
				stepRuns[j].Status = "skipped"
			}
			overall = "killed"
			goto finish
		default:
		}

		switch stepRuns[i].Status {
		case "failed", "killed":
			if !stepRuns[i].ContinueOnFailure {
				for j := i + 1; j < len(stepRuns); j++ {
					stepRuns[j].Status = "skipped"
				}
				overall = "failed"
				goto finish
			}
			overall = "partial"
		}
	}

finish:
	end := time.Now()
	_ = e.st.FinishWorkflowRun(runID, overall, end.Format(config.TimeLayout),
		end.Sub(start).Milliseconds(), marshalSteps(stepRuns))
	e.maybeNotify(wfName, runID, overall, start, end, stepRuns)
}

func nowLocal() string { return time.Now().Format(config.TimeLayout) }

// runStep 执行单个步骤：启动任务进程并等待其结束（任务并发已满时等待重试，保证顺序语义）。
func (e *Engine) runStep(step *store.WorkflowStepRun, kill <-chan struct{}) {
	task, err := e.st.GetTask(step.TaskID)
	if err != nil {
		step.Status = "failed"
		step.Error = "任务不存在或已删除"
		return
	}
	step.TaskName = task.Name

	var runID int64
	for {
		runID, err = e.rn.StartRun(task, "workflow")
		if err == nil {
			break
		}
		if !errors.Is(err, runner.ErrBusy) {
			step.Status = "failed"
			step.Error = "启动失败：" + err.Error()
			return
		}
		log.Printf("[workflow] 流程步骤[%s]并发已满，等待重试", task.Name)
		select {
		case <-kill:
			step.Status = "killed"
			return
		case <-time.After(busyRetry):
		}
	}
	step.RunID = runID

	// 轮询等待任务运行结束
	for {
		select {
		case <-kill:
			e.rn.KillRun(runID)
			step.Status = "killed"
			step.EndTime = nowLocal()
			if r, err := e.st.GetRun(runID); err == nil {
				step.DurationMS = r.DurationMS
				step.ExitCode = r.ExitCode
			}
			return
		case <-time.After(pollInterval):
		}
		r, err := e.st.GetRun(runID)
		if err != nil {
			step.Status = "failed"
			step.Error = "运行记录读取失败：" + err.Error()
			return
		}
		if r.Status == "running" {
			continue
		}
		step.Status = r.Status
		step.EndTime = r.EndTime
		step.DurationMS = r.DurationMS
		step.ExitCode = r.ExitCode
		if r.Status == "failed" {
			step.Error = "退出码 " + itoaPtr(r.ExitCode)
		}
		return
	}
}

func itoaPtr(code *int) string {
	if code == nil {
		return "未知"
	}
	return strconv.Itoa(*code)
}

// maybeNotify 流程结束推送通知（复用通知设置：失败/部分成功推送，notify_on=all 时全部推送）。
func (e *Engine) maybeNotify(wfName string, runID int64, status string, start, end time.Time, steps []store.WorkflowStepRun) {
	s := notify.Load(e.st)
	if !s.Enabled || s.Webhook == "" {
		return
	}
	if s.NotifyOn != "all" && status == "success" {
		return
	}
	text := "流程名称：" + wfName +
		"\n运行编号：" + strconv.FormatInt(runID, 10) +
		"\n状态：" + StatusText(status) +
		"\n开始时间：" + start.Format(config.TimeLayout) +
		"\n耗时：" + end.Sub(start).Round(time.Millisecond).String() +
		"\n步骤详情："
	for _, st := range steps {
		text += "\n· " + st.TaskName + "：" + StepStatusText(st.Status)
	}
	title := "【PyScheduler】流程" + StatusText(status)
	if _, err := notify.Send(s, title, text); err != nil {
		log.Printf("[notify] 流程通知发送失败: %v", err)
	}
}

// StatusText 流程整体状态中文描述。
func StatusText(s string) string {
	switch s {
	case "running":
		return "运行中"
	case "success":
		return "成功"
	case "partial":
		return "部分成功"
	case "failed":
		return "失败"
	case "killed":
		return "已终止"
	}
	return s
}

// StepStatusText 步骤状态中文描述。
func StepStatusText(s string) string {
	switch s {
	case "pending":
		return "等待中"
	case "running":
		return "运行中"
	case "success":
		return "成功"
	case "failed":
		return "失败"
	case "killed":
		return "已终止"
	case "skipped":
		return "已跳过"
	}
	return s
}
