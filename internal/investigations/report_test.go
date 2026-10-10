package investigations

import (
	"sherlock/internal/connectors"
	"strings"
	"testing"
	"time"
)

func TestReportClaimsAndProvenance(t *testing.T) {
	end := time.Now().Truncate(time.Minute)
	scope := Scope{"w", "s", "demo", 1, end.Add(-30 * time.Minute), end, end.Add(-90 * time.Minute)}
	item := Evidence{"logs", "w", "s", "demo", 1, connectors.Observation{Kind: "log_patterns", Start: scope.Start, End: scope.End, Complete: true, SampleCount: 20, CoverageFraction: .9, Errors: []connectors.Pattern{{Fingerprint: "opaque", Count: 12}}}, end.Add(time.Hour)}
	report, e := Build(scope, []Evidence{item})
	if e != nil || !report.Adequate || len(report.Observations) != 1 {
		t.Fatalf("partial report failed: %+v %v", report, e)
	}
	if strings.Contains(report.Observations[0].Text, "request error proportion") || !strings.Contains(report.Summary, "undetermined") {
		t.Fatal("unsupported claim")
	}
	for _, mutate := range []func(*Evidence){func(v *Evidence) { v.WorkspaceID = "other" }, func(v *Evidence) { v.ServiceID = "other" }, func(v *Evidence) { v.Origin = "live" }, func(v *Evidence) { v.MappingRevision = 2 }, func(v *Evidence) { v.Observation.Start = v.Observation.Start.Add(-time.Minute) }, func(v *Evidence) { v.ExpiresAt = end.Add(-time.Hour) }} {
		bad := item
		mutate(&bad)
		if _, e = Build(scope, []Evidence{bad}); e == nil {
			t.Fatal("invalid citation accepted")
		}
	}
	empty, e := Build(scope, nil)
	if e != nil || empty.Adequate {
		t.Fatal("empty report claims adequate evidence")
	}
}
