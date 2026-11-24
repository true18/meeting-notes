CREATE TABLE IF NOT EXISTS users (
    id BIGSERIAL PRIMARY KEY,
    external_id TEXT NOT NULL UNIQUE CHECK (external_id <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS meetings (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    file_path TEXT NOT NULL,
    file_format TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS processing_tasks (
    id BIGSERIAL PRIMARY KEY,
    meeting_id BIGINT NOT NULL UNIQUE REFERENCES meetings(id) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('created', 'processing', 'transcribed', 'summarized', 'completed', 'failed')),
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS transcripts (
    meeting_id BIGINT PRIMARY KEY REFERENCES meetings(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    saved_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS summaries (
    meeting_id BIGINT PRIMARY KEY REFERENCES meetings(id) ON DELETE CASCADE,
    text TEXT NOT NULL,
    saved_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS meetings_user_created_idx ON meetings(user_id, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS processing_tasks_status_created_idx ON processing_tasks(status, created_at, id);
CREATE INDEX IF NOT EXISTS transcripts_search_idx ON transcripts USING GIN (to_tsvector('simple', text));
CREATE INDEX IF NOT EXISTS summaries_search_idx ON summaries USING GIN (to_tsvector('simple', text));
