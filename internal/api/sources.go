package api

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"net/http"
	"reflect"
	"sherlock/internal/connectors"
	store "sherlock/internal/db/sqlc"
	"strings"
	"time"
)

type Connection struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	Kind         string     `json:"kind"`
	URL          string     `json:"base_url"`
	AuthMode     string     `json:"auth_mode"`
	Tenant       string     `json:"tenant"`
	CAPEM        string     `json:"ca_pem"`
	Origin       string     `json:"origin"`
	Enabled      bool       `json:"enabled"`
	Revision     int        `json:"revision"`
	Status       string     `json:"status"`
	LastError    string     `json:"last_error"`
	LastSuccess  *time.Time `json:"last_success_at"`
	Capabilities []string   `json:"capabilities"`
	LastTest     *time.Time `json:"last_test_at"`
}
type ConnectionInput struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	URL      string `json:"base_url"`
	AuthMode string `json:"auth_mode"`
	Username string `json:"username"`
	Secret   string `json:"secret"`
	Tenant   string `json:"tenant"`
	CAPEM    string `json:"ca_pem"`
}
type Service struct {
	ID            string             `json:"id"`
	Name          string             `json:"name"`
	Environment   string             `json:"environment"`
	Origin        string             `json:"origin"`
	PrometheusID  string             `json:"prometheus_id"`
	LokiID        string             `json:"loki_id"`
	Mapping       connectors.Mapping `json:"mapping"`
	Revision      int                `json:"revision"`
	LastScanID    string             `json:"last_scan_id,omitempty"`
	LastScanState string             `json:"last_scan_state,omitempty"`
	Monitoring    bool               `json:"monitoring"`
}
type Evidence struct {
	SourceAvailable bool                   `json:"source_available"`
	ID              string                 `json:"id"`
	ServiceID       string                 `json:"service_id"`
	ConnectionID    string                 `json:"connection_id"`
	Origin          string                 `json:"origin"`
	MappingRevision int                    `json:"mapping_revision"`
	CollectedAt     time.Time              `json:"collected_at"`
	ExpiresAt       time.Time              `json:"expires_at"`
	Observation     connectors.Observation `json:"observation"`
}

func workspace(r *http.Request) string {
	return r.Context().Value(sessionKey).(store.GetSessionRow).WorkspaceID.String()
}
func (s *Server) sourceRoutes(mux *http.ServeMux) {
	for path, handler := range map[string]http.HandlerFunc{
		"GET /api/v1/connections": s.listConnections, "POST /api/v1/connections": s.saveConnection, "POST /api/v1/connections/{id}": s.saveConnection,
		"POST /api/v1/connections/{id}/test": s.testConnection, "GET /api/v1/connections/{id}/labels": s.connectionLabels, "POST /api/v1/connections/{id}/state": s.connectionState,
		"POST /api/v1/demo": s.setupDemo, "GET /api/v1/services": s.listServices, "POST /api/v1/services": s.saveService, "POST /api/v1/services/{id}": s.saveService,
		"POST /api/v1/services/{id}/preview": s.preview, "GET /api/v1/evidence/{id}": s.getEvidence,
	} {
		mux.Handle(path, s.requireSession(handler))
	}
}

const connectionColumns = `id::text,name,kind,base_url,auth_mode,tenant,ca_pem,origin,enabled,revision,status,last_error,last_test_at,last_success_at`

