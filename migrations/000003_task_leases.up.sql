ALTER TABLE processing_tasks
    ADD COLUMN attempt BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN lease_expires_at TIMESTAMPTZ;

UPDATE processing_tasks
SET lease_expires_at = started_at + INTERVAL '90 seconds'
WHERE status IN ('processing', 'transcribed') AND started_at IS NOT NULL;

CREATE INDEX processing_tasks_lease_idx
    ON processing_tasks(lease_expires_at)
    WHERE status IN ('processing', 'transcribed');
