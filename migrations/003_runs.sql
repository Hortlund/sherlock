CREATE TABLE workspace_settings (
 workspace_id uuid PRIMARY KEY REFERENCES workspaces(id),
 settings jsonb NOT NULL DEFAULT '{}'
);
ALTER TABLE services ADD COLUMN next_scan_at timestamptz NOT NULL DEFAULT now();
CREATE TABLE runs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL REFERENCES workspaces(id),
 service_id uuid NOT NULL, kind text NOT NULL CHECK(kind IN ('scan','investigation')),
 state text NOT NULL DEFAULT 'queued',snapshot jsonb NOT NULL,config jsonb NOT NULL,
 window_start timestamptz NOT NULL,window_end timestamptz NOT NULL,
 scheduled boolean NOT NULL DEFAULT false,job_id bigint,requests integer NOT NULL DEFAULT 0,
 deadline timestamptz,created_at timestamptz NOT NULL DEFAULT now(),finished_at timestamptz,
 result jsonb NOT NULL DEFAULT '{}',cancelled boolean NOT NULL DEFAULT false,
 UNIQUE(workspace_id,id),FOREIGN KEY(workspace_id,service_id) REFERENCES services(workspace_id,id)
);
CREATE UNIQUE INDEX one_active_run ON runs(workspace_id,service_id,kind) WHERE state IN ('queued','collecting','evaluating','reporting');
CREATE TABLE run_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,workspace_id uuid NOT NULL,run_id uuid NOT NULL,
 stage text NOT NULL,message text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(run_id,stage),FOREIGN KEY(workspace_id,run_id) REFERENCES runs(workspace_id,id)
);
CREATE TABLE findings (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL,service_id uuid NOT NULL,
 mapping_revision integer NOT NULL,origin text NOT NULL,detector text NOT NULL,signal text NOT NULL,
 occurrence integer NOT NULL, state text NOT NULL DEFAULT 'new',severity text NOT NULL,
 title text NOT NULL,description text NOT NULL,details jsonb NOT NULL,
 first_seen timestamptz NOT NULL,last_seen timestamptz NOT NULL,closed_at timestamptz,snoozed_until timestamptz,
 feedback text NOT NULL DEFAULT '',
 UNIQUE(workspace_id,id),UNIQUE(workspace_id,service_id,mapping_revision,detector,signal,occurrence),
 FOREIGN KEY(workspace_id,service_id) REFERENCES services(workspace_id,id)
);
CREATE INDEX findings_list ON findings(workspace_id,last_seen DESC);
CREATE TABLE finding_events (
 id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,workspace_id uuid NOT NULL,finding_id uuid NOT NULL,
 state text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 FOREIGN KEY(workspace_id,finding_id) REFERENCES findings(workspace_id,id)
);
