CREATE TABLE inbound_bindings (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL REFERENCES workspaces(id),
 service_id uuid NOT NULL, name text NOT NULL, origin text NOT NULL CHECK(origin IN ('demo','live')),
 enabled boolean NOT NULL DEFAULT false, deleted boolean NOT NULL DEFAULT false,
 revision integer NOT NULL DEFAULT 1, secret bytea NOT NULL,
 per_minute integer NOT NULL DEFAULT 10 CHECK(per_minute BETWEEN 1 AND 60),
 daily_limit integer NOT NULL DEFAULT 10 CHECK(daily_limit BETWEEN 1 AND 100),
 rate_start timestamptz NOT NULL DEFAULT now(), rate_count integer NOT NULL DEFAULT 0,
 created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,service_id) REFERENCES services(workspace_id,id)
);
CREATE TABLE inbound_receipts (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL,
 binding_id uuid NOT NULL, binding_revision integer NOT NULL, policy_revision integer NOT NULL,
 mapping_revision integer NOT NULL, ai_policy_revision integer NOT NULL, provider_revision integer NOT NULL, external_id text NOT NULL, digest text NOT NULL,
 event_origin text NOT NULL DEFAULT 'external' CHECK(event_origin='external'),
 title text, window_start timestamptz NOT NULL, window_end timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','collecting','collected','ai_queued','skipped','cancelled','failed')),
 reason text NOT NULL DEFAULT 'Verified receipt awaiting admission.',
 created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL DEFAULT now()+interval '24 hours',
 deadline timestamptz NOT NULL DEFAULT now()+interval '15 minutes', started_at timestamptz, job_id bigint,
 UNIQUE(workspace_id,id), UNIQUE(binding_id,external_id),
 FOREIGN KEY(workspace_id,binding_id) REFERENCES inbound_bindings(workspace_id,id)
);
ALTER TABLE runs ADD COLUMN inbound_receipt_id uuid;
ALTER TABLE runs ADD CONSTRAINT run_inbound_scope FOREIGN KEY(workspace_id,inbound_receipt_id) REFERENCES inbound_receipts(workspace_id,id) ON DELETE SET NULL (inbound_receipt_id);
CREATE UNIQUE INDEX run_inbound_receipt ON runs(workspace_id,inbound_receipt_id) WHERE inbound_receipt_id IS NOT NULL;
ALTER TABLE ai_runs ADD COLUMN inbound_receipt_id uuid;
ALTER TABLE ai_runs ADD CONSTRAINT ai_inbound_scope FOREIGN KEY(workspace_id,inbound_receipt_id) REFERENCES inbound_receipts(workspace_id,id) ON DELETE SET NULL (inbound_receipt_id);
CREATE UNIQUE INDEX ai_inbound_receipt ON ai_runs(workspace_id,inbound_receipt_id) WHERE inbound_receipt_id IS NOT NULL;
CREATE INDEX inbound_history ON inbound_receipts(workspace_id,created_at DESC,id DESC);
CREATE INDEX inbound_daily ON inbound_receipts(binding_id,started_at) WHERE started_at IS NOT NULL;
