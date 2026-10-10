-- Preserve the original observation while recording a newer complete, nonqualifying scan.
ALTER TABLE automation_items ADD COLUMN cleared_at timestamptz;
