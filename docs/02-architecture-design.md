# CodeCoDriver 架构设计

## 1. 总体架构

系统采用混合架构：

- `Go` 负责核心后端与 Agent Runtime
- `Python` 负责文档处理与生态能力侧车
- `MCP` 负责标准化工具接入
- `PostgreSQL + pgvector + Redis` 负责状态、记忆与队列

总体结构如下：

```text
Client / Web UI / CLI
        |
        v
API Gateway (Go)
        |
        v
CodeCoDriver Runtime (Go)
|- Task Service
|- Orchestrator
|- State Machine
|- Agent Registry
|- Memory Service
|- Tool Gateway
|- Policy Engine
|- Trace Service
|- Scheduler / Worker
        |
        +--> Local Go Tools
        +--> MCP Client
        +--> Python Sidecar Client (gRPC)
        |
        +--> PostgreSQL
        +--> pgvector
        +--> Redis
        +--> Object Storage

Python Sidecars
|- Document Service
|- Enrichment Service
|- MCP Tool Servers
```

## 2. 服务边界

### 2.1 Go Runtime 的职责

Go 是系统控制面和执行面核心，负责：

- 接收任务请求
- 管理任务状态机
- 驱动多 Agent 编排
- 管理执行超时、取消、重试、重规划
- 管理工具调用与权限策略
- 维护长期记忆与执行记录
- 提供查询、追踪、审计接口

### 2.2 Python Sidecar 的职责

Python 只承担生态型、算法型、解析型能力，不持有系统主流程控制权，负责：

- PDF / DOCX / HTML 等文档解析
- layout-aware chunking
- OCR 与文本清洗
- 嵌入与重排实验能力
- 部分复杂文本预处理
- 对外以 gRPC 或 MCP 服务形式暴露能力

### 2.3 MCP 的职责

MCP 用于工具扩展层，而不是运行时核心。适合接入：

- Issue 系统
- 代码托管平台
- 外部知识源
- 文档解析工具
- 搜索工具

高频核心工具仍建议保留本地原生实现，例如：

- 文件读取
- 文件搜索
- 仓库索引
- patch 应用
- 测试执行

## 3. 核心运行时设计

### 3.1 运行时目标

运行时不是简单的请求转发器，而是一个具备以下能力的任务执行系统：

- 统一 Agent 接口
- 有限状态机
- 工具策略执行
- 失败恢复与重规划
- 步骤级 tracing
- 持久化 execution memory

### 3.2 任务状态机

建议状态机如下：

```text
CREATED
-> INDEX_CHECK
-> PLANNING
-> RETRIEVING_CONTEXT
-> GENERATING_PATCH
-> RUNNING_TESTS
-> REVIEWING
-> COMPLETED

失败分支：
RUNNING_TESTS -> REPLAN_REQUIRED -> PLANNING
REVIEWING -> HUMAN_REVIEW_REQUIRED
任意阶段 -> FAILED
```

### 3.3 Step 模型

每个任务由多个 step 构成，step 是最小可追踪执行单元。每个 step 至少包含：

- `step_id`
- `task_id`
- `run_id`
- `agent_name`
- `step_type`
- `input_refs`
- `tool_calls`
- `output_refs`
- `status`
- `started_at`
- `ended_at`
- `latency_ms`

### 3.4 Agent 接口

Go 侧建议统一成抽象接口：

```go
type Agent interface {
    Name() string
    Run(ctx context.Context, req AgentRequest) (AgentResult, error)
}
```

`AgentRequest` 中不要直接放整个大上下文，而应放引用和约束：

- 任务描述
- 当前计划
- 当前 step 上下文引用
- memory 检索结果
- 工具可用列表
- token / 时间预算

## 4. Agent 设计

### 4.1 Planner Agent

职责：

- 理解任务目标
- 生成执行计划
- 选择后续检索方向
- 在失败后做重规划

输出：

- 子任务列表
- 目标文件候选
- 工具建议
- 成功判定条件

### 4.2 Codebase Agent

职责：

- 做多策略仓库检索
- 拼装上下文包
- 输出结构化仓库理解结果

输入来源：

- 文件树
- symbol 索引
- import / call 关系
- 历史任务记忆
- embedding 相似内容

输出：

- 涉及文件清单
- 关键符号
- 代码片段
- 关联说明

