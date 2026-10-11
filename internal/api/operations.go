package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sherlock/internal/connectors"
	"sort"
	"time"
)

func (s *Server) connectionLabels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	source, _, e := s.source(ctx, workspace(r), r.PathValue("id"))
	if e != nil {
		s.sourceError(w, r, e)
		return
	}
	path := "/api/v1/labels"
	if source.Kind == "loki" {
		path = "/loki/api/v1/labels"
	}
	end := time.Now().UTC()
	var response connectors.Envelope
	e = s.policy.Get(ctx, source, path, url.Values{"start": {end.Add(-time.Hour).Format(time.RFC3339)}, "end": {end.Format(time.RFC3339)}, "limit": {"100"}}, &response)
	if e != nil {
		s.sourceError(w, r, e)
		return
	}
	var labels []string
	if response.Status != "success" || json.Unmarshal(response.Data, &labels) != nil {
		s.fail(w, r, 400, "invalid_response", "Source did not return label suggestions.")
		return
	}
	out := []string{}
	for _, label := range labels {
		if connectors.LabelName.MatchString(label) && len(label) <= 100 {
			out = append(out, label)
		}
		if len(out) == 100 {
			break
		}
	}
	sort.Strings(out)
	s.json(w, 200, map[string]any{"labels": out, "limited": len(labels) >= 100})
}

// AI runs and automation batches reference each other with ON DELETE SET NULL.
// Serialize their bounded history deletes across replicas before locking either
// table, otherwise opposite delete orders can deadlock through those FKs.
func (s *Server) pruneLinkedAIHistory(ctx context.Context, statement string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(73190005)`); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, statement); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Erase expired payloads while retaining tombstones for finding/report citations.
func (s *Server) Retain(ctx context.Context) error {
	if e := s.retainInbound(ctx); e != nil {
		return e
	}
	if e := s.retainAutomation(ctx); e != nil {
		return e
	}
	if e := s.retainAI(ctx); e != nil {
		return e
	}
	_, e := s.pool.Exec(ctx, `WITH expired AS(SELECT id FROM evidence WHERE expires_at<=now() AND document<>'{}'::jsonb ORDER BY expires_at LIMIT 1000 FOR UPDATE SKIP LOCKED) UPDATE evidence SET document='{}'::jsonb FROM expired WHERE evidence.id=expired.id`)
	if e != nil {
		return e
	}
	_, e = s.pool.Exec(ctx, `WITH expired AS(SELECT id FROM notification_events WHERE expires_at<=now() AND payload<>'{}'::jsonb LIMIT 1000 FOR UPDATE SKIP LOCKED) UPDATE notification_events SET payload='{}'::jsonb FROM expired WHERE notification_events.id=expired.id`)
	if e != nil {
		return e
	}
	_, e = s.pool.Exec(ctx, `WITH expired AS(SELECT id FROM notification_deliveries WHERE expires_at<=now() AND (body IS NOT NULL OR state IN ('pending','retrying')) LIMIT 1000 FOR UPDATE SKIP LOCKED) UPDATE notification_deliveries d SET body=NULL,finished_at=CASE WHEN state IN ('pending','retrying') THEN now() ELSE finished_at END,state=CASE WHEN state IN ('pending','retrying') THEN 'cancelled' ELSE state END,last_error=CASE WHEN state IN ('pending','retrying') THEN 'Delivery content or retry window expired.' ELSE last_error END FROM expired WHERE d.id=expired.id`)
	if e != nil {
		return e
	}
	_, e = s.pool.Exec(ctx, `DELETE FROM notification_deliveries WHERE id IN (SELECT id FROM notification_deliveries WHERE created_at<now()-interval '30 days' LIMIT 1000)`)
	if e != nil {
		return e
	}
	_, e = s.pool.Exec(ctx, `DELETE FROM notification_events WHERE id IN (SELECT e.id FROM notification_events e WHERE created_at<now()-interval '30 days' AND NOT EXISTS(SELECT 1 FROM notification_items i WHERE i.event_id=e.id) LIMIT 1000)`)
	return e
}
func (s *Server) systemHealth(w http.ResponseWriter, r *http.Request) {
	var queued, running, failed int
	e := s.pool.QueryRow(r.Context(), `SELECT count(*) FILTER(WHERE state='queued'),count(*) FILTER(WHERE state IN ('collecting','evaluating','reporting')),count(*) FILTER(WHERE state='failed') FROM runs WHERE workspace_id=$1`, workspace(r)).Scan(&queued, &running, &failed)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]any{"database": "connected", "queued": queued, "running": running, "failed": failed, "worker_limit": 4, "per_connection_limit": 2, "request_limit": 12, "deadline_seconds": 90, "version": "0.1.0"})
}
