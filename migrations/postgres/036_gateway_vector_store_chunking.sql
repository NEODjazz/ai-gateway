ALTER TABLE gateway_vector_store_files
    ADD COLUMN IF NOT EXISTS chunking_type TEXT NOT NULL DEFAULT 'auto',
    ADD COLUMN IF NOT EXISTS max_chunk_size_tokens INTEGER,
    ADD COLUMN IF NOT EXISTS chunk_overlap_tokens INTEGER;

ALTER TABLE gateway_vector_store_files
    DROP CONSTRAINT IF EXISTS gateway_vector_store_files_chunking_check;

ALTER TABLE gateway_vector_store_files
    ADD CONSTRAINT gateway_vector_store_files_chunking_check CHECK (
        (chunking_type = 'auto' AND max_chunk_size_tokens IS NULL AND chunk_overlap_tokens IS NULL)
        OR
        (chunking_type = 'static'
            AND max_chunk_size_tokens BETWEEN 100 AND 4096
            AND chunk_overlap_tokens BETWEEN 0 AND max_chunk_size_tokens / 2)
    );
