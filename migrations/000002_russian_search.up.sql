DROP INDEX IF EXISTS transcripts_search_idx;
DROP INDEX IF EXISTS summaries_search_idx;

CREATE INDEX transcripts_search_idx ON transcripts USING GIN (to_tsvector('russian', text));
CREATE INDEX summaries_search_idx ON summaries USING GIN (to_tsvector('russian', text));
