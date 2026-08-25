# CTF-BTFly 实现分析与路线图

> 这份文档是项目重新进入可开发状态后的第一份工程记录。它区分了“从现有材料确认的事实”和“后续需要实现的设计”，避免把编译产物误当成源码。

## 1. 当前仓库审计

### 1.1 已有材料

| 材料 | 结论 |
| --- | --- |
| `README.md` | 产品目标、用户流程、隔离策略和技术栈说明。核心闭环是：创建题目 → 建立沙箱 → Agent 解题 → 观察/接管 → Flag 审核 → Writeup 归档。 |
| `CTF-BTFly.exe` | Windows PE x64 的 Go/Wails 桌面程序，资源中包含 React 前端构建产物；嵌入页面标题为 CTF-BTFly，版本资源显示为 1.3.1。它只能作为行为参考，不能替代可维护源码。 |
| `ctfagent-daemon.exe` | Windows PE x64 的 Go daemon 行为参考。符号和字符串显示它使用 Docker SDK、SQLite、HTTP/WebSocket、Pi JSONL RPC、模型网关和事件持久化。 |
| `images/*/Dockerfile` | 六个方向镜像的目标工具集和运行时策略。基础镜像默认以非特权 `ctf` 用户运行。 |
| `SecurityChecker.class` | 独立的 MySQL JDBC 参数安全检查器，与 CTF 解题主流程没有直接依赖；暂不把它纳入 daemon。 |

### 1.2 已发现的阻塞点

1. **源码缺失**：仓库当前只有编译产物，没有 Go、Wails、React 或 Agent 源码。编译产物中的源码路径指向另一个本地工作区，不能据此恢复完整实现。
2. **镜像构建上下文不完整**：`images/base/Dockerfile` 会复制 `agents/common` 和 `skills`，专项镜像会复制 `agents/<profile>` 与 `skills/<profile>`，但这些目录原先不存在，`docker build` 会在 `COPY` 阶段失败。
3. **镜像版本参数不一致**：专项 Dockerfile 原先把基础镜像写死为 `ctf-agent-pi-base:0.1.0`，当构建脚本传入其它版本时会找不到正确的基础镜像。
4. **根目录 `build.ps1` 已损坏**：文件包含乱码且仍按旧目录结构查找 `base/Dockerfile`；真实入口应是 `images/build.ps1`。
5. **安全策略还没有落到控制平面**：镜像只能表达默认用户，CPU/内存/PID/能力、网络和 runtime 选择必须由 daemon 在创建容器时强制执行，不能依赖 Agent 自觉。

本次初始化已经补齐镜像所需的规则/资料目录，并修正两个构建脚本和基础镜像版本传递；没有删除或覆盖现有二进制材料。

## 2. 从 daemon 产物确认的边界契约

下面的内容来自 `ctfagent-daemon.exe` 中可识别的包名、路由、数据库 DDL 和前端调用，不代表已经有可编译源码。

### 2.1 分层

```text
Wails + React Desktop
        │ local authenticated HTTP / WebSocket
        ▼
Go Control Plane (daemon)
 ├─ task service / lifecycle and queue
 ├─ SQLite storage and append-only event log
 ├─ sandbox manager (Docker + runtime policy)
 ├─ Pi RPC session bridge (stdin/stdout JSONL)
 ├─ model gateway (OpenAI-compatible proxy + usage capture)
 ├─ event hub (live subscriptions)
 └─ host/system statistics
        │
        ├─ one workspace per task
        └─ one container/session per task
```

桌面端应保持为薄客户端：任务状态、恢复、权限和最终写入都由 daemon 决定。这样 UI 关闭或重启时，后台任务仍能继续，重新连接后也能从事件序列恢复。

### 2.2 任务状态机（第一版）

```text
queued ──▶ provisioning ──▶ running ──▶ settled
   │             │             │  ▲
   │             └─────────────┘  │
   │                             │
   ├─▶ cancelled                  └─▶ paused ──▶ running
   └─▶ failed                         │
                                      └─▶ cancelled
```

`delegating` 表示主 Agent 正在等待子 Agent；它属于可恢复的活动状态。任何状态迁移都必须同时写入任务快照和事件记录，不能只更新 UI。

### 2.3 已观察到的 HTTP/WS 接口

接口命名以现有前端调用为准，后续实现应把请求/响应结构放到可版本化的 contract 包中：

- `GET /health`
- `GET /api/system`
- `POST /api/system/model-probe`
- `GET|PUT /api/settings`
- `GET|PUT /api/models/config`
- `DELETE /api/models/config/{profile}`
- `GET /api/model-usage`
- `GET|POST /api/tasks`
- `GET /api/tasks/{id}/subtasks`
- `GET /api/tasks/{id}/events?after=N`
- `GET /api/tasks/{id}/files`
- `GET /api/tasks/{id}/file?path=...`
- `GET /api/tasks/{id}/writeup`
- `GET /api/tasks/{id}/download?path=...`
- `POST /api/tasks/{id}/attachments`（multipart）
- `GET|PUT /api/tasks/{id}/prompt`
- `POST /api/tasks/{id}/start|abort|pause|resume|retry|close-sandbox`
- `POST /api/tasks/{id}/flag-feedback`
- `WS /ws/tasks/{id}?after=N&token=...`