func scanConnection(row pgx.Row) (Connection, error) {
	var c Connection
	err := row.Scan(&c.ID, &c.Name, &c.Kind, &c.URL, &c.AuthMode, &c.Tenant, &c.CAPEM, &c.Origin, &c.Enabled, &c.Revision, &c.Status, &c.LastError, &c.LastTest, &c.LastSuccess)
	c.Capabilities = []string{}
	if c.LastSuccess != nil {
		if c.Kind == "prometheus" {
			c.Capabilities = []string{"instant_query"}
		} else {
			c.Capabilities = []string{"label_names"}
		}
	}
	return c, err
}
func (s *Server) connection(ctx context.Context, ws, id string) (Connection, error) {
	return scanConnection(s.pool.QueryRow(ctx, `SELECT `+connectionColumns+` FROM connections WHERE workspace_id=$1 AND id::text=$2 AND NOT deleted`, ws, id))
}
func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `SELECT `+connectionColumns+` FROM connections WHERE workspace_id=$1 AND NOT deleted AND id::text>$2 ORDER BY id::text LIMIT 100`, workspace(r), r.URL.Query().Get("cursor"))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	items := []Connection{}
	for rows.Next() {
		c, e := scanConnection(rows)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		items = append(items, c)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	s.json(w, 200, listResponse(items, func(c Connection) string { return c.ID }))
}
func listResponse[T any](items []T, id func(T) string) map[string]any {
	cursor := ""
	if len(items) == 100 {
		cursor = id(items[99])
	}
	return map[string]any{"items": items, "next_cursor": cursor}
}
func (s *Server) saveConnection(w http.ResponseWriter, r *http.Request) {
	var in ConnectionInput
	if !s.decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if len(in.Name) < 1 || len(in.Name) > 80 || (in.Kind != "prometheus" && in.Kind != "loki") || (in.AuthMode != "none" && in.AuthMode != "bearer" && in.AuthMode != "basic") || len(in.Secret) > 4096 || len(in.Username) > 200 || len(in.Tenant) > 200 || strings.ContainsAny(in.Tenant, "\r\n") || len(in.CAPEM) > 8192 {
		s.fail(w, r, 400, "invalid_input", "Check source name, type, authentication, and field lengths.")
		return
	}
	if err := connectors.ValidateURL(in.URL); err != nil {
		s.fail(w, r, 400, "invalid_url", err.Error())
		return
	}
	if in.CAPEM != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(in.CAPEM)) {
		s.fail(w, r, 400, "invalid_ca", "Provide a PEM-encoded CA certificate.")
		return
	}
	ws, id := workspace(r), r.PathValue("id")
	creds := connectors.Credential{Username: in.Username, Secret: in.Secret}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT id FROM workspaces WHERE id=$1 FOR UPDATE`, ws); err != nil {
		s.internal(w, r, err)
		return
	}
	if id != "" {
		old, e := scanConnection(tx.QueryRow(r.Context(), `SELECT `+connectionColumns+` FROM connections WHERE workspace_id=$1 AND id::text=$2 AND NOT deleted FOR UPDATE`, ws, id))
		if e != nil {
			s.missing(w, r, e)
			return
		}
		if old.Kind != in.Kind {
			s.fail(w, r, 400, "immutable_kind", "Create a new connection to change source type.")
			return
		}
		if old.Origin == "demo" {
			s.fail(w, r, 400, "demo_read_only", "Demo connection settings are managed by Sherlock.")
			return
		}
		if in.AuthMode != "none" && in.Secret == "" && in.AuthMode == old.AuthMode {
			var encrypted []byte
			if e = tx.QueryRow(r.Context(), `SELECT credential FROM connections WHERE workspace_id=$1 AND id::text=$2`, ws, id).Scan(&encrypted); e != nil {
				s.internal(w, r, e)
				return
			}
			plain, e := s.vault.Open(encrypted, ws+":"+id)
			if e != nil || json.Unmarshal(plain, &creds) != nil {
				s.fail(w, r, 500, "credential_unavailable", "Stored credentials could not be read.")
				return
			}
			if in.Username != "" {
				creds.Username = in.Username
			}
		}
	} else {
		var count int
		if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM connections WHERE workspace_id=$1 AND NOT deleted`, ws).Scan(&count); err != nil {
			s.internal(w, r, err)
			return
		}
		if count >= 100 {
			s.fail(w, r, 400, "connection_limit", "This installation supports at most 100 active source configurations.")
			return
		}
		if err = tx.QueryRow(r.Context(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
			s.internal(w, r, err)
			return
		}
	}
	if in.AuthMode != "none" && (creds.Secret == "" || (in.AuthMode == "basic" && creds.Username == "")) {
		s.fail(w, r, 400, "missing_credentials", "Provide the source credentials.")
		return
	}
	if in.AuthMode == "none" {
		creds = connectors.Credential{}
	}
	plain, _ := json.Marshal(creds)
	encrypted := s.vault.Seal(plain, ws+":"+id)
	if r.PathValue("id") == "" {
		_, err = tx.Exec(r.Context(), `INSERT INTO connections(id,workspace_id,name,kind,base_url,auth_mode,credential,tenant,ca_pem) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, ws, in.Name, in.Kind, in.URL, in.AuthMode, encrypted, in.Tenant, in.CAPEM)
	} else {
		_, err = tx.Exec(r.Context(), `UPDATE connections SET name=$3,kind=$4,base_url=$5,auth_mode=$6,credential=$7,tenant=$8,ca_pem=$9,status='untested',last_error='',last_test_at=NULL,revision=revision+1 WHERE workspace_id=$1 AND id::text=$2`, ws, id, in.Name, in.Kind, in.URL, in.AuthMode, encrypted, in.Tenant, in.CAPEM)
	}
	if err == nil && r.PathValue("id") != "" {
		_, err = tx.Exec(r.Context(), `UPDATE services SET revision=revision+1 WHERE workspace_id=$1 AND (prometheus_id=$2::uuid OR loki_id=$2::uuid)`, ws, id)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_events(workspace_id,action) VALUES($1,'connection.saved')`, ws)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	c, err := s.connection(r.Context(), ws, id)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.json(w, 200, c)
}
func (s *Server) source(ctx context.Context, ws, id string) (connectors.Source, Connection, error) {
	c, e := s.connection(ctx, ws, id)
	if e != nil {
		return connectors.Source{}, c, e
	}
	if !c.Enabled {
		return connectors.Source{}, c, &connectors.Error{Code: "disabled", Message: "Source is disabled."}
	}
	var data []byte
	e = s.pool.QueryRow(ctx, `SELECT credential FROM connections WHERE workspace_id=$1 AND id::text=$2 AND enabled AND NOT deleted AND revision=$3`, ws, id, c.Revision).Scan(&data)
	if e != nil {
		return connectors.Source{}, c, e
	}
	plain, e := s.vault.Open(data, ws+":"+id)
	if e != nil {
		return connectors.Source{}, c, e
	}
	var credential connectors.Credential
	if e = json.Unmarshal(plain, &credential); e != nil {
		return connectors.Source{}, c, e
	}
	return connectors.Source{ID: id, Kind: c.Kind, URL: c.URL, AuthMode: c.AuthMode, Tenant: c.Tenant, CAPEM: c.CAPEM, Credential: credential, Before: func(ctx context.Context) error {
		var valid bool
		e := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM connections WHERE workspace_id=$1 AND id::text=$2 AND revision=$3 AND enabled AND NOT deleted)`, ws, id, c.Revision).Scan(&valid)
		if e != nil {
			return e
		}
		if !valid {
			return &connectors.Error{Code: "disabled", Message: "Source changed or was disabled."}
		}
		return nil
	}}, c, nil
}
func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	ws := workspace(r)
	source, c, err := s.source(ctx, ws, r.PathValue("id"))
	if err != nil {
		s.sourceError(w, r, err)
		return
	}
	err = s.policy.Test(ctx, source)
	status, message := "connected", ""
	if err != nil {
		status = "disconnected"
		_, message = connectors.SafeError(err)
	}
	saveCtx, saveCancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer saveCancel()
	tag, e := s.pool.Exec(saveCtx, `UPDATE connections SET status=$3,last_error=$4,last_test_at=now(),last_success_at=CASE WHEN $3='connected' THEN now() ELSE last_success_at END WHERE workspace_id=$1 AND id::text=$2 AND enabled AND NOT deleted AND revision=$5`, ws, c.ID, status, message, c.Revision)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if tag.RowsAffected() == 0 {
		s.fail(w, r, 409, "connection_changed", "Connection changed during the test. Try again.")
		return
	}
	c, e = s.connection(saveCtx, ws, c.ID)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, c)
}
func (s *Server) connectionState(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action string `json:"action"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Action != "enable" && in.Action != "disable" && in.Action != "delete" {
		s.fail(w, r, 400, "invalid_action", "Choose enable, disable, or delete.")
		return
	}
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	status := "disabled"
	if in.Action == "enable" {
		status = "untested"
	}
	tag, e := tx.Exec(r.Context(), `UPDATE connections SET enabled=$3,deleted=$4,status=$5,revision=revision+1,credential=CASE WHEN $4 THEN ''::bytea ELSE credential END WHERE workspace_id=$1 AND id::text=$2 AND NOT deleted`, workspace(r), r.PathValue("id"), in.Action == "enable", in.Action == "delete", status)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	if tag.RowsAffected() == 0 {
		s.fail(w, r, 404, "not_found", "Connection not found.")
		return
	}
	_, e = tx.Exec(r.Context(), `INSERT INTO audit_events(workspace_id,action) VALUES($1,$2)`, workspace(r), "connection."+in.Action)
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, map[string]bool{"ok": true})
}

