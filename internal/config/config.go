// Package config 管理全局目录、镜像源与常量。
package config

import (
	"os"
	"path/filepath"
	"time"
)

// MaxEditSize 在线编辑支持的单个文件最大字节数（1MB）。
const MaxEditSize = 1 << 20

// MisfireGrace 错过调度点的补跑宽限期（与 APScheduler misfire_grace_time 对齐）。
const MisfireGrace = 60 * time.Second

// TimeLayout 数据库中统一使用的时间格式（本地时区）。
const TimeLayout = "2006-01-02 15:04:05"

var (
	BaseDir     string // 程序所在目录，所有数据目录的根
	DataDir     string // data/（SQLite 数据库）
	ProjectsDir string // projects/（用户项目）
	VenvsDir    string // venvs/（用户虚拟环境）
	PythonsDir  string // pythons/（在线下载的 Python 运行时，可复用）
	LogsDir     string // logs/
	TaskLogsDir string // logs/tasks/<任务id>/（运行日志）
	EnvLogsDir  string // logs/envs/（环境创建与 pip 操作日志）
)

// Mirror pip 镜像源。
type Mirror struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Mirrors 内置镜像源列表。
var Mirrors = []Mirror{
	{Name: "清华源", URL: "https://pypi.tuna.tsinghua.edu.cn/simple"},
	{Name: "阿里云", URL: "https://mirrors.aliyun.com/pypi/simple/"},
	{Name: "中科大", URL: "https://pypi.mirrors.ustc.edu.cn/simple/"},
	{Name: "腾讯云", URL: "https://mirrors.cloud.tencent.com/pypi/simple/"},
	{Name: "官方源", URL: "https://pypi.org/simple"},
}

// Init 计算并创建全部数据目录。
func Init() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	BaseDir = filepath.Dir(exe)
	DataDir = filepath.Join(BaseDir, "data")
	ProjectsDir = filepath.Join(BaseDir, "projects")
	VenvsDir = filepath.Join(BaseDir, "venvs")
	PythonsDir = filepath.Join(BaseDir, "pythons")
	LogsDir = filepath.Join(BaseDir, "logs")
	TaskLogsDir = filepath.Join(LogsDir, "tasks")
	EnvLogsDir = filepath.Join(LogsDir, "envs")
	for _, d := range []string{DataDir, ProjectsDir, VenvsDir, PythonsDir, TaskLogsDir, EnvLogsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
