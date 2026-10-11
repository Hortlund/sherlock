ALTER TABLE ai_runs ADD COLUMN tools_enabled boolean NOT NULL DEFAULT false;
ALTER TABLE ai_runs ADD COLUMN parent_ai_run_id uuid;
ALTER TABLE ai_runs ADD CONSTRAINT ai_parent_scope FOREIGN KEY(workspace_id,parent_ai_run_id) REFERENCES ai_runs(workspace_id,id) ON DELETE SET NULL (parent_ai_run_id);
ALTER TABLE ai_runs ADD COLUMN question text;
ALTER TABLE ai_runs ADD COLUMN deep boolean NOT NULL DEFAULT false;
ALTER TABLE ai_runs ADD COLUMN base_digest text NOT NULL DEFAULT '';
ALTER TABLE ai_runs ADD COLUMN telemetry_attempts integer NOT NULL DEFAULT 0;
CREATE TABLE ai_steps (
 workspace_id uuid NOT NULL, ai_run_id uuid NOT NULL, sequence integer NOT NULL CHECK(sequence BETWEEN 1 AND 13),
 kind text NOT NULL CHECK(kind IN ('provider','evidence','prometheus_counts','loki_patterns')),
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','running','completed','failed','unknown','cancelled')),
 reason text NOT NULL DEFAULT 'Waiting for execution.',
 payload jsonb, result jsonb, evidence_ids text[] NOT NULL DEFAULT '{}',
 input_bound integer NOT NULL DEFAULT 0, output_limit integer NOT NULL DEFAULT 0,
 telemetry_limit integer NOT NULL DEFAULT 0, reserved bigint NOT NULL DEFAULT 0,
 charged bigint NOT NULL DEFAULT 0, input_tokens bigint, output_tokens bigint,
 accounting text NOT NULL DEFAULT 'reserved' CHECK(accounting IN ('reserved','settled','unknown')),
 request_id text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), started_at timestamptz, finished_at timestamptz,
 PRIMARY KEY(workspace_id,ai_run_id,sequence),
 FOREIGN KEY(workspace_id,ai_run_id) REFERENCES ai_runs(workspace_id,id) ON DELETE CASCADE
);
-- All existing policy/privacy deletion paths erase new content as well. A
-- cancellation between settled steps releases only undispatched reservations.
CREATE FUNCTION ai_step_privacy() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.deep THEN
  IF NEW.cancel_requested OR NEW.state='cancelled' OR NEW.expires_at<=now() OR (OLD.report IS NOT NULL AND NEW.report IS NULL) THEN
   NEW.question := NULL;
   UPDATE ai_steps SET payload=NULL,result=NULL,
    state=CASE WHEN state='queued' THEN 'cancelled' ELSE state END,
    accounting=CASE WHEN state='queued' THEN 'settled' ELSE accounting END,
    reason=CASE WHEN state='queued' THEN 'Processing stopped before dispatch.' ELSE reason END,
    finished_at=CASE WHEN state='queued' THEN now() ELSE finished_at END
   WHERE workspace_id=NEW.workspace_id AND ai_run_id=NEW.id;
  END IF;
  IF NEW.state NOT IN ('queued','analyzing') THEN
   UPDATE ai_steps SET state=CASE WHEN state='running' THEN 'unknown' ELSE 'cancelled' END, accounting=CASE WHEN state='queued' THEN 'settled' ELSE accounting END, payload=NULL,reason=NEW.reason,finished_at=now() WHERE workspace_id=NEW.workspace_id AND ai_run_id=NEW.id AND state IN ('queued','running');
  END IF;
  IF NEW.state NOT IN ('queued','analyzing') AND NOT EXISTS(SELECT 1 FROM ai_steps WHERE workspace_id=NEW.workspace_id AND ai_run_id=NEW.id AND accounting='unknown') THEN
   NEW.accounting := CASE WHEN NEW.accounting='reconciled' THEN 'reconciled' ELSE 'settled' END;
  END IF;
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER ai_step_privacy BEFORE UPDATE ON ai_runs FOR EACH ROW EXECUTE FUNCTION ai_step_privacy();
CREATE INDEX ai_step_run ON ai_steps(ai_run_id);
CREATE INDEX ai_parent ON ai_runs(workspace_id,parent_ai_run_id) WHERE parent_ai_run_id IS NOT NULL;
CREATE INDEX ai_deep_reuse ON ai_runs(workspace_id,provider_id,base_digest) WHERE deep AND state='completed';
