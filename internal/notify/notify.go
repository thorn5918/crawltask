// Package notify 实现 webhook 失败通知（钉钉/飞书/企业微信/自定义）。
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"pyscheduler/internal/store"
)

// Settings 通知配置（存于 settings 表 key=notify）。
type Settings struct {
	Enabled  bool   `json:"enabled"`
	Type     string `json:"type"`      // dingtalk | feishu | wecom | custom
	Webhook  string `json:"webhook"`
	NotifyOn string `json:"notify_on"` // failed | all
}

// DefaultSettings 默认配置。
func DefaultSettings() Settings {
	return Settings{Enabled: false, Type: "dingtalk", NotifyOn: "failed"}
}

// Load 从数据库读取通知配置。
func Load(st *store.Store) Settings {
	v, err := st.GetSetting("notify")
	if err != nil || v == "" {
		return DefaultSettings()
	}
	var s Settings
	if err := json.Unmarshal([]byte(v), &s); err != nil {
		return DefaultSettings()
	}
	return s
}

// Save 保存通知配置。
func Save(st *store.Store, s Settings) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return st.SetSetting("notify", string(b))
}

// Send 按渠道推送文本消息，返回错误及响应体摘要。
func Send(s Settings, title, text string) (string, error) {
	if s.Webhook == "" {
		return "", fmt.Errorf("webhook 地址为空")
	}
	full := title + "\n" + text
	var body []byte
	switch s.Type {
	case "dingtalk":
		body, _ = json.Marshal(map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": full},
		})
	case "feishu":
		body, _ = json.Marshal(map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": full},
		})
	case "wecom":
		body, _ = json.Marshal(map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": full},
		})
	default: // custom
		body, _ = json.Marshal(map[string]any{"title": title, "text": text})
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(s.Webhook, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 300 {
		return string(b), fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(b), nil
}
