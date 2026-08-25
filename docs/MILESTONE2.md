# Milestone 2：工作区与沙箱安全策略

本里程碑在 Milestone 1 控制平面的基础上，交付了**工作区安全管理（WorkspaceManager）**与**沙箱资源/能力边界（SandboxManager）**，并补齐了附件上传、工作区文件浏览、中文 Writeup 读写与整包归档下载的 HTTP API。

---

## 1. 架构与包职责

```text
internal/workspace/      工作区路径规范化、目录遍历/符号链接逃逸防御、附件与 Writeup 管理、ZIP 导出
internal/sandbox/        六大题型隔离策略、CPU/内存/PID/能力约束、SYS_PTRACE 降级审计、禁止 Docker Socket 挂载
internal/agent/          生命周期编排（联动 Workspace 与 Sandbox）、FakeRunner 产出解题脚本与证据
internal/api/            本地 HTTP API（新增 /files、/file、/writeup、/download、/attachments）
internal/platform/       题型 SandboxPolicy、FileInfo、Writeup、SystemStatus 与新事件常量
```

---

## 2. 安全与隔离策略

### 2.1 工作区路径规范化与逃逸防御 (`WorkspaceManager`)

- **严格相对路径**：所有输入路径均经过 `filepath.Clean` 与分隔符统一化处理；
- **防目录遍历**：严厉拒绝包含 `..`、以 `.` 或 `..` 开头、以及绝对路径（如 `/etc/passwd`、`C:\Windows`）的请求；
- **防符号链接逃逸**：通过 `filepath.EvalSymlinks` 校验目标物理路径是否越过 `<workspace_root>/<task_id>` 边界；
- **大小限制与只读保护**：单文件读取限制 `10MB`，单次附件上传限制 `50MB`；任务在活动运行（running）状态时锁定附件上传，避免竞态覆盖。

### 2.2 专项沙箱安全策略 (`SandboxPolicy`)

每个 CTF 题型根据解题工具需求配置最小必要权限：

| 题型 (Category) | 默认镜像 | CPU 配额 | 内存限制 | PID 限制 | Capabilities | 网络模式 | 特殊审计 |
|---|---|---|---|---|---|---|---|
| **Web** | `ctf-agent-pi-web:0.1.0` | 2.0 核 | 1024 MB | 256 | `NET_BIND_SERVICE` | `bridge` (允许目标请求) | 无 |
| **Crypto** | `ctf-agent-pi-crypto:0.1.0` | 2.0 核 | 2048 MB | 128 | 无 | `none` (离线隔离) | 无 |
| **Pwn** | `ctf-agent-pi-pwn:0.1.0` | 2.0 核 | 1024 MB | 256 | `SYS_PTRACE` | `bridge` (网络交互) | 触发 `sandbox.degraded` 警告 |
| **Reverse** | `ctf-agent-pi-reverse:0.1.0` | 2.0 核 | 2048 MB | 256 | `SYS_PTRACE` | `none` (离线隔离) | 触发 `sandbox.degraded` 警告 |
| **Forensics** | `ctf-agent-pi-forensics:0.1.0` | 2.0 核 | 2048 MB | 256 | 无 | `none` (离线隔离) | 无 |
| **Misc** | `ctf-agent-pi-misc:0.1.0` | 2.0 核 | 1024 MB | 128 | 无 | `none` (离线隔离) | 无 |

### 2.3 安全红线强制执行

- **禁止挂载 Docker Socket**：拒绝包含 `docker.sock`、`/var/run/docker.sock`、`/proc`、`/sys`、`/etc/shadow` 的挂载；
- **默认丢弃所有特权**：`CapDrop: ["ALL"]`，从不使用 `--privileged`；
- **非特权默认用户**：容器内默认运行在 `ctf` 用户（UID 1000）；
- **工作区与技能只读分流**：工作区挂载为 `/workspace`（读写），参考资料库挂载为 `/opt/cpi/ctf-skills`（只读）。

---

## 3. HTTP 接口列表

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/health` | 无 | 服务健康状态 |
| GET | `/api/system` | Bearer | 系统概况、Docker 状态、工作区路径与六大题型策略 |
| GET/PUT | `/api/settings` | Bearer | 并发度等执行设置 |
| GET | `/api/model-usage` | Bearer | Token 用量统计 |
| GET/POST | `/api/tasks` | Bearer | 列表 / 创建题目（自动初始化工作区与模版 Writeup） |
| GET | `/api/tasks/{id}` | Bearer | 任务详情 |
| GET | `/api/tasks/{id}/events?after=&limit=` | Bearer | 事件增量拉取 |
| GET | `/api/tasks/{id}/stream?after=` | Bearer | SSE 实时事件流 |
| POST | `/api/tasks/{id}/start\|abort\|pause\|resume\|retry\|close-sandbox` | Bearer | 生命周期操作与沙箱控制 |
| POST | `/api/tasks/{id}/flag-feedback` | Bearer | Flag 人工确认 |
| **GET** | `/api/tasks/{id}/files` | Bearer | **[M2]** 递归获取任务工作区文件列表与元数据 |
| **GET** | `/api/tasks/{id}/file?path=...&raw=` | Bearer | **[M2]** 读取工作区内指定文件（JSON 或 Raw 流） |
| **GET** | `/api/tasks/{id}/writeup` | Bearer | **[M2]** 获取任务 WRITEUP.md 内容 |
| **PUT** | `/api/tasks/{id}/writeup` | Bearer | **[M2]** 保存/更新任务 WRITEUP.md |
| **GET** | `/api/tasks/{id}/download?path=...` | Bearer | **[M2]** 下载单个文件或导出整包工作区 ZIP |
| **POST** | `/api/tasks/{id}/attachments` | Bearer | **[M2]** Multipart/form-data 附件上传 |

---

## 4. 本地验证与冒烟测试

### 4.1 运行所有单元与集成测试

```bash
go test -mod=mod -v ./internal/...
```

### 4.2 编译与启动 daemon

```bash
go build -mod=mod -o bin/ctfagent-daemon ./cmd/ctfagent-daemon
./bin/ctfagent-daemon -data-dir ~/.ctf-btfly -addr 127.0.0.1:7521
```

### 4.3 端到端测试样例

```bash
TOKEN=$(cat ~/.ctf-btfly/daemon.token)
BASE=http://127.0.0.1:7521

# 1. 查看系统概况与沙箱策略
curl -s -H "Authorization: Bearer $TOKEN" $BASE/api/system

# 2. 创建题目（自动初始化工作区目录）
TASK_ID=$(curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"Pwn Chall 1","category":"pwn","prompt":"exploit buffer overflow"}' \
  $BASE/api/tasks | jq -r .id)

# 3. 上传题目附件
curl -s -H "Authorization: Bearer $TOKEN" \
  -F "file=@/path/to/chall.elf" \
  $BASE/api/tasks/$TASK_ID/attachments

# 4. 启动任务并观察沙箱事件
curl -s -X POST -H "Authorization: Bearer $TOKEN" $BASE/api/tasks/$TASK_ID/start
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/api/tasks/$TASK_ID/events?after=0"

# 5. 查看自动产出的脚本与 Writeup
curl -s -H "Authorization: Bearer $TOKEN" $BASE/api/tasks/$TASK_ID/files
curl -s -H "Authorization: Bearer $TOKEN" $BASE/api/tasks/$TASK_ID/writeup

# 6. 下载完整任务工作区 ZIP
curl -s -H "Authorization: Bearer $TOKEN" $BASE/api/tasks/$TASK_ID/download -o task_workspace.zip
```