源码上下文读取必须经过安全边界：

- 只读取仓库根目录内已索引的普通文件
- 拒绝路径穿越和指向仓库外的符号链接
- 过滤环境变量、凭证、私钥等敏感文件名
- 使用单文件和总上下文预算限制发送给模型的内容
- 代码片段携带文件路径、语言、行号与截断标记

### 4.3 Patch Agent

职责：

- 基于上下文生成最小必要 patch
- 说明为何这样修改
- 给出潜在风险

约束：

- 限制最大修改文件数
- 限制高风险目录修改
- 尽量最小改动原则

### 4.4 Test Agent

职责：

- 选择测试范围
- 运行测试和 lint
- 解析失败日志
- 总结失败原因

输出：

- 测试结果
- 失败摘要
- 是否需要重规划

### 4.5 Reviewer Agent

职责：

- 检查 patch 风险
- 检查风格与边界条件
- 检查是否出现越权修改
- 决定是否接受、驳回或转人工审核

## 5. Tool Gateway 设计

工具层建议拆成统一网关，避免 Agent 直接耦合具体工具实现。

### 5.1 工具分类

#### 本地原生工具

- `search_files`
- `read_file`
- `search_symbol`
- `find_references`
- `apply_patch`
- `run_tests`
- `run_lint`
- `get_git_history`

#### MCP 工具

- 外部 Issue / Ticket 查询
- 外部文档读取
- 外部知识库搜索
- 其他生态工具

#### Python Sidecar 工具

- `parse_document`
- `extract_structure`
- `build_chunks`
- `embed_texts`
- `rerank_contexts`

### 5.2 工具策略

每次工具调用都要经过策略引擎校验：

- 当前 Agent 是否有该工具权限
- 是否允许读取目标路径
- 是否超过 patch 文件数限制
- 是否超过执行超时
- 是否需要人工确认

## 6. Memory 设计

### 6.1 Repository Memory

存储仓库层面的稳定知识：

- 目录结构
- 文件摘要
- symbol 元数据
- import / call 邻接信息
- 配置文件知识
- 常见入口点

### 6.2 Task Memory

存储任务级经验：

- 任务描述
- 涉及文件
- 成功 patch
- 失败 patch
- 测试结果
- reviewer 结论

### 6.3 Pattern Memory

存储可复用模式：

- 修复模板
- 测试模板
- 常见问题模式
- 风格偏好
- 高风险变更样式

### 6.4 Execution Memory

存储执行过程数据：

- step trace
- 工具调用轨迹
- 重规划路径
- token 与延迟统计

### 6.5 检索策略

不要只做向量检索，建议混合召回：

- 关键词 / path 检索
- symbol 检索
- 邻接关系扩展
- embedding 相似检索
- 基于成功率与近期性的重排序

## 7. 存储设计

### 7.1 PostgreSQL

适合存储：

- 任务状态
- step 与 run
- repository 元数据
- memory 元数据
- tool call records
- 审核与反馈记录

### 7.2 pgvector

适合存储：

- 文件摘要向量
- 文档 chunk 向量
- 任务记忆向量
- pattern memory 向量

### 7.3 Redis

适合存储：

- worker queue
- 分布式锁
- 短期缓存
- 热状态

### 7.4 Object Storage

适合存储：

- 大型文档原文
- 日志全文
- 补丁 artifact
- trace 原始快照

## 8. API 设计建议

建议至少提供以下 API：

- `POST /repositories`
- `POST /repositories/{id}/index`
- `GET /repositories/{id}`
- `POST /tasks`
- `GET /tasks/{id}`
- `GET /tasks/{id}/steps`
- `GET /tasks/{id}/artifacts`
- `GET /tasks/{id}/trace`
- `GET /memory/search`
- `POST /human-reviews/{id}/approve`

## 9. 可观测性设计

建议从第一版就接入：

- 结构化日志
- request id / task id / run id 链路
- step 耗时
- tool 调用耗时
- LLM 请求耗时与成本
- 成功率与失败分类

可以落地为：

- `zap` 日志
- `OpenTelemetry` tracing
- metrics exporter

## 10. 安全与约束

必须在设计中保留这些约束：

- 工具调用权限隔离
- 仅读或受控写工作区
- 测试执行超时
- patch 风险目录拦截
- 人工审批节点
- prompt 与输出审计可查询
