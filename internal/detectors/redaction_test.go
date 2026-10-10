package detectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sherlock/internal/connectors"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWithheldLogsCannotCreateFindings(t *testing.T) {
	end := time.Now().Truncate(time.Minute)
	start := end.Add(-15 * time.Minute)
	values := [][]string{}
	for i := 0; i < 20; i++ {
		line := "warn ordinary warning"
		if i < 3 {
			line = "error " + []string{"alpha", "bravo", "charlie"}[i] + " " + strings.Repeat("x", 5000)
		}
		values = append(values, []string{strconv.FormatInt(start.Add(time.Duration(i)*end.Sub(start)/20).UnixNano(), 10), line})
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{"values": values}}}})
	}))
	defer up.Close()
	p, _ := connectors.NewPolicy("127.0.0.0/8", "")
	for _, excerpts := range []bool{true, false} {
		observation, err := p.Logs(context.Background(), connectors.Source{ID: "limit", URL: up.URL}, connectors.Mapping{LokiLabels: map[string]string{"app": "test"}, ErrorPattern: "error", WarningPattern: "warn", SensitivePatterns: []string{"x"}, StoreExcerpts: excerpts}, start, end)
		if err != nil {
			t.Fatal(err)
		}
		if observation.Complete || observation.SampleCount != 20 || len(observation.Errors) != 0 || len(observation.Warnings) != 1 || !strings.Contains(observation.Notes, "redaction match limit") {
			t.Fatalf("withheld lines became evidence: %+v", observation)
		}
		baseline := fixture("log_patterns", 0, start.Add(-time.Hour), start)
		current := Input{ID: "current", Observation: observation}
		// Either window being incomplete must make both log detectors abstain,
		// including warning_burst even though its own 17 lines were sanitized.
		for _, pair := range [][2]Input{{current, baseline}, {baseline, current}} {
			result := Evaluate([]Input{pair[0]}, []Input{pair[1]}, Defaults())
			if len(result.Candidates) != 0 {
				t.Fatalf("fabricated findings: %+v", result)
			}
			for _, coverage := range result.Coverage {
				if coverage.Status != "insufficient_signal" {
					t.Fatalf("false coverage: %+v", coverage)
				}
			}
		}
	}
}
