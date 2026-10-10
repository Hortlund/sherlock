// Package demo serves synthetic telemetry in vendor API formats. The application
// still uses its normal HTTP connectors, persistence, and analysis pipeline.
package demo

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func Handler() http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, data any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": data})
	}
	mux.HandleFunc("GET /api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		end, _ := strconv.ParseFloat(r.URL.Query().Get("time"), 64)
		at := time.Unix(int64(end), 0)
		value := 1.0
		if strings.HasPrefix(q, "sum(increase(") {
			value = 1000
			if strings.Contains(q, "3600s") {
				value = 4000
			}
			if strings.Contains(q, `status="500"`) {
				value = 4
				if at.After(time.Now().UTC().Truncate(5 * time.Minute).Add(-time.Minute)) {
					value = 120
				}
			}
		}
		write(w, map[string]any{"resultType": "vector", "result": []any{map[string]any{"metric": map[string]string{}, "value": []any{at.Unix(), strconv.FormatFloat(value, 'f', 0, 64)}}}})
	})
	mux.HandleFunc("GET /api/v1/labels", func(w http.ResponseWriter, r *http.Request) { write(w, []string{"app", "status"}) })
	mux.HandleFunc("GET /loki/api/v1/labels", func(w http.ResponseWriter, r *http.Request) { write(w, []string{"app", "environment"}) })
	mux.HandleFunc("GET /loki/api/v1/query_range", func(w http.ResponseWriter, r *http.Request) {
		ns, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
		ne, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
		start, end := time.Unix(0, ns), time.Unix(0, ne)
		current := end.After(time.Now().UTC().Truncate(5 * time.Minute).Add(-time.Minute))
		values := [][]string{}
		for i := 0; i < 20; i++ {
			at := start.Add(time.Duration(i+1) * end.Sub(start) / 21)
			line := `{"level":"info","message":"Request completed"}`
			if current && i < 12 {
				line = `{"level":"error","message":"Database connection acquisition timeout"}`
			}
			values = append(values, []string{strconv.FormatInt(at.UnixNano(), 10), line})
		}
		write(w, map[string]any{"resultType": "streams", "result": []any{map[string]any{"stream": map[string]string{"app": "checkout"}, "values": values}}})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	return mux
}
