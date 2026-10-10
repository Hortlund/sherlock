CREATE TABLE ai_policy (
 workspace_id uuid PRIMARY KEY REFERENCES workspaces(id),revision integer NOT NULL DEFAULT 1,
 enabled boolean NOT NULL DEFAULT false,daily_limit bigint NOT NULL DEFAULT 0,run_limit bigint NOT NULL DEFAULT 0
);
CREATE TABLE ai_providers (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL REFERENCES workspaces(id),
 kind text NOT NULL CHECK(kind IN ('openai','anthropic')),config jsonb NOT NULL,
 revision integer NOT NULL DEFAULT 1,secret bytea NOT NULL,deleted boolean NOT NULL DEFAULT false,
 capability text NOT NULL DEFAULT 'untested',created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,id)
);
CREATE UNIQUE INDEX ai_provider_kind ON ai_providers(workspace_id,kind) WHERE NOT deleted;
CREATE TABLE ai_runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL REFERENCES workspaces(id),
 provider_id uuid NOT NULL,provider_revision integer NOT NULL,policy_revision integer NOT NULL,
 run_id uuid,service_id uuid,purpose text NOT NULL CHECK(purpose IN ('test','analysis')),
 model text NOT NULL,origin text NOT NULL,digest text NOT NULL,prompt_version text NOT NULL DEFAULT 'analysis-v1',
 state text NOT NULL DEFAULT 'queued',reason text NOT NULL DEFAULT '',payload jsonb,report jsonb,
 evidence_ids text[] NOT NULL DEFAULT '{}',reserved bigint NOT NULL,charged bigint NOT NULL DEFAULT 0,
 accounting text NOT NULL DEFAULT 'reserved' CHECK(accounting IN ('reserved','unknown','settled','reconciled')),
 input_bound integer NOT NULL,output_limit integer NOT NULL DEFAULT 2048,
 input_price bigint NOT NULL,output_price bigint NOT NULL,price_date text NOT NULL,
 input_tokens bigint,output_tokens bigint,job_id bigint,request_id text NOT NULL DEFAULT '',
 created_at timestamptz NOT NULL DEFAULT now(),started_at timestamptz,finished_at timestamptz,
 expires_at timestamptz NOT NULL,deadline timestamptz NOT NULL,
 UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,provider_id) REFERENCES ai_providers(workspace_id,id),
 FOREIGN KEY(workspace_id,run_id) REFERENCES runs(workspace_id,id),
 FOREIGN KEY(workspace_id,service_id) REFERENCES services(workspace_id,id)
);
CREATE UNIQUE INDEX ai_active_service ON ai_runs(workspace_id,service_id) WHERE state IN ('queued','analyzing');
CREATE UNIQUE INDEX ai_active_test ON ai_runs(provider_id) WHERE purpose='test' AND state IN ('queued','analyzing');
CREATE INDEX ai_history ON ai_runs(workspace_id,created_at DESC);
CREATE INDEX ai_budget ON ai_runs(workspace_id,created_at,accounting);
CREATE INDEX ai_reuse ON ai_runs(workspace_id,provider_id,digest) WHERE state='completed';
CREATE INDEX ai_expiry ON ai_runs(expires_at) WHERE payload IS NOT NULL OR report IS NOT NULL;
