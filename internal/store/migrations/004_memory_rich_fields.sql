ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS title TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS summary TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS symptom TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS root_cause TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS changed_files TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS symbols TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS test_command TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS verification_evidence TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS success_score DOUBLE PRECISION NOT NULL DEFAULT 0;
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS source_run_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS memory_changed_files_gin_idx ON memory_entries USING GIN(changed_files);
CREATE INDEX IF NOT EXISTS memory_symbols_gin_idx ON memory_entries USING GIN(symbols);
CREATE INDEX IF NOT EXISTS memory_success_score_idx ON memory_entries(success_score DESC, created_at DESC);
