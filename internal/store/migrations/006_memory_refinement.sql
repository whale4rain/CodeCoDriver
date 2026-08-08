ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS duplicate_of TEXT;
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS conflict_group_id TEXT;
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS condition TEXT NOT NULL DEFAULT '';
ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS refined_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS memory_entries_duplicate_of_idx
    ON memory_entries(duplicate_of);

CREATE INDEX IF NOT EXISTS memory_entries_conflict_group_idx
    ON memory_entries(conflict_group_id);
