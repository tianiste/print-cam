CREATE TABLE IF NOT EXISTS rate_limit_events (
    id TEXT PRIMARY KEY,
    key TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS rate_limit_events_key_created_at_idx ON rate_limit_events(key, created_at);
CREATE INDEX IF NOT EXISTS rate_limit_events_created_at_idx ON rate_limit_events(created_at);
