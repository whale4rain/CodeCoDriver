# CodeCoDriver

CodeCoDriver 是一个面向真实代码仓库的多 Agent 工程运行时。它会索引本地代码库、接收工程任务、制定计划、检索相关代码、生成补丁、在沙箱中验证、执行审查、记录完整审计轨迹，并跨任务复用长期记忆。

[English](README.md)

## 快速开始

前置条件：Go、Node.js/npm、Docker Desktop，以及 `DEEPSEEK_API_KEY`。

1. 启动 PostgreSQL 和 Redis：

```powershell
docker compose up -d postgres redis
```

2. 启动 Go API：

```powershell
$env:DEEPSEEK_API_KEY="your-api-key"
$env:DOUBAO_API_KEY="your-doubao-api-key"
$env:GOTELEMETRY="off"
go run ./cmd/api
```

设置 `DOUBAO_API_KEY` 会启用火山方舟 `doubao-embedding-text-240715` 的真实语义 embedding。未设置时系统会退回确定性本地 embedding，不影响本地开发。

启动 API 前设置 `CODECODRIVER_REDIS_ADDR=localhost:6379` 可启用 Redis 租约和多 Worker 协作。如果不设置，Runtime 会退回单进程内存队列。

3. 启动 Dashboard：

```powershell
cd web
npm install
npm run dev
```

打开 `http://127.0.0.1:5173`。API 默认监听 `http://localhost:8080`，Vite 会把 API 请求代理到 Go 服务。

4. 初始化 Demo 仓库和 benchmark case：

```powershell
./scripts/seed-demo.ps1
```

脚本会注册本地 `demo/go-rest-api` 仓库并创建 benchmark case，让 Dashboard 开箱即可使用。

## 页面说明

### Overview 总览

Overview 是主要操作入口：

- 输入仓库名称和本地路径，点击 `Register repo` 注册仓库。
- 选择仓库、填写任务标题和描述，点击 `Create task` 创建任务。
- 查看运行中任务、已完成任务、人工审核数量、平均耗时、完成率和失败数。
- 点击最近任务可直接进入执行轨迹。

### Task Trace 任务轨迹

Task Trace 页面展示所有任务和选中任务的详细审计轨迹：

- 点击左侧任务列表中的任意任务，右侧会加载时间线。
- 时间线包含 Planner、Codebase、Patch、Test、Reviewer、ToolCall 和 LLM 用量事件。
- 如果任务是 `HUMAN_REVIEW_REQUIRED`，可以填写可选的审核原因，然后点击 `Approve` 或 `Reject`。
- 批准后任务标记为完成；拒绝后任务标记为失败。

### Memory 记忆检查

Memory 页面用于查看按仓库隔离的长期记忆：

- 在 `Repository ID` 中输入仓库 ID。该 ID 会由 `seed-demo.ps1` 输出，也会显示在 Overview 的仓库选择器中。
- 输入查询词，例如 `retry timeout` 或 `pagination validation`。
- 点击 `Search memory` 查看记忆命中，包括类型、分数、来源、被召回次数和创建时间。

### Evaluation 评估

Evaluation 页面用于运行和比较 benchmark：

- 选择 `Agent` 或 `Baseline` 模式。
- 点击 `Run suite` 把所有已注册 benchmark case 作为一批执行。
- 查看通过率、运行总数、benchmark case、最近批次、历史指标、Agent 与 baseline 对比，以及单次运行结果。

## 推荐使用流程

1. 启动 PostgreSQL、API 和 Dashboard。
2. 如果希望使用可复现 Demo，运行 `seed-demo.ps1`。
3. 在 Overview 中注册新仓库，或为 Demo 仓库创建任务。
4. 在 Task Trace 中查看每个 Agent 步骤和失败证据。
5. 如果系统要求人工审核，在任务轨迹页批准或拒绝。
6. 在 Memory 中搜索历史经验，再发起相似任务。
7. 运行 Evaluation suite 衡量 Agent 的 benchmark 表现。

## 工作方式

