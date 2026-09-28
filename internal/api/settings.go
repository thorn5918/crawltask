package api

import (
	"net/http"

	"pyscheduler/internal/notify"
)

func validNotifyType(t string) bool {
	switch t {
	case "dingtalk", "feishu", "wecom", "custom":
		return true
	}
	return false
}

func (s *Server) getNotify(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, notify.Load(s.st))
}

func (s *Server) saveNotify(w http.ResponseWriter, r *http.Request) {
	var p notify.Settings
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体解析失败："+err.Error())
		return
	}
	if !validNotifyType(p.Type) {
		writeErr(w, http.StatusBadRequest, "通知渠道不合法")
		return
	}
	if p.NotifyOn != "failed" && p.NotifyOn != "all" {
		p.NotifyOn = "failed"
	}
	if p.Enabled && p.Webhook == "" {
		writeErr(w, http.StatusBadRequest, "启用通知时 webhook 地址不能为空")
		return
	}
	if err := notify.Save(s.st, p); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) testNotify(w http.ResponseWriter, r *http.Request) {
	var p notify.Settings
	if err := readJSON(r, &p); err != nil { // 无请求体时用已保存配置测试
		p = notify.Load(s.st)
	}
	if p.Webhook == "" {
		writeErr(w, http.StatusBadRequest, "webhook 地址为空")
		return
	}
	if !validNotifyType(p.Type) {
		p.Type = "dingtalk"
	}
	body, err := notify.Send(p, "【PyScheduler】测试通知", "这是一条测试消息：通知渠道配置成功。")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error(), "response": body})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "response": body})
}
