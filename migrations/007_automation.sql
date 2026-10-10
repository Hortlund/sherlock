CREATE TABLE automation_policy (
 workspace_id uuid PRIMARY KEY REFERENCES workspaces(id),
 config jsonb NOT NULL, revision integer NOT NULL DEFAULT 1,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE automation_checkpoints (
 workspace_id uuid NOT NULL, finding_id uuid NOT NULL,
 measurement jsonb NOT NULL, observed_at timestamptz NOT NULL,policy_revision integer NOT NULL,
 PRIMARY KEY(workspace_id,finding_id),
 FOREIGN KEY(workspace_id,finding_id) REFERENCES findings(workspace_id,id)
);
CREATE TABLE automation_services (
 workspace_id uuid NOT NULL, service_id uuid NOT NULL,
 next_eligible_at timestamptz,
 PRIMARY KEY(workspace_id,service_id),
 FOREIGN KEY(workspace_id,service_id) REFERENCES services(workspace_id,id)
);
CREATE TABLE automation_batches (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL,service_id uuid NOT NULL,
 policy_revision integer NOT NULL,mapping_revision integer NOT NULL,job_id bigint,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','recommended','skipped')),
 reason text NOT NULL DEFAULT 'Waiting for the 60-second grouping window.',
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),due_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '60 seconds',
 evaluated_at timestamptz,next_eligible_at timestamptz,
 UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,service_id) REFERENCES services(workspace_id,id)
);
CREATE UNIQUE INDEX automation_pending_service ON automation_batches(workspace_id,service_id,policy_revision,mapping_revision) WHERE state='pending';
CREATE INDEX automation_history ON automation_batches(workspace_id,created_at DESC,id DESC);
CREATE TABLE automation_items (
 workspace_id uuid NOT NULL,batch_id uuid NOT NULL,finding_id uuid NOT NULL,
 is_new boolean NOT NULL,measurement jsonb NOT NULL,observed_at timestamptz NOT NULL,
 trigger text NOT NULL,evidence_ids jsonb NOT NULL,
 recommendation text NOT NULL DEFAULT 'pending',reason text NOT NULL DEFAULT '',
 PRIMARY KEY(batch_id,finding_id),
 FOREIGN KEY(workspace_id,batch_id) REFERENCES automation_batches(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,finding_id) REFERENCES findings(workspace_id,id)
);
CREATE INDEX automation_items_finding ON automation_items(workspace_id,finding_id);
