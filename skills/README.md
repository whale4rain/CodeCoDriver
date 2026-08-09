# CodeCoDriver Skills

Each `.json` file in this directory is one Skill. The API scans this directory at startup and reloads it after `POST /skills` or `POST /skills/import`.

After editing files manually, call `POST /skills/reload` or click `Reload folder` in the Dashboard to rescan this directory without restarting the API.

## Manual Skill

Create a file such as `api-review.json`:

```json
{
  "name": "api-review",
  "description": "API contract and review focus",
  "keywords": ["api", "contract", "endpoint"],
  "path_patterns": ["**/*.go", "docs/**"],
  "workflow": "standard_agent_loop",
  "allowed_tools": ["read_file", "list_files"],
  "prompts": {
    "planner": {
      "system": "You are the Planner Agent in CodeCoDriver.",
      "user": "API REVIEW: Focus on {{task_title}} and {{task_description}}."
    },
    "patch": {
      "system": "You are the Patch Agent in CodeCoDriver.",
      "user": "API REVIEW: Preserve public API contracts and add focused tests."
    },
    "reviewer": {
      "system": "You are the Reviewer Agent in CodeCoDriver.",
      "user": "API REVIEW: Verify contract compatibility and regression risk."
    }
  }
}
```

## GitHub Import

From the Dashboard Skills page, paste a GitHub repository URL or a GitHub `.json` skill file URL. The runtime clones the repository (or downloads the raw JSON) and writes valid `.json` files into this directory.

## Prompt Variables

Available variables in prompt templates:

- `task_title`, `task_description`
- `repository_name`, `primary_language`
- `indexed_files`, `indexed_symbols`
- `attempt`, `memory_guidance`
- `repair_feedback`, `previous_patch`, `context_json`
- `selected_skill`, `selected_workflow`
