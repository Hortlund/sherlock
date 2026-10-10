ALTER TABLE connections ADD COLUMN last_success_at timestamptz;
CREATE INDEX evidence_retention ON evidence(expires_at) WHERE document<>'{}'::jsonb;
CREATE INDEX runs_service_latest ON runs(workspace_id,service_id,created_at DESC);