const serviceColumns = `id::text,name,environment,origin,COALESCE(prometheus_id::text,''),COALESCE(loki_id::text,''),mapping,revision,monitoring`

func scanService(row pgx.Row) (Service, error) {
	var v Service
	var raw []byte
	e := row.Scan(&v.ID, &v.Name, &v.Environment, &v.Origin, &v.PrometheusID, &v.LokiID, &raw, &v.Revision, &v.Monitoring)
	if e == nil {
		e = json.Unmarshal(raw, &v.Mapping)
	}
	return v, e
}
func (s *Server) service(ctx context.Context, ws, id string) (Service, error) {
	return scanService(s.pool.QueryRow(ctx, `SELECT `+serviceColumns+` FROM services WHERE workspace_id=$1 AND id::text=$2`, ws, id))
}
func (s *Server) listServices(w http.ResponseWriter, r *http.Request) {
	rows, e := s.pool.Query(r.Context(), `SELECT `+serviceColumns+` FROM services WHERE workspace_id=$1 AND id::text>$2 ORDER BY id::text LIMIT 100`, workspace(r), r.URL.Query().Get("cursor"))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer rows.Close()
	items := []Service{}
	for rows.Next() {
		v, e := scanService(rows)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		items = append(items, v)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	rows.Close()
	latest, e := s.pool.Query(r.Context(), `SELECT DISTINCT ON(service_id) service_id::text,id::text,state FROM runs WHERE workspace_id=$1 AND kind='scan' ORDER BY service_id,created_at DESC`, workspace(r))
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer latest.Close()
	for latest.Next() {
		var sid, id, state string
		if e = latest.Scan(&sid, &id, &state); e != nil {
			s.internal(w, r, e)
			return
		}
		for i := range items {
			if items[i].ID == sid {
				items[i].LastScanID = id
				items[i].LastScanState = state
			}
		}
	}
	if latest.Err() != nil {
		s.internal(w, r, latest.Err())
		return
	}
	s.json(w, 200, listResponse(items, func(v Service) string { return v.ID }))
}
func (s *Server) saveService(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name          string             `json:"name"`
		Environment   string             `json:"environment"`
		PrometheusID  string             `json:"prometheus_id"`
		LokiID        string             `json:"loki_id"`
		Mapping       connectors.Mapping `json:"mapping"`
		LastScanID    string             `json:"last_scan_id,omitempty"`
		LastScanState string             `json:"last_scan_state,omitempty"`
		Monitoring    bool               `json:"monitoring"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if len(strings.TrimSpace(in.Name)) < 1 || len(in.Name) > 80 || len(in.Environment) < 1 || len(in.Environment) > 80 || (in.PrometheusID == "" && in.LokiID == "") {
		s.fail(w, r, 400, "invalid_service", "Provide a service name, environment, and at least one source.")
		return
	}
	if e := in.Mapping.Validate(in.PrometheusID != "", in.LokiID != ""); e != nil {
		s.fail(w, r, 400, "invalid_mapping", e.Error())
		return
	}
	ws, id := workspace(r), r.PathValue("id")
	origin := ""
	for _, pair := range []struct{ id, kind string }{{in.PrometheusID, "prometheus"}, {in.LokiID, "loki"}} {
		if pair.id == "" {
			continue
		}
		c, e := s.connection(r.Context(), ws, pair.id)
		if e != nil {
			s.missing(w, r, e)
			return
		}
		if !c.Enabled || c.Kind != pair.kind {
			s.fail(w, r, 400, "invalid_source", "Choose an enabled source of the correct type.")
			return
		}
		if origin != "" && origin != c.Origin {
			s.fail(w, r, 400, "mixed_origin", "Demo and live sources cannot share a service.")
			return
		}
		origin = c.Origin
	}
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), `SELECT id FROM workspaces WHERE id=$1 FOR UPDATE`, ws); e != nil {
		s.internal(w, r, e)
		return
	}
	for _, pair := range []struct{ id, kind string }{{in.PrometheusID, "prometheus"}, {in.LokiID, "loki"}} {
		if pair.id == "" {
			continue
		}
		var sourceID string
		e = tx.QueryRow(r.Context(), `SELECT id::text FROM connections WHERE workspace_id=$1 AND id::text=$2 AND kind=$3 AND origin=$4 AND enabled AND NOT deleted FOR SHARE`, ws, pair.id, pair.kind, origin).Scan(&sourceID)
		if e != nil {
			s.fail(w, r, 409, "source_changed", "Source changed while saving. Refresh and try again.")
			return
		}
	}
	if id == "" {
		var count int
		if e = tx.QueryRow(r.Context(), `SELECT count(*) FROM services WHERE workspace_id=$1`, ws).Scan(&count); e != nil {
			s.internal(w, r, e)
			return
		}
		if count >= 100 {
			s.fail(w, r, 400, "service_limit", "This MVP supports up to 100 mapped services.")
			return
		}
	}
	in.Mapping = in.Mapping.Canonical(in.PrometheusID != "", in.LokiID != "")
	raw, _ := json.Marshal(in.Mapping)
	if id == "" {
		e = tx.QueryRow(r.Context(), `INSERT INTO services(workspace_id,name,environment,origin,prometheus_id,loki_id,mapping,monitoring) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,NULLIF($6,'')::uuid,$7,$8) RETURNING id::text`, ws, in.Name, in.Environment, origin, in.PrometheusID, in.LokiID, raw, in.Monitoring).Scan(&id)
	} else {
		old, err := scanService(tx.QueryRow(r.Context(), `SELECT `+serviceColumns+` FROM services WHERE workspace_id=$1 AND id::text=$2 AND origin=$3 FOR UPDATE`, ws, id, origin))
		if err != nil {
			s.missing(w, r, err)
			return
		}
		changed := old.PrometheusID != in.PrometheusID || old.LokiID != in.LokiID || !reflect.DeepEqual(old.Mapping.Canonical(old.PrometheusID != "", old.LokiID != ""), in.Mapping)
		revision := old.Revision
		if changed {
			revision++
		}
		tag, err := tx.Exec(r.Context(), `UPDATE services SET name=$3,environment=$4,origin=$5,prometheus_id=NULLIF($6,'')::uuid,loki_id=NULLIF($7,'')::uuid,mapping=$8,monitoring=$9,revision=$10 WHERE workspace_id=$1 AND id::text=$2 AND origin=$5`, ws, id, in.Name, in.Environment, origin, in.PrometheusID, in.LokiID, raw, in.Monitoring, revision)
		e = err
		if e == nil && tag.RowsAffected() == 0 {
			e = pgx.ErrNoRows
		}
	}
	if e == nil && !in.Monitoring {
		e = cancelInboundServiceReceipts(r.Context(), tx, ws, id)
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO audit_events(workspace_id,action) VALUES($1,'service.mapping_saved')`, ws)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		s.missing(w, r, e)
		return
	}
	v, e := s.service(r.Context(), ws, id)
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, v)
}
func (s *Server) collect(ctx context.Context, ws string, v Service, start, end time.Time) ([]Evidence, []string) {
	settings, settingsErr := s.settings(ctx, ws)
	if settingsErr != nil {
		return nil, []string{"Workspace collection settings unavailable."}
	}
	observations := []Evidence{}
	issues := []string{}
	for _, id := range []string{v.PrometheusID, v.LokiID} {
		if id == "" {
			continue
		}
		source, c, e := s.source(ctx, ws, id)
		if e != nil {
			_, msg := connectors.SafeError(e)
			issues = append(issues, msg)
			continue
		}
		if c.Origin != v.Origin {
			issues = append(issues, "Source origin does not match service.")
			continue
		}
		var results []connectors.Observation
		if c.Kind == "prometheus" {
			for _, errorsOnly := range []bool{false, true} {
				o, err := s.policy.Count(ctx, source, v.Mapping, start, end, errorsOnly)
				if err != nil {
					_, msg := connectors.SafeError(err)
					issues = append(issues, msg)
				} else {
					results = append(results, o)
				}
			}
		} else {
			o, err := s.policy.Logs(ctx, source, v.Mapping, start, end)
			if err != nil {
				_, msg := connectors.SafeError(err)
				issues = append(issues, msg)
			} else {
				results = append(results, o)
			}
		}
		for _, o := range results {
			doc, _ := json.Marshal(o)
			ev := Evidence{ServiceID: v.ID, ConnectionID: id, Origin: v.Origin, MappingRevision: v.Revision, Observation: o, SourceAvailable: true}
			e = s.pool.QueryRow(ctx, `INSERT INTO evidence(workspace_id,service_id,connection_id,origin,mapping_revision,kind,window_start,window_end,document,expires_at) SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,now()+$11*interval '1 day' WHERE EXISTS(SELECT 1 FROM services WHERE workspace_id=$1 AND id=$2 AND revision=$5) AND EXISTS(SELECT 1 FROM connections WHERE workspace_id=$1 AND id=$3 AND revision=$10 AND enabled AND NOT deleted) RETURNING id::text,collected_at,expires_at`, ws, v.ID, id, v.Origin, v.Revision, o.Kind, start, end, doc, c.Revision, settings.RetentionDays).Scan(&ev.ID, &ev.CollectedAt, &ev.ExpiresAt)
			if e != nil {
				issues = append(issues, "Evidence could not be saved, or the source mapping changed.")
			} else {
				observations = append(observations, ev)
			}
		}
	}
	return observations, issues
}
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	v, e := s.service(r.Context(), workspace(r), r.PathValue("id"))
	if e != nil {
		s.missing(w, r, e)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	end := time.Now().UTC().Truncate(5 * time.Minute)
	evidence, issues := s.collect(ctx, workspace(r), v, end.Add(-15*time.Minute), end)
	s.json(w, 200, map[string]any{"evidence": evidence, "issues": issues})
}
func (s *Server) getEvidence(w http.ResponseWriter, r *http.Request) {
	var v Evidence
	var raw []byte
	e := s.pool.QueryRow(r.Context(), `SELECT id::text,service_id::text,connection_id::text,origin,mapping_revision,collected_at,expires_at,document,EXISTS(SELECT 1 FROM connections WHERE connections.workspace_id=evidence.workspace_id AND connections.id=evidence.connection_id AND connections.enabled AND NOT connections.deleted) FROM evidence WHERE workspace_id=$1 AND id::text=$2`, workspace(r), r.PathValue("id")).Scan(&v.ID, &v.ServiceID, &v.ConnectionID, &v.Origin, &v.MappingRevision, &v.CollectedAt, &v.ExpiresAt, &raw, &v.SourceAvailable)
	if e != nil {
		s.missing(w, r, e)
		return
	}
	if time.Now().After(v.ExpiresAt) {
		s.fail(w, r, 410, "expired", "Evidence has expired under the retention policy.")
		return
	}
	if e = json.Unmarshal(raw, &v.Observation); e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 200, v)
}
func (s *Server) missing(w http.ResponseWriter, r *http.Request, e error) {
	if errors.Is(e, pgx.ErrNoRows) {
		s.fail(w, r, 404, "not_found", "The requested item was not found in this workspace.")
	} else {
		s.internal(w, r, e)
	}
}
func (s *Server) sourceError(w http.ResponseWriter, r *http.Request, e error) {
	if errors.Is(e, pgx.ErrNoRows) {
		s.missing(w, r, e)
		return
	}
	code, msg := connectors.SafeError(e)
	s.fail(w, r, 400, code, msg)
}

