# PyScheduler · Python 定时任务管理平台（单机版 · Go 实现）

借鉴 [TaskPyro](https://docs.taskpyro.cn/) 的设计思路，用 **Go** 实现的轻量单机版定时任务管理平台：
**多虚拟环境隔离 + 项目（一项目一文件夹、支持拖拽上传）+ 定时调度 + 执行历史/实时日志 + 仪表盘 + 失败通知**。

技术栈：Go 标准库（`net/http` 路由 / `go:embed` 内嵌前端）+ [robfig/cron](https://github.com/robfig/cron) + [modernc.org/sqlite](https://gitlab.com/cznic/sqlite)（纯 Go、免 CGO）+ gopsutil 系统监控；前端为纯原生 HTML/CSS/JS 单页应用，随二进制内嵌，**单文件分发、无运行时依赖、无需 Node 构建**。

## 快速开始（GitHub Actions 打包）

项目通过 GitHub Actions 在云端构建，不在本地编译：

1. 将本仓库推送到 GitHub；
2. 打 tag 触发构建（或到仓库 Actions 页面手动 Run workflow）：

   ```
   git tag v1.0.0
   git push origin v1.0.0
   ```

3. 构建完成后，在 **Actions → Build → Artifacts** 下载对应平台的二进制；打了 tag 时也会自动挂到 **Releases**。

| 平台 | 产物 |
| --- | --- |
| Windows amd64 / arm64 | `pyscheduler.exe` |
| Linux amd64 / arm64 | `pyscheduler` |
| macOS amd64 / arm64 | `pyscheduler` |

4. 把二进制放到任意有写权限的目录，直接运行：

   ```
   .\pyscheduler.exe        # Windows
   ./pyscheduler            # Linux / macOS
   ```

浏览器访问 **http://127.0.0.1:8300** 。

- 修改端口/监听地址：设置环境变量 `PORT` / `HOST`（默认 `127.0.0.1:8300`；局域网访问设 `HOST=0.0.0.0`）。
- 首次启动自动在二进制所在目录创建 `data/ projects/ venvs/ pythons/ logs/`。
- 开机自启：用 Windows 任务计划程序在登录时运行，或用 NSSM 注册为系统服务。

> 若本机装有 Go 1.23+，也可以自行构建：`go mod tidy && go build`（交叉编译设置 `GOOS/GOARCH`，`CGO_ENABLED=0`）。

## 功能总览（与 TaskPyro 的对应关系）

| 模块 | 功能 |
| --- | --- |
| 仪表盘 | CPU/内存/磁盘监控、任务概况（活跃/异常/今日成功失败/24h 成功率）、近 7 天执行统计图 |
| 任务管理 | 四种调度方式：**立即执行 / 间隔执行 / 一次性 / Cron 表达式**；最大并发数（默认 1，上轮未跑满并发则跳过本次调度）；暂停/恢复/立即执行/强制终止；执行历史（触发方式、状态、耗时、退出码） |
| 项目管理 | 一个项目对应 `projects\` 下一个文件夹；支持**拖拽上传文件/整个文件夹**、点击选择上传、新建文件夹、目录浏览、面包屑导航；**点击文件名在线编辑**（纯文本编辑器，Ctrl+S 保存）；工作路径（workdir）决定脚本执行的工作目录；标签分类 |
| 环境管理 | 创建多个 Python 虚拟环境：**本机解释器**（自动发现）、**官方安装包**（python.org 安装包，华为云/中科大/npmmirror 国内镜像直连、静默安装，仅 Windows）或 **python-build-standalone**（GitHub，3.10~3.14，可配加速前缀；均带下载进度日志，运行时保留在 `pythons/` 可复用）；**依赖包可视化管理**（查看已装模块/版本、安装、卸载，pip 实时日志）；内置清华/阿里云/中科大等镜像源切换；一个环境可服务多个任务 |
| 运行日志 | 全部运行记录，按任务/状态/日期筛选，实时日志查看（自动刷新 + 关键词过滤） |
| 通知设置 | 任务失败（可选全部）推送 **钉钉 / 飞书 / 企业微信 / 自定义 webhook** |

核心机制：

- 任务命令是**任意命令行**（如 `python spider.py --date today`、`cd sub && python 1.py`），
  系统把所选虚拟环境的 `Scripts`（Windows）/ `bin`（Unix）目录放到 PATH 最前，命令中的 `python` 自动指向所选环境。
- 每次运行生成独立日志文件 `logs\tasks\<任务id>\<运行id>.log`，运行记录（状态/耗时/退出码）存 SQLite（纯 Go 驱动，免 CGO）。
- 服务重启后自动恢复所有「活跃中」任务的调度；`misfire_grace_time=60s + coalesce`，错过的调度点合并补跑一次。
- 终止任务按**进程树**强杀（Windows `taskkill /T`，Unix 进程组 `SIGKILL`），子进程不残留。

## 目录结构

```
pyscheduler/
├─ main.go                  启动入口（内嵌 static/，优雅退出）
├─ go.mod                   Go 依赖（cron / sqlite / gopsutil）
├─ .github/workflows/build.yml   GitHub Actions 多平台打包
├─ internal/
│  ├─ config/               目录/镜像源/常量
│  ├─ store/                SQLite 访问层（tasks/runs/envs/projects/settings/stats）
│  ├─ scheduler/           调度器（interval/date/cron、重启恢复、misfire 补跑）
│  ├─ runner/               子进程运行器（日志采集、进程树终止、失败通知）
│  ├─ envs/                 虚拟环境 + 在线下载 Python + pip 安装/卸载/列表
│  ├─ projects/             项目 + 文件上传/浏览/在线编辑（路径穿越防护）
│  ├─ notify/              webhook 通知（钉钉/飞书/企业微信/自定义）
│  └─ api/                  HTTP API（Go 1.22 方法+路径路由）
├─ static/                  前端（原生 HTML/CSS/JS 单页应用，go:embed 内嵌）
├─ data/                    scheduler.db（SQLite，运行时生成）
├─ projects/                用户项目（一项目一文件夹）
├─ venvs/                   用户虚拟环境
├─ pythons/                 在线下载的 Python 运行时（可复用）
└─ logs/                    运行日志（tasks/）与 环境日志（envs/）
```

## 使用建议（来自 TaskPyro 文档的实践）

- 依赖相近的任务共用一个虚拟环境，减少环境数量。
- 关键依赖锁定版本（如 `pandas==2.2.0`），避免兼容性问题。
- pip 慢时切换就近镜像源。
- 项目命名可带版本信息，善用标签筛选。

## 已知边界

- 单机单用户设计，无登录鉴权——请勿直接暴露到公网（如需远程访问，建议加反向代理鉴权或改绑定内网地址）。
- GUI「上传文件/文件夹」依赖浏览器原生文件选择与拖拽，已在 Chrome/Edge 常规环境验证；上传接口有完整的目录穿越防护与相对路径还原。
- 在线下载 Python 依赖 GitHub 连通性（断网时可用本机解释器；版本列表有内置兜底）。GitHub 不可达时可在「创建环境」弹窗配置**下载加速前缀**（ghproxy 形式，内置常用公共加速，支持自定义；阿里源/清华源仅镜像 pip 包，不适用于运行时下载）。下载的运行时在 `pythons/`，删除环境不会删除运行时。
- 在线编辑仅支持 UTF-8 文本、单个文件不超过 1MB。
- 任务无超时强制杀进程的配置（可后续在任务表加 `timeout` 字段扩展）。
