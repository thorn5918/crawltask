package api

import (
	"net/http"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"

	"pyscheduler/internal/config"
)

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{}

	if percents, err := cpu.Percent(0, false); err == nil && len(percents) > 0 {
		resp["cpu_percent"] = percents[0]
	} else {
		resp["cpu_percent"] = 0.0
	}

	if m, err := mem.VirtualMemory(); err == nil {
		resp["mem"] = map[string]any{"used": m.Used, "total": m.Total, "percent": m.UsedPercent}
	} else {
		resp["mem"] = map[string]any{"used": 0, "total": 0, "percent": 0.0}
	}

	if d, err := disk.Usage(config.BaseDir); err == nil {
		resp["disk"] = map[string]any{"used": d.Used, "total": d.Total, "percent": d.UsedPercent, "path": d.Path}
	} else {
		resp["disk"] = map[string]any{"used": 0, "total": 0, "percent": 0.0, "path": config.BaseDir}
	}

	stats, err := s.st.DashboardStats()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	stats.Running = s.rn.RunningTotal()
	resp["tasks"] = stats

	// 异常任务：最近一次运行失败/被终止的任务数
	failed := 0
	if last, err := s.st.LastRunPerTask(); err == nil {
		for _, run := range last {
			if run.Status == "failed" || run.Status == "killed" {
				failed++
			}
		}
	}
	resp["failed_tasks"] = failed

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) chart(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 30 {
		days = 30
	}
	stats, err := s.st.DailyStats(days)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, days)
	now := time.Now()
	for i := days - 1; i >= 0; i-- {
		day := now.AddDate(0, 0, -i)
		key := day.Format("01-02")
		st := stats[key]
		out = append(out, map[string]any{
			"date":    key,
			"success": st.Success,
			"failed":  st.Failed,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"days": out})
}