func (s *Server) setupDemo(w http.ResponseWriter, r *http.Request) {
	if s.cfg.DemoURL == "" {
		s.fail(w, r, 400, "demo_unavailable", "Start Compose with the demo profile to enable synthetic sources.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if e := s.policy.Test(ctx, connectors.Source{ID: "demo-availability", Kind: "prometheus", URL: s.cfg.DemoURL}); e != nil {
		s.fail(w, r, 400, "demo_unavailable", "Demo source is unavailable. Run make demo, then try again.")
		return
	}
	ws := workspace(r)
	tx, e := s.pool.Begin(r.Context())
	if e != nil {
		s.internal(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	if _, e = tx.Exec(r.Context(), `SELECT id FROM workspaces WHERE id=$1 FOR UPDATE`, ws); e != nil {
		s.internal(w, r, e)
		return
	}
	var existing string
	e = tx.QueryRow(r.Context(), `SELECT id::text FROM services WHERE workspace_id=$1 AND origin='demo' AND EXISTS(SELECT 1 FROM connections c WHERE c.id=services.prometheus_id AND NOT c.deleted) AND EXISTS(SELECT 1 FROM connections c WHERE c.id=services.loki_id AND NOT c.deleted) LIMIT 1`, ws).Scan(&existing)
	if e == nil {
		s.json(w, 200, map[string]string{"service_id": existing})
		return
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		s.internal(w, r, e)
		return
	}
	ids := []string{}
	for _, kind := range []string{"prometheus", "loki"} {
		var id string
		if e = tx.QueryRow(r.Context(), `SELECT gen_random_uuid()::text`).Scan(&id); e != nil {
			s.internal(w, r, e)
			return
		}
		encrypted := s.vault.Seal([]byte(`{"username":"","secret":""}`), ws+":"+id)
		name := "Demo metrics"
		if kind == "loki" {
			name = "Demo logs"
		}
		_, e = tx.Exec(r.Context(), `INSERT INTO connections(id,workspace_id,name,kind,base_url,auth_mode,credential,origin) VALUES($1,$2,$3,$4,$5,'none',$6,'demo')`, id, ws, name, kind, s.cfg.DemoURL, encrypted)
		if e != nil {
			s.internal(w, r, e)
			return
		}
		ids = append(ids, id)
	}
	mapping := connectors.Mapping{Labels: map[string]string{"app": "checkout"}, Metric: "http_requests_total", ErrorLabels: map[string]string{"status": "500"}, ErrorPattern: "(?i)error|exception|timeout", WarningPattern: "(?i)warn|retry", StoreExcerpts: true}
	raw, _ := json.Marshal(mapping)
	var id string
	e = tx.QueryRow(r.Context(), `INSERT INTO services(workspace_id,name,environment,origin,prometheus_id,loki_id,mapping) VALUES($1,'checkout-service','demo','demo',$2,$3,$4) RETURNING id::text`, ws, ids[0], ids[1], raw).Scan(&id)
	if e == nil {
		_, e = tx.Exec(r.Context(), `INSERT INTO audit_events(workspace_id,action) VALUES($1,'demo.created')`, ws)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	if e != nil {
		s.internal(w, r, e)
		return
	}
	s.json(w, 201, map[string]string{"service_id": id})
}
