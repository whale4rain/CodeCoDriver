# CodeCoDriver 实现方案与步骤

## 1. 总体实现原则

实现顺序应遵循以下原则：

1. 先做可运行的闭环，再做泛化
2. 先做运行时骨架，再做 Agent 智能
3. 先做结构化索引，再做复杂记忆
4. 先做可追踪，再做效果优化

不要一开始堆太多 Agent、太多工具、太多模型实验。先把一个稳定闭环做透。

## 2. 实现阶段概览

建议按以下阶段推进：

1. 项目骨架与基础设施
2. 仓库索引与检索基础
3. Runtime 状态机与任务执行闭环
4. Agent 能力接入
5. 测试、审查与重规划
6. 长期记忆沉淀与复用
7. Dashboard、追踪与评估
8. 稳定性增强与项目包装

下面给出每个阶段的目标、实现内容与完成标准。

## 3. 阶段一：项目骨架与基础设施

### 3.1 阶段目标

建立后续开发的稳定底座，让系统具备最小可运行服务能力。

### 3.2 实现内容

- 初始化 Go 项目结构
- 建立 API 服务和 Worker 进程
- 配置 PostgreSQL、Redis、pgvector
- 建立数据库迁移机制
- 建立基础配置管理
- 建立日志、错误处理、链路 ID 传递
- 定义核心领域对象与接口

### 3.3 优先产出

- `cmd/api`
- `cmd/worker`
- `internal/runtime`
- `internal/storage`
- `internal/trace`
- `internal/config`

### 3.4 完成标准

- 服务可启动
- 数据库可迁移
- 可以创建一个空任务并落库
- 有基础日志与健康检查接口

当前已实现 PostgreSQL 持久化 Store、自动 schema migration 和独立 Docker Compose。Repository、索引、Task、Run、Step、Artifact 与 Memory 均可在 API 重启后恢复；内存 Store 保留用于快速单元测试。

## 4. 阶段二：仓库索引与检索基础

### 4.1 阶段目标

让系统具备理解代码仓库的基础能力，为后续 Agent 提供可用上下文。

### 4.2 实现内容

- 接入仓库注册接口
- 建立文件树扫描能力
- 读取文件元数据与摘要
- 提取 symbol 信息
- 建立基础 relation 信息
- 构建文本和向量检索
- 实现文件搜索、symbol 搜索、邻接扩展

### 4.3 推荐实现顺序

1. 文件扫描
2. 文件内容摘要
3. symbol 抽取
4. repository metadata 入库
5. embedding 生成
6. 检索接口

### 4.4 完成标准

- 可对一个仓库执行索引
- 可通过 API 查询文件、symbol、相关代码片段
- 可返回一个结构化上下文包

## 5. 阶段三：Runtime 状态机与任务执行闭环

### 5.1 阶段目标

建立系统最核心的任务驱动与状态机能力。

### 5.2 实现内容

- 定义任务状态机
- 定义 step 模型
- 定义 run 模型
- 实现 worker 调度流程
- 实现 step 执行记录
- 实现失败状态落库
- 实现重试和取消机制

### 5.3 推荐实现顺序

1. `Task` 创建
2. `Run` 创建
3. `Step` 执行器
4. 状态流转
5. Trace 记录
6. 错误恢复

### 5.4 完成标准

- 一个任务能驱动多个 step 顺序执行
- 状态可查询
- 失败不会丢失上下文
- 整个执行链路可追踪

当前已实现单进程 worker 并发限制、任务级取消、启动恢复和进程内队列去重。恢复采用 at-least-once 语义：中断 Run 先关闭为 FAILED，再从新 Run 重放任务。分布式 Redis lease 与 fencing token 留在下一可靠性增量。

## 6. 阶段四：Agent 能力接入

### 6.1 阶段目标

把多 Agent 逻辑挂到受控运行时里，而不是散落在业务代码中。

### 6.2 实现内容

- 定义统一 Agent 接口
- 实现 `Planner Agent`
- 实现 `Codebase Agent`
- 实现 `Patch Agent`
- 实现基础 LLM Provider
- 定义 Agent 输入输出 schema

### 6.3 推荐实现顺序

