# CodeCoDriver 文档索引

本目录包含 `CodeCoDriver` 项目的核心设计文档，后续开发、拆任务、建表、接口实现都以这些文档为基线。

## 文档列表

- `01-project-design.md`
  - 项目定位、目标用户、核心能力、范围边界、非功能性要求
- `02-architecture-design.md`
  - 系统架构、服务边界、运行时设计、Agent 设计、工具与记忆体系
- `03-data-model.md`
  - 核心领域模型、数据库表建议、对象关系、关键索引
- `04-implementation-plan.md`
  - 实现顺序、阶段目标、交付标准、关键风险与验收点
- `05-runtime-reliability.md`
  - Worker 并发、任务取消、启动恢复、幂等边界与分布式租约规划
- `06-demo-runbook.md`
  - Demo 仓库、服务启动、数据种子和完整演示流程
- `07-resume-project-summary.md`
  - 简历项目描述、技术亮点和可量化叙述
- `11-current-architecture.md`
  - 当前实际代码中的总体架构、Agent Loop、Worker、记忆、工具和可观测性设计
- `12-docker-sandbox.md`
  - Docker 沙箱隔离边界、配置、CRLF 处理与真实 E2E 测试结果

## 使用建议

建议按以下顺序阅读和落地：

1. 先读 `01-project-design.md`，统一项目目标与 MVP 边界
2. 再读 `02-architecture-design.md`，确定服务拆分和运行时实现方式
3. 结合 `03-data-model.md` 建数据库和对象模型
4. 按 `04-implementation-plan.md` 的顺序推进实现
5. 结合 `05-runtime-reliability.md` 验证运行时可靠性语义

## 当前项目定义

`CodeCoDriver` 是一个面向真实代码仓库的软件工程 Agent Runtime。系统接收一个工程任务后，完成任务规划、仓库检索、上下文召回、补丁生成、测试验证、风险审查与记忆沉淀，并保留完整执行链路。
