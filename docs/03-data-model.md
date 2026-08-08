# CodeCoDriver 数据模型设计

## 1. 建模原则

数据模型需要支持四件事：

1. 任务执行
2. 仓库索引
3. 长期记忆
4. 执行追踪与评估

因此模型不能只围绕聊天消息设计，而要围绕：

- `Repository`
- `Task`
- `Run`
- `Step`
- `Artifact`
- `Memory`
- `Tool Call`

## 2. 核心实体

### 2.1 Repository

表示一个被系统管理的代码仓库。

关键字段建议：

- `id`
- `name`
- `root_path`
- `default_branch`
- `primary_language`
- `status`
- `last_indexed_at`
- `created_at`
- `updated_at`

### 2.2 RepositoryFile

表示仓库中的文件元数据。

关键字段建议：

- `id`
- `repository_id`
- `path`
- `language`
- `size_bytes`
- `content_hash`
- `is_generated`
- `summary`
- `embedding`
- `indexed_at`

### 2.3 Symbol

表示函数、类、接口、方法、配置项等符号级信息。

关键字段建议：

- `id`
- `repository_id`
- `file_id`
- `symbol_name`
- `symbol_type`
- `signature`
- `start_line`
- `end_line`
- `parent_symbol_id`
- `summary`

### 2.4 CodeRelation

表示文件或符号之间的关系。

关键字段建议：

- `id`
- `repository_id`
- `source_type`
- `source_id`
- `target_type`
- `target_id`
- `relation_type`

`relation_type` 例如：

- `imports`
- `calls`
- `implements`
- `references`
- `configures`

### 2.5 Task

表示一个用户发起的任务。

关键字段建议：

- `id`
- `repository_id`
- `task_type`
- `title`
- `input_text`
- `input_metadata`
- `status`
- `priority`
- `created_by`
- `created_at`
- `updated_at`

### 2.6 TaskRun

表示某个任务的一次完整执行。

关键字段建议：

- `id`
- `task_id`
- `run_status`
- `current_state`
- `replan_count`
- `started_at`
- `ended_at`
- `final_summary`

### 2.7 TaskStep

表示执行过程中的单步。

关键字段建议：

- `id`
- `run_id`
- `task_id`
- `agent_name`
- `step_type`
- `sequence_no`
- `status`
- `input_refs`
- `output_refs`
- `error_message`
- `started_at`
- `ended_at`
- `latency_ms`

### 2.8 ToolCall

表示一次工具调用。

关键字段建议：

- `id`
- `step_id`
- `tool_name`
- `provider_type`
- `request_payload`
- `response_payload`
- `status`
- `latency_ms`
- `created_at`

`provider_type` 可取：

- `local`
- `mcp`
- `python_sidecar`

当前已实现 `tool_calls` 持久化。Runtime 会把任务、Run、Step 执行上下文注入工具调用，记录请求、响应、状态、错误和耗时，并在任务 trace 中返回这些记录。

### 2.9 Artifact

表示执行过程中产出的补丁、日志、上下文包、计划等对象。

关键字段建议：

- `id`
- `task_id`
- `run_id`
- `artifact_type`
- `storage_type`
- `content_ref`
- `summary`
- `created_at`

`artifact_type` 例如：

- `plan`
- `context_bundle`
- `patch`
- `test_log`
- `review_report`
- `memory_snapshot`

### 2.10 MemoryEntry

表示长期记忆条目。

当前实现保留 `source`、`score`、`metadata` 以及更丰富的结构化字段，包括 `title`、`summary`、`symptom`、`root_cause`、`changed_files`、`symbols`、`test_command`、`verification_evidence`、`success_score`、`source_run_id`。运行时会沉淀以下结构化记忆：

- `execution_summary`：每次结束的审计摘要
- `execution_success`：Reviewer 批准后的成功经验
- `failure_pattern`：每个失败 attempt 或 Agent loop 中间阶段失败的验证证据、症状与根因

