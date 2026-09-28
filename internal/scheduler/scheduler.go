// Package scheduler 定时调度器：interval / date / cron，重启恢复与 misfire 补跑。
package scheduler

import (
	"log"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"pyscheduler/internal/config"
	"pyscheduler/internal/runner"
	"pyscheduler/internal/store"
)

// Scheduler 管理各任务的调度循环。
type Scheduler struct {
	st *store.Store
	rn *runner.Runner

	mu      sync.Mutex
	stops   map[int64]chan struct{} // taskID -> stop
	cronTab map[int64]cron.Schedule // taskID -> 已解析的 cron/interval 调度（供 misfire 判断）
	wg      sync.WaitGroup
}

// New 创建调度器。
func New(st *store.Store, rn *runner.Runner) *Scheduler {
	return &Scheduler{st: st, rn: rn, stops: map[int64]chan struct{}{}, cronTab: map[int64]cron.Schedule{}}
}

// Start 服务启动时恢复所有「活跃中」任务的调度。
func (s *Scheduler) Start() {
	tasks, err := s.st.ListTasks()
	if err != nil {
		log.Printf("[scheduler] 读取任务失败: %v", err)
		return
	}
	now := time.Now()
	for _, t := range tasks {
		if !t.Enabled {
			continue
		}
		// misfire：错过但仍在宽限期内的调度点，合并补跑一次（coalesce）
		if sched := s.parseSchedule(t); sched != nil {
			if lf, err := time.ParseInLocation(config.TimeLayout, t.LastFire, time.Local); err == nil && !lf.IsZero() {
				missed := sched.Next(lf)
				if !missed.IsZero() && !missed.After(now) && now.Sub(missed) <= config.MisfireGrace {
					log.Printf("[scheduler] 任务[%s]补跑错过的一次调度（计划 %s）", t.Name, missed.Format(config.TimeLayout))
					_ = s.st.SetTaskLastFire(t.ID, missed)
					go s.rn.TriggerScheduled(t)
				}
			}
		}
		// date 任务：已过期则视作结束（宽限期内补跑一次，之后不再调度）
		if t.ScheduleType == "date" {
			if rt, err := time.ParseInLocation("2006-01-02T15:04", t.RunDate, time.Local); err == nil && !now.Before(rt) {
				if now.Sub(rt) <= config.MisfireGrace {
					log.Printf("[scheduler] date 任务[%s]补跑（计划 %s）", t.Name, t.RunDate)
					_ = s.st.SetTaskLastFire(t.ID, rt)
					go s.rn.TriggerScheduled(t)
					_ = s.st.SetTaskEnabled(t.ID, false)
				} else {
					log.Printf("[scheduler] date 任务[%s]计划时间已过期（%s），标记为暂停", t.Name, t.RunDate)
					_ = s.st.SetTaskEnabled(t.ID, false)
				}
				continue
			}
		}
		s.startLoop(t.ID)
	}
}

// parseSchedule 解析任务的 cron.Schedule（interval/cron）。
func (s *Scheduler) parseSchedule(t *store.Task) cron.Schedule {
	switch t.ScheduleType {
	case "interval":
		if t.IntervalSeconds <= 0 {
			return nil
		}
		return cron.Every(time.Duration(t.IntervalSeconds) * time.Second)
	case "cron":
		sched, err := cron.ParseStandard(t.CronExpr)
		if err != nil {
			return nil
		}
		return sched
	}
	return nil
}

// Sync 任务增删改/启停后同步调度循环。
func (s *Scheduler) Sync(taskID int64) {
	s.stopLoop(taskID)
	t, err := s.st.GetTask(taskID)
	if err != nil || t == nil {
		return
	}
	if !t.Enabled || t.ScheduleType == "manual" {
		return
	}
	s.startLoop(taskID)
}

func (s *Scheduler) startLoop(taskID int64) {
	stop := make(chan struct{})
	s.mu.Lock()
	if _, exists := s.stops[taskID]; exists {
		s.mu.Unlock()
		return
	}
	s.stops[taskID] = stop
	s.mu.Unlock()
	s.wg.Add(1)
	go s.runLoop(taskID, stop)
}

func (s *Scheduler) stopLoop(taskID int64) {
	s.mu.Lock()
	stop, exists := s.stops[taskID]
	if exists {
		delete(s.stops, taskID)
	}
	s.mu.Unlock()
	if exists {
		close(stop)
	}
}

// Stop 停止全部调度循环（不中断运行中的子进程）。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	ids := make([]int64, 0, len(s.stops))
	for id := range s.stops {
		ids = append(ids, id)
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.stopLoop(id)
	}
	s.wg.Wait()
}

func (s *Scheduler) runLoop(taskID int64, stop chan struct{}) {
	defer s.wg.Done()
	for {
		t, err := s.st.GetTask(taskID)
		if err != nil || t == nil || !t.Enabled {
			return
		}

		var next time.Time
		switch t.ScheduleType {
		case "interval", "cron":
			sched := s.parseSchedule(t)
			if sched == nil {
				log.Printf("[scheduler] 任务[%s]调度配置无效（%s），停止调度", t.Name, t.ScheduleType)
				return
			}
			next = sched.Next(time.Now())
		case "date":
			rt, err := time.ParseInLocation("2006-01-02T15:04", t.RunDate, time.Local)
			if err != nil {
				return
			}
			if !time.Now().Before(rt) {
				// 到点（含轻微延迟）：执行一次后结束
				_ = s.st.SetTaskLastFire(taskID, rt)
				s.rn.TriggerScheduled(t)
				_ = s.st.SetTaskEnabled(taskID, false)
				log.Printf("[scheduler] date 任务[%s]已触发，标记为暂停", t.Name)
				return
			}
			next = rt
		default: // manual
			return
		}

		timer := time.NewTimer(time.Until(next))
		select {
		case <-stop:
			timer.Stop()
			return
		case <-timer.C:
		}

		cur, err := s.st.GetTask(taskID)
		if err != nil || cur == nil || !cur.Enabled {
			return
		}
		_ = s.st.SetTaskLastFire(taskID, next)
		s.rn.TriggerScheduled(cur)
		if cur.ScheduleType == "date" {
			_ = s.st.SetTaskEnabled(taskID, false)
			log.Printf("[scheduler] date 任务[%s]已触发，标记为暂停", cur.Name)
			return
		}
	}
}