1. `Planner Agent`
2. `Codebase Agent`
3. `Patch Agent`
4. LLM Provider 抽象
5. Prompt / schema 管理

### 6.4 完成标准

- 给定任务后可生成计划
- 可以召回相关上下文
- 可以输出一个结构化 patch 草案

## 7. 阶段五：测试、审查与重规划

### 7.1 阶段目标

让系统从“会生成”升级到“会验证、会回退、会重试”。

### 7.2 实现内容

- 实现 `Test Agent`
- 接入测试命令执行器
- 解析测试失败日志
- 实现 `Reviewer Agent`
- 建立 `REPLAN_REQUIRED` 分支
- 加入人工审核节点

### 7.3 推荐实现顺序

1. 测试执行器
2. `Test Agent`
3. reviewer 规则
4. 重规划触发条件
5. 人工审核接口

### 7.4 完成标准

- patch 生成后可自动执行测试
- 测试失败能回写失败原因
- 失败后可重新进入规划流程

当前运行时采用最多三次 patch attempt 的有界修复循环。Sandbox 失败证据会压缩后传递给 Repair Planner 和 Patch Agent，每次尝试都保留独立 step 与 artifact；达到上限后进入 Reviewer，禁止无限重试。

Sandbox 通过后 Reviewer 仍可触发 REQUEST_CHANGES。审查意见将作为结构化 feedback 进入下一次 Repair Planner/Patch；只有 APPROVE_PROPOSAL 才进入 COMPLETED，达到尝试上限或无法确定决策时进入 HUMAN_REVIEW_REQUIRED。
- 高风险任务可挂起等待人工确认

## 8. 阶段六：长期记忆沉淀与复用

### 8.1 阶段目标

让系统具备跨任务学习能力，不再只依赖当前任务上下文。

### 8.2 实现内容

- 实现 `MemoryEntry` 存储
- 定义 repository / task / pattern / execution 四类记忆
- 构建记忆写入策略
- 构建记忆检索策略
- 在 Codebase Agent 中接入记忆召回
- 实现记忆评分和老化策略

### 8.3 推荐实现顺序

1. memory schema
2. 成功任务沉淀
3. 失败任务沉淀
4. memory search API
5. 检索重排序
6. Agent 消费 memory

### 8.4 完成标准

- 系统能存储历史经验
- 新任务可召回历史任务与模式记忆
- memory 命中可在 trace 中体现

当前已实现阶段六的基础闭环：MemoryEntry 支持 source、score、metadata；任务启动按标题和描述召回同仓库历史记忆；命中以 memory-context artifact 写入 trace；Planner 和 Codebase Agent 均可消费命中结果。任务结束时会按执行结果沉淀 `execution_summary`，批准任务额外沉淀 `execution_success`，每个失败 attempt 沉淀 `failure_pattern`，并记录 run、attempt、decision 等结构化元数据。

当前已完成确定性文本 embedding、JSONB 持久化、关键词/cosine 混合检索，以及基于时间新鲜度和访问次数的 rerank。每次召回都会持久化访问时间与次数。当前仍待增强的部分是 pgvector 原生索引、真实 embedding provider 和更高质量的失败模式归纳。

## 9. 阶段七：Python Sidecar 与 MCP 集成

### 9.1 阶段目标

把 Python 生态能力与标准化工具协议接入系统，但不破坏主运行时边界。

### 9.2 实现内容

- 建立 Python `document-service`
- 建立 gRPC 协议
- 建立 Go 侧 sidecar client
- 接入 MCP client
- 注册若干 MCP 工具
- 将非核心工具统一接到 Tool Gateway

### 9.3 推荐实现顺序

1. gRPC contract
2. `document-service`
3. Go sidecar client
4. MCP client
5. Tool Gateway 路由

### 9.4 完成标准

- Go runtime 可调用 Python 解析服务
- Go runtime 可调用 MCP 工具
- 外部工具接入方式一致

