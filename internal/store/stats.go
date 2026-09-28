package store

import "strconv"

// DayStat 按天统计。
type DayStat struct {
	Date    string `json:"date"` // MM-DD
	Success int    `json:"success"`
	Failed  int    `json:"failed"` // 含 killed
}

// DailyStats 最近 days 天的执行统计（不补零，由调用方补齐）。
func (s *Store) DailyStats(days int) (map[string]DayStat, error) {
	rows, err := s.db.Query(`SELECT substr(start_time, 6, 5) AS d,
			SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),
			SUM(CASE WHEN status IN ('failed','killed') THEN 1 ELSE 0 END)
		FROM runs
		WHERE start_time >= date('now', 'localtime', ?) || ' 00:00:00'
		GROUP BY d`, "-"+strconv.Itoa(days-1)+" days")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]DayStat{}
	for rows.Next() {
		var d DayStat
		var ok, fail int
		if err := rows.Scan(&d.Date, &ok, &fail); err != nil {
			return nil, err
		}
		d.Success, d.Failed = ok, fail
		out[d.Date] = d
	}
	return out, rows.Err()
}

// OverviewTasks 仪表盘任务统计。
type OverviewTasks struct {
	Total     int     `json:"total"`
	Active    int     `json:"active"`
	Running   int     `json:"running"`
	TodayOK   int     `json:"today_ok"`
	TodayFail int     `json:"today_fail"`
	Rate24h   float64 `json:"rate_24h"` // 0~100，无记录时为 -1
}

// DashboardStats 汇总仪表盘统计。
func (s *Store) DashboardStats() (OverviewTasks, error) {
	var o OverviewTasks
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(enabled),0) FROM tasks`).Scan(&o.Total, &o.Active); err != nil {
		return o, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE status='running'`).Scan(&o.Running); err != nil {
		return o, err
	}
	if err := s.db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status IN ('failed','killed') THEN 1 ELSE 0 END),0)
		FROM runs WHERE start_time >= date('now','localtime') || ' 00:00:00'`).Scan(&o.TodayOK, &o.TodayFail); err != nil {
		return o, err
	}
	var ok, fail int
	if err := s.db.QueryRow(`SELECT
			COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END),0),
			COALESCE(SUM(CASE WHEN status IN ('failed','killed') THEN 1 ELSE 0 END),0)
		FROM runs WHERE start_time >= datetime('now','localtime','-24 hours')`).Scan(&ok, &fail); err != nil {
		return o, err
	}
	if ok+fail > 0 {
		o.Rate24h = float64(ok) * 100 / float64(ok+fail)
	} else {
		o.Rate24h = -1
	}
	return o, nil
}