- `Planner Agent` 制定执行计划；修复尝试时会生成聚焦的修复计划。
- `Codebase Agent` 检索相关文件；当任务涉及测试时，会尽量同时召回源码和已有 `_test.go`。
- `Patch Agent` 生成 unified diff，并接收关于当前源码状态、新文件语法、diff 头、hunk context 的明确约束。
- `Sandbox` 会把仓库复制到临时目录，规范化并校验 diff，应用补丁并运行测试，不修改原始工作区。
- `Reviewer Agent` 在批准前检查正确性、回归风险、证据和测试覆盖。
- 分布式 Worker 会为任务领取 Redis 租约，执行期间续租，结束后释放，并使用 fencing token 阻止过期 Worker 覆盖当前任务状态。
- 长期记忆会沉淀执行总结、成功模式和失败模式，并保存症状、根因、变更文件、符号、测试命令、验证证据和成功分等结构化字段。Doubao embedding 持久化到 pgvector `halfvec(2560)` 并使用 HNSW 索引，召回结合语义、关键词、新鲜度和访问频率信号。Agent loop 中间阶段失败也会沉淀为失败记忆，供后续任务规避。
- `Tool Gateway` 支持本地工具、Python 文档 sidecar 和 MCP JSON-RPC stdio 服务。

模型默认使用 DeepSeek OpenAI-compatible API 的 `deepseek-v4-flash`。

## 配置项

常用环境变量：

| 变量 | 作用 |
|---|---|
| `DEEPSEEK_API_KEY` | DeepSeek API Key。 |
| `DEEPSEEK_BASE_URL` | 覆盖 DeepSeek API 地址。 |
| `DEEPSEEK_TIMEOUT_SECONDS` | 覆盖模型请求超时。 |
| `DOUBAO_API_KEY` | 火山方舟 embedding API Key，也可用 `CODECODRIVER_EMBEDDING_API_KEY`。 |
| `CODECODRIVER_EMBEDDING_BASE_URL` | 覆盖 embedding API 地址，默认 `https://ark.cn-beijing.volces.com/api/v3`。 |
| `CODECODRIVER_EMBEDDING_MODEL` | 覆盖 embedding 模型，默认 `doubao-embedding-text-240715`。 |
| `CODECODRIVER_EMBEDDING_TIMEOUT_SECONDS` | 覆盖 embedding 请求超时，默认 `30`。 |
| `DATABASE_URL` | 覆盖 PostgreSQL 连接串。 |
| `CODECODRIVER_ADDR` | 覆盖 API 监听地址。 |
| `CODECODRIVER_WORKERS` | 本地 Worker 并发数，默认 `1`。 |
| `CODECODRIVER_REDIS_ADDR` | Redis 地址，用于分布式任务租约和 fencing token。 |
| `CODECODRIVER_RATE_LIMIT` | 每个客户端每分钟 API 请求数，`0` 表示关闭。 |
| `DEEPSEEK_INPUT_COST_PER_MILLION` | 启用估算输入成本。 |
| `DEEPSEEK_OUTPUT_COST_PER_MILLION` | 启用估算输出成本。 |

## API 概览

核心接口：

- `GET /dashboard/overview`
- `GET /repositories`、`POST /repositories`、`POST /repositories/{id}/index`
- `GET /tasks`、`POST /tasks`、`GET /tasks/{id}/timeline`、`POST /tasks/{id}/cancel`
- `GET /memory/search?repository_id=...&query=...`
- `GET /evaluations`、`POST /evaluations/cases`、`PUT /evaluations/cases/{id}`
- `POST /evaluations/runs`、`POST /evaluations/suites`
- `GET /human-reviews`、`POST /human-reviews/{taskId}/approve`、`POST /human-reviews/{taskId}/reject`

## 文档

- [项目设计](docs/01-project-design.md)
- [架构设计](docs/02-architecture-design.md)
- [数据模型](docs/03-data-model.md)
- [实现方案](docs/04-implementation-plan.md)
- [运行时可靠性](docs/05-runtime-reliability.md)
- [Demo 运行手册](docs/06-demo-runbook.md)
- [简历项目总结](docs/07-resume-project-summary.md)

## 当前状态

CodeCoDriver 目前是本地工程运行时原型。它支持真实任务执行、补丁验证、长期记忆、分布式 Worker lease、Dashboard 操作和 benchmark 评估，但还不是生产级多用户产品：当前没有登录鉴权、容器级隔离，benchmark 结果也仍受模型输出质量影响。