当前记忆同时保留 JSONB embedding 与 pgvector `embedding_halfvec halfvec(2560)`。默认通过火山方舟 `doubao-embedding-text-240715` 生成 2560 维向量并建立 HNSW 索引；未配置 API Key 时回退到确定性的 32 维本地 embedding，仅写入 JSONB。检索使用关键词命中与 cosine 相似度的混合评分，并优先用 pgvector 召回语义候选。每次召回会更新 `last_accessed_at` 和 `access_count`，rerank 会结合时间新鲜度与访问次数，降低长期未使用记忆的影响。

关键字段建议：

- `id`
- `repository_id`
- `memory_type`
- `source_task_id`
- `source_run_id`
- `title`
- `content`
- `summary`
- `embedding`
- `importance_score`
- `success_score`
- `last_accessed_at`
- `created_at`

当前实现还包含：

- `last_accessed_at`：最近一次被召回的时间
- `access_count`：被召回次数
- `memory_links`：记忆到 task、run、file、symbol 的可追溯关联
- `duplicate_of`、`conflict_group_id`、`condition`、`refined_at`：记忆提炼、去重和冲突合并状态

`memory_type` 建议至少包括：

- `repository`
- `task`
- `pattern`
- `execution`

### 2.11 HumanReview

表示人工审核节点。

关键字段建议：

- `id`
- `task_id`
- `run_id`
- `review_type`
- `status`
- `requested_reason`
- `decision`
- `reviewer`
- `reviewed_at`

## 3. 推荐表结构

建议初版至少建立以下表：

- `repositories`
- `repository_files`
- `symbols`
- `code_relations`
- `tasks`
- `task_runs`
- `task_steps`
- `tool_calls`
- `artifacts`
- `memory_entries`
- `memory_links`
- `human_reviews`
- `feedback_records`

## 4. 关键关联关系

### 4.1 Repository 相关

- 一个 `repository` 有多个 `repository_files`
- 一个 `repository_file` 有多个 `symbols`
- `code_relations` 将文件和符号连接起来

### 4.2 Task 相关

- 一个 `task` 有多个 `task_runs`
- 一个 `task_run` 有多个 `task_steps`
- 一个 `task_step` 有多个 `tool_calls`
- 一个 `task_run` 有多个 `artifacts`

### 4.3 Memory 相关

- 一个 `task_run` 可以沉淀多个 `memory_entries`
- `memory_links` 可把 memory 与文件、符号、任务、artifact 关联起来

## 5. 索引建议

### 5.1 唯一约束

- `repositories(root_path)` 唯一
- `repository_files(repository_id, path)` 唯一

### 5.2 常规索引

- `tasks(repository_id, status)`
- `task_runs(task_id, run_status)`
- `task_steps(run_id, sequence_no)`
- `tool_calls(step_id)`
- `memory_entries(repository_id, memory_type)`
- `symbols(repository_id, symbol_name)`
- `repository_files(repository_id, path)`

### 5.3 向量索引

为以下字段建立向量索引：

- `repository_files.embedding`
- `memory_entries.embedding`

## 6. 对象存储内容建议

以下内容不建议直接塞入主表：

- 大体积原始日志
- 补丁全文
- 大型上下文拼接结果
- 文档解析原始输出

建议存对象存储，再在 `artifacts.content_ref` 中保存引用。

## 7. Memory Consolidation 规则

成功任务结束后可沉淀：

- 成功修复摘要
- 涉及文件模式
- 测试策略
- reviewer 认可的改法

失败任务结束后可沉淀：

- 失败原因摘要
- 错误检索路径
- 无效 patch 模式

当前运行时已经将上述规则落为结构化 `MemoryEntry`，并通过 `source`、`score`、`metadata` 保留来源、重要性和执行上下文。

## 8. 后续可扩展模型

后续如需增强，可以新增：

- `benchmark_cases`
- `evaluation_runs`
- `cost_records`
- `prompt_templates`
- `agent_policies`
- `repository_snapshots`
