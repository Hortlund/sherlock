package maintenance

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"sherlock/internal/crypto"
)

// Rotate re-encrypts every live credential and the installation sentinel in one transaction.
// Stop application processes before calling; never run against an active installation.
func Rotate(ctx context.Context, pool *pgxpool.Pool, oldKey, newKey []byte) error {
	old, e := crypto.New(oldKey)
	if e != nil {
		return e
	}
	next, e := crypto.New(newKey)
	if e != nil {
		return e
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73190002)`); e != nil {
		return e
	}
	var sentinel []byte
	if e = tx.QueryRow(ctx, `SELECT key_check FROM installation_secrets WHERE id=true FOR UPDATE`).Scan(&sentinel); e != nil {
		return e
	}
	plain, e := old.Open(sentinel, "installation")
	if e != nil || string(plain) != "sherlock-key-check" {
		return errors.New("original encryption key does not match")
	}
	rows, e := tx.Query(ctx, `SELECT id::text,workspace_id::text,credential,'connections' FROM connections WHERE NOT deleted UNION ALL SELECT id::text,workspace_id::text,secret,'notification_destinations' FROM notification_destinations WHERE NOT deleted UNION ALL SELECT id::text,workspace_id::text,secret,'ai_providers' FROM ai_providers WHERE NOT deleted UNION ALL SELECT id::text,workspace_id::text,secret,'inbound_bindings' FROM inbound_bindings WHERE NOT deleted`)
	if e != nil {
		return e
	}
	type secret struct {
		id, ws, table string
		encrypted     []byte
	}
	all := []secret{}
	for rows.Next() {
		var v secret
		if e = rows.Scan(&v.id, &v.ws, &v.encrypted, &v.table); e != nil {
			rows.Close()
			return e
		}
		aad := v.ws + ":" + v.id
		if v.table == "inbound_bindings" {
			aad = v.ws + ":inbound:" + v.id
		}
		if v.table == "notification_destinations" {
			aad = v.ws + ":notification:" + v.id
		}
		if v.table == "ai_providers" {
			aad = v.ws + ":ai:" + v.id
		}
		plain, e = old.Open(v.encrypted, aad)
		if e != nil {
			rows.Close()
			return errors.New("credential validation failed; nothing was changed")
		}
		v.encrypted = next.Seal(plain, aad)
		all = append(all, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, v := range all {
		query := `UPDATE connections SET credential=$3 WHERE workspace_id=$1 AND id=$2`
		if v.table == "inbound_bindings" {
			query = `UPDATE inbound_bindings SET secret=$3 WHERE workspace_id=$1 AND id=$2`
		}
		if v.table == "notification_destinations" {
			query = `UPDATE notification_destinations SET secret=$3 WHERE workspace_id=$1 AND id=$2`
		}
		if v.table == "ai_providers" {
			query = `UPDATE ai_providers SET secret=$3 WHERE workspace_id=$1 AND id=$2`
		}
		if _, e = tx.Exec(ctx, query, v.ws, v.id, v.encrypted); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE installation_secrets SET key_check=$1 WHERE id=true`, next.Seal([]byte("sherlock-key-check"), "installation")); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO audit_events(workspace_id,action) SELECT id,'security.key_rotated' FROM workspaces`); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
