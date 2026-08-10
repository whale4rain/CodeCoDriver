ALTER TABLE llm_usages DROP CONSTRAINT IF EXISTS llm_usages_step_id_fkey;
ALTER TABLE tool_calls DROP CONSTRAINT IF EXISTS tool_calls_step_id_fkey;
