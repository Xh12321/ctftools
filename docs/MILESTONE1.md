# Milestone 1：无 Docker 可测试的 daemon 核心

本里程碑交付可编译、可单测的 Go 控制平面骨架，用 **Fake Agent** 跑通完整任务生命周期，不依赖 Docker、Pi RPC 或真实模型。

## 包结构

```text
cmd/ctfagent-daemon/     进程入口
internal/platform/       题型、状态机、事件常量、DTO、ID
internal/storage/        SQLite 迁移、任务 CRUD、事件追加/分页、用量、设置
internal/eventhub/       按任务订阅；Publish 不阻塞；after 过滤
internal/agent/          生命周期编排 + FakeRunner
internal/api/            本地 HTTP API（Bearer token）
internal/daemon/         组装 data dir / token / store / hub / API
vendor/                  离线依赖（uuid、ncruces sqlite、wazero）
```

## 任务状态机

```text
queued → provisioning → running → settled
                │           │  ▲
                └───────────┘  │
                               └─ paused ⇄ running
queued/running/paused → cancelled
running → failed → (retry) → queued → …
```

任意状态迁移都会：

1. 更新 `tasks` 快照；
2. 追加 `task_events`（`sequence` 单调递增）；
3. 通过 `eventhub` 推送给在线订阅者。

## Fake Agent 竖切片

1. `POST /api/tasks` 创建题目 → 事件 `task.created` / `task.queued`
2. `POST /api/tasks/{id}/start` → `sandbox.provisioning` → `sandbox.started` → `agent.started` → `running`
3. FakeRunner 产生工具事件 + 消息 + `flag.candidate`
4. `GET /api/tasks/{id}/events?after=N` 增量拉取，不重复、不丢失
5. `POST /api/tasks/{id}/flag-feedback`（accept）→ 若仍在运行则 `paused`
6. `POST /api/tasks/{id}/close-sandbox` 清理容器元数据

## 本地运行

```bash
# 测试
go test -mod=mod ./internal/...

# 构建
go build -mod=mod -o bin/ctfagent-daemon ./cmd/ctfagent-daemon

# 启动（默认 127.0.0.1:7521，数据目录 ~/.ctf-btfly）
./bin/ctfagent-daemon

# 或自定义
./bin/ctfagent-daemon -data-dir /tmp/ctf-data -addr 127.0.0.1:7521 -token devtoken
```

### 手动冒烟

```bash
TOKEN=$(cat ~/.ctf-btfly/daemon.token)
BASE=http://127.0.0.1:7521

curl -s $BASE/health

curl -s -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"title":"demo","category":"web","prompt":"find flag","flagFormat":"flag{...}"}' \
  $BASE/api/tasks

# 记下返回的 id
curl -s -X POST -H "Authorization: Bearer $TOKEN" $BASE/api/tasks/<id>/start
curl -s -H "Authorization: Bearer $TOKEN" "$BASE/api/tasks/<id>/events?after=0"
```

## HTTP 接口（本里程碑已实现）

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | 健康检查（无需 token） |
| GET | `/api/system` | 系统概况 / 队列 |
| GET/PUT | `/api/settings` | `maxConcurrentTasks` |
| GET | `/api/model-usage` | 按日用量汇总 |
| GET/POST | `/api/tasks` | 列表 / 创建 |
| GET | `/api/tasks/{id}` | 任务详情 |
| GET | `/api/tasks/{id}/events?after=&limit=` | 事件增量 |
| GET | `/api/tasks/{id}/stream?after=` | SSE 实时事件 |
| GET/PUT | `/api/tasks/{id}/prompt` | 提示词 |
| POST | `/api/tasks/{id}/start\|pause\|resume\|abort\|retry\|close-sandbox` | 生命周期 |
| POST | `/api/tasks/{id}/flag-feedback` | Flag 人工审核 |

WebSocket `/ws/tasks/{id}`、附件、Writeup、真实沙箱与模型网关留到后续里程碑。

## 依赖说明

沙箱环境可能无法访问 `proxy.golang.org`。仓库通过 `replace` 指向 `vendor/` 中的源码树，因此：

```bash
go test -mod=mod ./...
go build -mod=mod ./cmd/ctfagent-daemon
```

可在无外网模块代理的情况下完成编译与测试。SQLite 使用纯 Go 的 `github.com/ncruces/go-sqlite3`（wazero + 嵌入 wasm），无需 cgo。
