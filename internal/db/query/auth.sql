-- name: IsInitialized :one
SELECT EXISTS(SELECT 1 FROM users);

-- name: CreateWorkspace :one
INSERT INTO workspaces(name) VALUES ($1) RETURNING id, name;

-- name: CreateAdmin :one
INSERT INTO users(workspace_id,email,password_hash) VALUES ($1,$2,$3) RETURNING id,email;

-- name: FindUser :one
SELECT id,workspace_id,email,password_hash FROM users WHERE email=$1;

-- name: CreateSession :exec
INSERT INTO sessions(token_hash,workspace_id,user_id,csrf_hash,expires_at) VALUES($1,$2,$3,$4,now()+ interval '12 hours');

-- name: GetSession :one
SELECT u.id,u.email,s.workspace_id,w.name AS workspace_name,s.csrf_hash,s.expires_at
FROM sessions s JOIN users u ON s.user_id=u.id AND s.workspace_id=u.workspace_id
JOIN workspaces w ON s.workspace_id=w.id
WHERE s.token_hash=$1 AND s.expires_at>now();

-- name: DeleteSession :exec
DELETE FROM sessions WHERE token_hash=$1;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE workspace_id=$1 AND user_id=$2;

-- name: UpdatePassword :exec
UPDATE users SET password_hash=$3 WHERE workspace_id=$1 AND id=$2;

-- name: Audit :exec
INSERT INTO audit_events(workspace_id,action) VALUES($1,$2);

-- name: CheckRate :one
INSERT INTO auth_rate_limits(bucket,attempts,reset_at) VALUES($1,1,now()+interval '1 minute')
ON CONFLICT(bucket) DO UPDATE SET
 attempts=CASE WHEN auth_rate_limits.reset_at<=now() THEN 1 ELSE auth_rate_limits.attempts+1 END,
 reset_at=CASE WHEN auth_rate_limits.reset_at<=now() THEN now()+interval '1 minute' ELSE auth_rate_limits.reset_at END
RETURNING attempts;

-- name: CleanupAuth :exec
DELETE FROM auth_rate_limits WHERE reset_at < now()-interval '1 hour';

-- name: CleanupSessions :exec
DELETE FROM sessions WHERE expires_at <= now();
