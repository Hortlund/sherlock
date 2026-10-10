package connectors

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRealSources(t *testing.T) {
	prom, loki := os.Getenv("SHERLOCK_REAL_PROMETHEUS"), os.Getenv("SHERLOCK_REAL_LOKI")
	if prom == "" || loki == "" {
		t.Skip("run make real-sources")
	}
	n, e := strconv.ParseInt(os.Getenv("SHERLOCK_REAL_END"), 10, 64)
	if e != nil {
		t.Fatal(e)
	}
	end := time.Unix(n, 0)
	p, _ := NewPolicy("127.0.0.0/8", "")
	ctx := context.Background()
	m := Mapping{Labels: map[string]string{"app": "checkout"}, Metric: "http_requests_total", ErrorLabels: map[string]string{"status": "500"}, ErrorPattern: "level=error", WarningPattern: "level=warn", StoreExcerpts: true}
	ps := Source{ID: "real-prometheus", Kind: "prometheus", URL: prom}
	ls := Source{ID: "real-loki", Kind: "loki", URL: loki}
	for _, source := range []Source{ps, ls} {
		if e = p.Test(ctx, source); e != nil {
			t.Fatal(e)
		}
	}
	total, e := p.Count(ctx, ps, m, end.Add(-15*time.Minute), end, false)
	if e != nil || !total.Complete || total.Value < 1400 {
		t.Fatalf("counter query: %+v %v", total, e)
	}
	errors, e := p.Count(ctx, ps, m, end.Add(-15*time.Minute), end, true)
	if e != nil || errors.Value/total.Value < .11 {
		t.Fatalf("error proportion: %+v %v", errors, e)
	}
	base, e := p.Count(ctx, ps, m, end.Add(-75*time.Minute), end.Add(-15*time.Minute), true)
	if e != nil || base.Value > 10 {
		t.Fatalf("comparison semantics: %+v %v", base, e)
	}
	logs, e := p.Logs(ctx, ls, m, end.Add(-15*time.Minute), end)
	if e != nil || !logs.Complete || logs.SampleCount != 15 || len(logs.Errors) != 1 || logs.Errors[0].Count != 15 {
		t.Fatalf("Loki semantics: %+v %v", logs, e)
	}
	if strings.Contains(logs.Errors[0].Sample, "secret-test-value") {
		t.Fatal("secret persisted")
	}
	m.Labels = map[string]string{"app": "absent"}
	if _, e = p.Count(ctx, ps, m, end.Add(-15*time.Minute), end, false); e == nil {
		t.Fatal("missing metric treated as healthy")
	}
	empty, e := p.Logs(ctx, ls, m, end.Add(-15*time.Minute), end)
	if e != nil || empty.SampleCount != 0 || empty.Notes == "" {
		t.Fatal("empty log result not qualified")
	}
	// No data after the last imported sample: stale counters must not pass the guard.
	m.Labels = map[string]string{"app": "checkout"}
	if _, e = p.Count(ctx, ps, m, end.Add(-10*time.Minute), end.Add(5*time.Minute), false); e == nil {
		t.Fatal("stale series accepted")
	}
}