本地 API 也必须做 token 校验；WebSocket 不能因为没有浏览器的标准 `Authorization` header 就放弃认证。

### 2.4 SQLite 最小模型

现有产物中至少包含以下表：

- `tasks`：题目基本信息、方向、提示词、模型、状态、镜像、runtime、container ID、错误和时间。
- `task_events`：按 `(task_id, sequence)` 唯一递增的事件流，保存 source、event type、turn/tool ID 和 JSON payload。
- `model_usage`：按请求记录 input/cached/output/reasoning/total tokens、延迟、状态码和是否上报。
- `app_settings`：执行并发度等全局设置。

后续黑板数据不要直接塞进事件 payload 后就无法查询；在确认多 Agent 需求后再增加事实、猜想、证据、工作项和 Flag 候选的规范化表，并保留事件作为审计记录。

## 3. 实施顺序

### Milestone 0：可重复构建（本次）

- 修复 PowerShell 入口和镜像版本传递。
- 提供公共 Agent 规则以及六个方向的最小 Skill 资料。
- 明确目录边界、安全原则和第一版契约。

### Milestone 1：无 Docker 也可测试的 daemon 核心 ✅

- ✅ 建立 Go module 和 `internal/platform`：题型、任务状态、请求/响应、事件类型、ID 规则。
- ✅ 建立 `internal/storage`：迁移、任务 CRUD、事件追加/分页、用量统计与设置。
- ✅ 建立 `internal/eventhub`：按任务订阅、断线后使用 `after` 序号补齐。
- ✅ FakeRunner + `internal/agent` 生命周期（start/pause/resume/abort/retry/close-sandbox/flag-feedback）。
- ✅ 本地 HTTP API（`internal/api`）与 `cmd/ctfagent-daemon` 入口。
- 细节见 [`docs/MILESTONE1.md`](MILESTONE1.md)。
### Milestone 2：沙箱与工作区

- `WorkspaceManager` 只接受规范化相对路径；拒绝 `..`、绝对路径、符号链接逃逸和重复覆盖。
- `SandboxManager` 统一设置只读/读写挂载、CPU、内存、PID、超时、网络、capabilities、用户和 runtime。
- 默认不挂载 Docker Socket、不使用 `--privileged`；Pwn 和动态 Reverse 使用显式策略，并在系统状态中报告降级。
- 所有 stdout/stderr、退出码和容器 ID 写入事件。

### Milestone 3：Pi RPC 与模型网关

- 定义 JSONL 输入/输出 framing、最大消息、取消和重启恢复策略。
- 以 session directory 保存 Agent 上下文；daemon 负责注入模型名、任务提示词和已授权工具。
- 模型 API Key 仅存本机配置，日志/事件/UI 只展示脱敏值。
- 统一捕获非流式和流式 usage，并将请求失败区分为可重试/不可重试。

### Milestone 4：桌面工作台

- 先实现“系统概况 + 新建题目 + 题目工作区 + 时间线 + Flag 审核”五个页面。
- 用 OpenAPI/TypeScript 生成或校验 client，禁止各页面手写不一致的请求结构。
- 事件列表按序号增量合并，最多保留有限窗口，历史通过分页加载。
- 最后加入模型配置、用量、MCP 和黑板多模型，避免首个版本被复杂功能拖垮。

## 4. 第一条可验收的竖切片

推荐下一步先实现一个**Fake Agent 模式**，不需要 Docker 或真实模型就能验收：

1. 创建一个 Web/Crypto/Pwn 等题目并上传附件。
2. daemon 创建工作区和任务事件 `task.created`。
3. 点击启动后进入 `queued → provisioning → running`。
4. fake agent 产生一条工具事件、一条输出事件和一个可疑 Flag 候选。
5. UI/HTTP 客户端实时收到事件；断开后用 `after` 恢复，不重复、不丢失。
6. 操作员接受或拒绝候选；接受后任务暂停或稳定，继续运行不会重复消耗。
7. 生成可复现的 `WRITEUP.md`，然后关闭沙箱并保留归档。

这条切片先验证最重要的“可观察、可恢复、可人工接管”能力，再接入真正的 Pi、Docker 和模型服务。

## 5. 明确的安全红线

- 不执行题目附件中的宿主机脚本；所有题目命令只在沙箱内运行。
- 不让 Agent 直接获得 Docker Socket、宿主机文件系统或任意宿主机插件权限。
- 不用 `--privileged` 绕过问题；需要调试能力时只授予最小的显式 capability，并显示警告。
- 文件下载、预览和上传都限制在任务工作区内，并设置大小/数量/压缩炸弹限制。
- Flag 检测只产生候选，最终结果永远由用户确认；日志中避免泄露模型密钥。
- MCP/宿主工具权限在任务启动时冻结，运行中不隐式扩大。

## 6. 暂不做的事情

首个可用版本暂不承诺远程集群、多用户 RBAC、自动向比赛平台提交 Flag、全量 MCP 生态和复杂黑板调度。这些都应建立在本地单用户生命周期、事件恢复和沙箱安全已经稳定的基础上。
