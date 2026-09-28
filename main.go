// PyScheduler · Python 定时任务管理平台（Go 单文件版）
//
// 启动：pyscheduler[.exe]；HOST/PORT 环境变量控制监听地址（默认 127.0.0.1:8300）。
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"pyscheduler/internal/api"
	"pyscheduler/internal/config"
	"pyscheduler/internal/envs"
	"pyscheduler/internal/projects"
	"pyscheduler/internal/runner"
	"pyscheduler/internal/scheduler"
	"pyscheduler/internal/store"
)

//go:embed static
var staticFS embed.FS

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	if err := config.Init(); err != nil {
		log.Fatalf("初始化数据目录失败: %v", err)
	}

	host := getenv("HOST", "0.0.0.0")
	port := getenv("PORT", "8300")

	st, err := store.Open(filepath.Join(config.DataDir, "scheduler.db"))
	if err != nil {
		log.Fatalf("打开数据库失败: %v", err)
	}
	defer st.Close()

	rn := runner.New(st)
	sch := scheduler.New(st, rn)
	sch.Start() // 服务重启后自动恢复「活跃中」任务的调度

	envm := envs.New(st)
	projm := projects.New(st)
	apiSrv := api.New(st, sch, rn, envm, projm)

	mux := http.NewServeMux()
	mux.Handle("/api/", apiSrv.Routes())

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("静态资源加载失败: %v", err)
	}
	fileServer := http.FileServer(http.FS(staticSub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := staticSub.Open(p); err == nil {
				f.Close()
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		index, _ := fs.ReadFile(staticSub, "index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})

	addr := host + ":" + port
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		log.Printf("PyScheduler(Go) 已启动: http://%s:%s  （数据目录：%s）", displayHost(host), port, config.BaseDir)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP 服务失败: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit
	log.Println("正在停止……")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)

	sch.Stop()                 // 停止新调度
	rn.WaitAll(15 * time.Second) // 等待运行中的任务结束（超时后强制终止）
	log.Println("已退出")
}

func displayHost(host string) string {
	if host == "" || host == "::" {
		return "127.0.0.1"
	}
	return host
}
