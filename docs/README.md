# Repo Engineer 文档索引

本目录包含 `Repo Engineer` 项目的核心设计文档，后续开发、拆任务、建表、接口实现都以这些文档为基线。

## 文档列表

- `01-project-design.md`
  - 项目定位、目标用户、核心能力、范围边界、非功能性要求
- `02-architecture-design.md`
  - 系统架构、服务边界、运行时设计、Agent 设计、工具与记忆体系
- `03-data-model.md`
  - 核心领域模型、数据库表建议、对象关系、关键索引
- `04-implementation-plan.md`
  - 实现顺序、阶段目标、交付标准、关键风险与验收点

## 使用建议

建议按以下顺序阅读和落地：

1. 先读 `01-project-design.md`，统一项目目标与 MVP 边界
2. 再读 `02-architecture-design.md`，确定服务拆分和运行时实现方式
3. 结合 `03-data-model.md` 建数据库和对象模型
4. 按 `04-implementation-plan.md` 的顺序推进实现

## 当前项目定义

`Repo Engineer` 是一个面向真实代码仓库的软件工程 Agent Runtime。系统接收一个工程任务后，完成任务规划、仓库检索、上下文召回、补丁生成、测试验证、风险审查与记忆沉淀，并保留完整执行链路。
