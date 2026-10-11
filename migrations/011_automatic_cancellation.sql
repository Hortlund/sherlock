-- Preserve explicit cancellation even when completion won the state transition.
-- Completed triage may still be referenced by pending admission or cached batches.
ALTER TABLE ai_runs ADD COLUMN cancel_requested boolean NOT NULL DEFAULT false;