阶段七已完成：`internal/tools` 提供线程安全的 Tool Gateway、HTTP Python document-service client，以及基于 JSON-RPC stdio 的 MCP client/proxy。MCP client 支持 initialize、initialized notification 和 tools/list 能力协商；Runtime 已向 AgentRequest 注入 Gateway，工具调用支持全局和按 Agent 允许列表、30 秒超时、一次失败重试和执行上下文；调用审计会持久化到 `tool_calls` 并通过任务 trace 返回。项目内 `python/document_service.py` 提供 `/health` 和 `/parse`，可在不引入 Python 依赖的情况下完成文本分块和 token 提取。gRPC contract 和更复杂的工具重试退避属于后续增强，不阻塞阶段七验收。

## 10. 阶段八：Dashboard、追踪与评估

### 10.1 阶段目标

让系统具备可演示性、可诊断性和可量化评估能力。

### 10.2 实现内容

- 构建任务列表页面
- 构建 trace 页面
- 展示计划、召回上下文、patch、测试日志、review 结论
- 统计成功率、测试通过率、重规划次数
- 构造 benchmark case 集
- 建立 baseline 对比流程

### 10.3 推荐实现顺序

1. trace 查询接口
2. artifact 查询接口
3. Web 页面
4. metrics 聚合
5. benchmark 执行器

当前已完成阶段八第一批实现：Go API 提供 `/dashboard/overview`、`/repositories/{id}/overview` 和 `/tasks/{id}/timeline`；`web/` 提供 React + TypeScript + Vite 控制台，包含 Overview、Task trace、Memory inspector 和 Evaluation 四个视图，支持任务自动刷新、时间线查看、记忆检索、benchmark case 和 evaluation run 指标展示。`POST /evaluations/runs` 可以创建真实 Runtime 任务，任务完成或失败后自动回写 EvaluationRun。Evaluation API 现在按 mode 和 case 聚合通过率，前端展示 Agent 与 baseline 对比。指标历史趋势和自动化 benchmark 执行编排仍待后续实现。

### 10.4 完成标准

- 可完整演示一次任务执行
- 可查看每个 step 细节
- 可展示定量指标

## 11. 阶段九：稳定性增强与项目包装

### 11.1 阶段目标

把系统从“能跑”提升到“能讲、能演示、能写进简历”。

### 11.2 实现内容

- 增加异常场景保护
- 增加限流与超时控制
- 优化 prompt 和检索策略
- 固化 demo 仓库和 benchmark
- 编写 README、架构图、示例 trace
- 录制演示流程

### 11.3 完成标准

- 系统在 demo 仓库上可重复稳定运行
- 有代表性 benchmark 结果
- 有完整项目说明材料

## 12. 关键实现顺序总结

真正的落地顺序应保持如下依赖关系：

1. 先搭 Runtime 骨架和数据库
2. 再做仓库索引和检索
3. 再做任务状态机和 step 执行
4. 再接 Planner / Codebase / Patch 三个主 Agent
5. 再做 Test / Reviewer / Replan
6. 再做长期记忆
7. 再接 Python sidecar 与 MCP
8. 最后补 Dashboard、评估、包装

原因很简单：

- 没有 runtime，Agent 无处运行
- 没有索引，Agent 拿不到有效上下文
- 没有状态机，系统不可控
- 没有验证环节，patch 结果不可信
- 没有记忆，multi-task 价值不成立

## 13. 每阶段验收问题

每完成一个阶段，都应能回答以下问题：

### 阶段一后

- 服务是否可以独立启动并写库

### 阶段二后

- 是否能从仓库中稳定召回相关文件和符号

### 阶段三后

- 是否能看到完整执行链路而不是黑盒过程

### 阶段四后

- 系统是否真的在用受控 Agent，而不是单次脚本拼接

### 阶段五后

- 失败后是否能自动反馈并重试

### 阶段六后

- 历史任务是否真的会被后续任务利用

### 阶段七后

- Python 和 MCP 是否只是能力扩展，而不是反客为主

### 阶段八后

- 是否能用指标证明系统优于简单 baseline

## 14. 推荐的首个开发闭环

如果你要马上开始写代码，首个闭环建议是：

1. 注册一个本地仓库
2. 建立文件索引和 symbol 索引
3. 创建一个 `Bug Fix` 任务
4. Planner 输出计划
5. Codebase Agent 召回上下文
6. Patch Agent 生成 patch
7. Test Agent 运行测试
8. Trace 页面展示结果

先把这个闭环跑通，再继续加记忆、MCP、文档处理与评估。
