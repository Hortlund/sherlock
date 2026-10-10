CREATE TABLE connections (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL REFERENCES workspaces(id),
 name text NOT NULL, kind text NOT NULL CHECK(kind IN ('prometheus','loki')), base_url text NOT NULL,
 auth_mode text NOT NULL CHECK(auth_mode IN ('none','bearer','basic')), credential bytea NOT NULL,
 tenant text NOT NULL DEFAULT '', ca_pem text NOT NULL DEFAULT '',
 origin text NOT NULL DEFAULT 'live' CHECK(origin IN ('live','demo')),
 enabled boolean NOT NULL DEFAULT true, deleted boolean NOT NULL DEFAULT false,
 revision integer NOT NULL DEFAULT 1, status text NOT NULL DEFAULT 'untested', last_error text NOT NULL DEFAULT '',
 last_test_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(workspace_id,id)
);
CREATE INDEX connections_workspace ON connections(workspace_id,created_at,id) WHERE NOT deleted;
CREATE TABLE services (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL REFERENCES workspaces(id),
 name text NOT NULL, environment text NOT NULL, origin text NOT NULL CHECK(origin IN ('live','demo')),
 prometheus_id uuid, loki_id uuid, mapping jsonb NOT NULL, revision integer NOT NULL DEFAULT 1,
 monitoring boolean NOT NULL DEFAULT false, created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,prometheus_id) REFERENCES connections(workspace_id,id),
 FOREIGN KEY(workspace_id,loki_id) REFERENCES connections(workspace_id,id)
);
CREATE INDEX services_workspace ON services(workspace_id,created_at,id);
CREATE TABLE evidence (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), workspace_id uuid NOT NULL, service_id uuid NOT NULL,
 connection_id uuid NOT NULL, origin text NOT NULL CHECK(origin IN ('live','demo')), mapping_revision integer NOT NULL,
 kind text NOT NULL, window_start timestamptz NOT NULL, window_end timestamptz NOT NULL,
 collected_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL DEFAULT now()+interval '7 days',
 document jsonb NOT NULL, UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,service_id) REFERENCES services(workspace_id,id),
 FOREIGN KEY(workspace_id,connection_id) REFERENCES connections(workspace_id,id)
);
CREATE INDEX evidence_service_time ON evidence(workspace_id,service_id,collected_at DESC);
CREATE INDEX evidence_expiry ON evidence(expires_at);
