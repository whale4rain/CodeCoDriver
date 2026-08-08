CREATE TABLE IF NOT EXISTS memory_links (
    id TEXT PRIMARY KEY,
    memory_id TEXT NOT NULL REFERENCES memory_entries(id) ON DELETE CASCADE,
    repository_id TEXT NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    target_type TEXT NOT NULL,
    target_id TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS memory_links_unique_idx
    ON memory_links(memory_id, target_type, target_id);

CREATE INDEX IF NOT EXISTS memory_links_memory_idx
    ON memory_links(memory_id, target_type);

CREATE INDEX IF NOT EXISTS memory_links_target_idx
    ON memory_links(repository_id, target_type, target_id);
