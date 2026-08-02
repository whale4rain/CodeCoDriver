# Resume Project Summary

## One-Line Description

CodeCoDriver is a repository-aware multi-agent engineering runtime that plans code changes, retrieves source context, generates and validates patches in a sandbox, reviews evidence, and learns from historical execution memory.

## Resume Bullets

- Built a Go multi-agent runtime with Planner, Codebase, Patch, Test, and Reviewer agents, bounded repair loops, PostgreSQL state persistence, task cancellation, startup recovery, and auditable Run/Step/Artifact traces.
- Implemented repository-aware retrieval with safe path boundaries, symbol indexing, structured execution memory, deterministic embedding persistence, hybrid keyword/cosine search, freshness decay, and access-frequency reranking.
- Integrated Python document processing and MCP JSON-RPC tools behind a policy-controlled Tool Gateway with per-Agent allowlists, timeouts, retries, and persisted ToolCall audit records.
- Delivered a React/TypeScript evaluation console with task timelines, tool and LLM usage traces, benchmark suites, Agent/Baseline comparisons, batch progress, and historical pass-rate snapshots.
- Added operational protections including per-client API rate limiting, HTTP timeouts, DeepSeek token/latency/cost tracking, reproducible demo data, and PostgreSQL-backed evaluation metrics.

## Technical Focus

Go, PostgreSQL, React, TypeScript, DeepSeek API, Python sidecar, MCP, JSON-RPC, sandboxed Git patch validation, multi-agent orchestration, long-term memory retrieval, and runtime reliability.
