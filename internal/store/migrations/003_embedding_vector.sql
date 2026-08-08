CREATE EXTENSION IF NOT EXISTS vector;

ALTER TABLE memory_entries ADD COLUMN IF NOT EXISTS embedding_halfvec halfvec(2560);

CREATE INDEX IF NOT EXISTS memory_entries_embedding_hnsw_idx
    ON memory_entries USING hnsw (embedding_halfvec halfvec_cosine_ops);
