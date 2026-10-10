CREATE TABLE workspaces (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    singleton boolean NOT NULL DEFAULT true UNIQUE CHECK (singleton),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    singleton boolean NOT NULL DEFAULT true UNIQUE CHECK (singleton),
    email text NOT NULL UNIQUE,
    password_hash text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id)
);
CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY,
    workspace_id uuid NOT NULL,
    user_id uuid NOT NULL,
    csrf_hash bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    FOREIGN KEY (workspace_id, user_id) REFERENCES users(workspace_id, id) ON DELETE CASCADE
);
CREATE INDEX sessions_expiry ON sessions(expires_at);
CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces(id),
    action text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE installation_secrets (
    id boolean PRIMARY KEY DEFAULT true CHECK (id),
    key_check bytea NOT NULL
);
CREATE TABLE auth_rate_limits (
    bucket text PRIMARY KEY,
    attempts integer NOT NULL,
    reset_at timestamptz NOT NULL
);
