CREATE TABLE notification_destinations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL REFERENCES workspaces(id),
 name text NOT NULL, kind text NOT NULL CHECK(kind IN ('slack','discord','webhook')),
 enabled boolean NOT NULL DEFAULT false, deleted boolean NOT NULL DEFAULT false,
 revision integer NOT NULL DEFAULT 1, rules jsonb NOT NULL, secret bytea NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 next_send_at timestamptz NOT NULL DEFAULT now(), created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,id)
);
CREATE TABLE notification_events (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL REFERENCES workspaces(id),
 event_key text NOT NULL,kind text NOT NULL,service_id uuid, finding_id uuid,run_id uuid,
 origin text NOT NULL, severity text NOT NULL, payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL,
 UNIQUE(workspace_id,id),UNIQUE(workspace_id,event_key),
 FOREIGN KEY(workspace_id,service_id) REFERENCES services(workspace_id,id),
 FOREIGN KEY(workspace_id,finding_id) REFERENCES findings(workspace_id,id),
 FOREIGN KEY(workspace_id,run_id) REFERENCES runs(workspace_id,id)
);
CREATE TABLE notification_deliveries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),workspace_id uuid NOT NULL,destination_id uuid NOT NULL,
 revision integer NOT NULL,service_key text NOT NULL, state text NOT NULL DEFAULT 'pending'
 CHECK(state IN ('pending','sending','retrying','sent','failed','cancelled')),
 test boolean NOT NULL DEFAULT false,body bytea,attempts integer NOT NULL DEFAULT 0,
 last_error text NOT NULL DEFAULT '', message_id text NOT NULL DEFAULT '',
 due_at timestamptz NOT NULL,expires_at timestamptz NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),finished_at timestamptz,
 UNIQUE(workspace_id,id), UNIQUE(workspace_id,id,destination_id),
 FOREIGN KEY(workspace_id,destination_id) REFERENCES notification_destinations(workspace_id,id)
);
CREATE INDEX notification_delivery_history ON notification_deliveries(workspace_id,created_at DESC,id DESC);
CREATE INDEX notification_open_batches ON notification_deliveries(destination_id,service_key,due_at) WHERE state='pending' AND body IS NULL;
CREATE TABLE notification_items (
 workspace_id uuid NOT NULL,delivery_id uuid NOT NULL,destination_id uuid NOT NULL,event_id uuid NOT NULL,
 excluded boolean NOT NULL DEFAULT false,
 PRIMARY KEY(destination_id,event_id),
 FOREIGN KEY(workspace_id,delivery_id,destination_id) REFERENCES notification_deliveries(workspace_id,id,destination_id) ON DELETE CASCADE,
 FOREIGN KEY(workspace_id,event_id) REFERENCES notification_events(workspace_id,id) ON DELETE CASCADE
);
CREATE TABLE notification_attempts (
 workspace_id uuid NOT NULL,delivery_id uuid NOT NULL,number integer NOT NULL,
 started_at timestamptz NOT NULL DEFAULT now(),finished_at timestamptz,
 outcome text NOT NULL DEFAULT 'unknown',status_code integer NOT NULL DEFAULT 0,
 detail text NOT NULL DEFAULT 'Dispatch started; acknowledgement not yet recorded.',
 PRIMARY KEY(delivery_id,number),
 FOREIGN KEY(workspace_id,delivery_id) REFERENCES notification_deliveries(workspace_id,id) ON DELETE CASCADE
);

CREATE INDEX notification_items_delivery ON notification_items(delivery_id);
CREATE INDEX notification_items_event ON notification_items(event_id);
CREATE INDEX notification_events_expiry ON notification_events(expires_at) WHERE payload<>'{}'::jsonb;
CREATE INDEX notification_events_age ON notification_events(created_at);
CREATE INDEX notification_deliveries_expiry ON notification_deliveries(expires_at) WHERE body IS NOT NULL OR state IN ('pending','retrying');
