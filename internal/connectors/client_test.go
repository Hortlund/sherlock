package connectors

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"reflect"
	"sherlock/internal/demo"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNetworkPolicy(t *testing.T) {
	p, _ := NewPolicy("", "")
	for _, ip := range []string{"127.0.0.1", "::1", "169.254.169.254", "10.2.3.4", "::ffff:127.0.0.1", "0.0.0.0"} {
		if p.allowed("untrusted.example", netip.MustParseAddr(ip)) {
			t.Fatalf("allowed %s", ip)
		}
	}
	p, _ = NewPolicy("127.0.0.0/8", "demo")
	if !p.allowed("localhost", netip.MustParseAddr("127.0.0.1")) || !p.allowed("demo", netip.MustParseAddr("172.18.0.2")) {
		t.Fatal("explicit policy rejected")
	}
	upstream := httptest.NewServer(demo.Handler())
	defer upstream.Close()
	blocked, _ := NewPolicy("", "")
	if blocked.Test(context.Background(), Source{ID: "test", Kind: "prometheus", URL: upstream.URL}) == nil {
		t.Fatal("loopback SSRF allowed")
	}
}
func TestVendorContracts(t *testing.T) {
	upstream := httptest.NewServer(demo.Handler())
	defer upstream.Close()
	p, _ := NewPolicy("127.0.0.0/8", "")
	s := Source{ID: "test", Kind: "prometheus", URL: upstream.URL}
	if e := p.Test(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	s.Kind = "loki"
	if e := p.Test(context.Background(), s); e != nil {
		t.Fatal(e)
	}
	end := time.Now().UTC().Truncate(5 * time.Minute)
	m := Mapping{Labels: map[string]string{"app": "checkout"}, Metric: "http_requests_total", ErrorLabels: map[string]string{"status": "500"}, ErrorPattern: "(?i)error", WarningPattern: "(?i)warn", StoreExcerpts: true}
	logs, e := p.Logs(context.Background(), s, m, end.Add(-15*time.Minute), end)
	if e != nil || !logs.Complete || logs.SampleCount != 20 || len(logs.Errors) != 1 || logs.Errors[0].Count != 12 {
		t.Fatalf("bad logs: %+v %v", logs, e)
	}
	base, e := p.Logs(context.Background(), s, m, end.Add(-75*time.Minute), end.Add(-15*time.Minute))
	if e != nil || len(base.Errors) != 0 {
		t.Fatalf("bad baseline: %+v %v", base, e)
	}
	count, e := p.Count(context.Background(), s, m, end.Add(-15*time.Minute), end, true)
	if e != nil || count.Value != 120 {
		t.Fatalf("bad count: %+v %v", count, e)
	}
}
func TestUpstreamErrorsAndRedirects(t *testing.T) {
	p, _ := NewPolicy("127.0.0.0/8", "")
	for _, status := range []int{401, 403, 404, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte("private credential in upstream body"))
			}))
			defer up.Close()
			err := p.Test(context.Background(), Source{ID: "test", Kind: "prometheus", URL: up.URL})
			if err == nil || strings.Contains(err.Error(), "private credential") {
				t.Fatal("unsafe failure")
			}
		})
	}
	redirected := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected = true }))
	defer target.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer up.Close()
	if e := p.Test(context.Background(), Source{ID: "test", Kind: "prometheus", URL: up.URL}); e == nil || redirected {
		t.Fatal("followed redirect")
	}
}
func TestSelectorAndRedaction(t *testing.T) {
	got, e := Selector(map[string]string{"app": "checkout\"} or vector(1)"})
	if e != nil || !strings.Contains(got, `\"`) {
		t.Fatal("label escaping failed")
	}
	for _, raw := range []string{"file:///etc/passwd", "http://user:password@example.com", "https://example.com?token=secret"} {
		if ValidateURL(raw) == nil {
			t.Fatal("unsafe URL accepted")
		}
	}
	for _, raw := range []string{`{"token":"secret-value"}`, `Authorization: Bearer secret-value`, `password=secret-value`} {
		if strings.Contains(Redact(raw), "secret-value") {
			t.Fatalf("secret leaked in %s", raw)
		}
	}
	if Fingerprint("request 123 failed") != Fingerprint("request 456 failed") {
		t.Fatal("unstable fingerprint")
	}
}

func TestTLSLimitsAndCancellation(t *testing.T) {
	p, _ := NewPolicy("127.0.0.0/8", "")
	tlsServer := httptest.NewTLSServer(demo.Handler())
	defer tlsServer.Close()
	s := Source{ID: "tls", Kind: "prometheus", URL: tlsServer.URL}
	if e := p.Test(context.Background(), s); e == nil {
		t.Fatal("untrusted TLS accepted")
	}
	s.CAPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsServer.Certificate().Raw}))
	if e := p.Test(context.Background(), s); e != nil {
		t.Fatal("private CA failed", e)
	}
	huge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(strings.Repeat("x", 2*1024*1024+1))) }))
	defer huge.Close()
	if code, _ := SafeError(p.Test(context.Background(), Source{ID: "huge", Kind: "prometheus", URL: huge.URL})); code != "response_limit" {
		t.Fatal("body limit not enforced", code)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if code, _ := SafeError(p.Test(ctx, Source{ID: "slow", Kind: "prometheus", URL: slow.URL})); code != "timeout" {
		t.Fatal("timeout not enforced", code)
	}
}
func TestTruncationAndPrivatePatterns(t *testing.T) {
	end := time.Now().Truncate(time.Minute)
	start := end.Add(-15 * time.Minute)
	values := [][]string{}
	for i := 0; i < 2000; i++ {
		values = append(values, []string{strconv.FormatInt(start.Add(time.Duration(i)*end.Sub(start)/2001).UnixNano(), 10), `error account=private-account-123 <script>alert(1)</script>`})
	}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{"values": values}}}})
	}))
	defer up.Close()
	p, _ := NewPolicy("127.0.0.0/8", "")
	m := Mapping{Labels: map[string]string{"app": "test"}, ErrorPattern: "error", WarningPattern: "warn", StoreExcerpts: true, SensitivePatterns: []string{`account=\S+`}}
	o, e := p.Logs(context.Background(), Source{ID: "capped", URL: up.URL}, m, start, end)
	if e != nil || o.Complete || o.SampleCount != 2000 {
		t.Fatal("truncation not detected", e)
	}
	if strings.Contains(o.Errors[0].Sample, "private-account") {
		t.Fatal("custom pattern leaked")
	}
	m.SensitivePatterns = []string{"a*"}
	m.StoreExcerpts = false
	o, e = p.Logs(context.Background(), Source{ID: "capped", URL: up.URL}, m, start, end)
	if e != nil || o.Errors[0].Sample != "" || strings.Contains(o.Errors[0].Fingerprint, "script") {
		t.Fatal("excerpt disable retained raw text")
	}
}

func TestRedactionBeforeExcerptBoundary(t *testing.T) {
	end := time.Now().Truncate(time.Minute)
	line := "error " + strings.Repeat("x", 460) + " customer[PRIVATE-ID-123" + strings.Repeat("z", 80) + "]"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "streams", "result": []any{map[string]any{"values": [][]string{{strconv.FormatInt(end.Add(-time.Minute).UnixNano(), 10), line}}}}}})
	}))
	defer up.Close()
	p, _ := NewPolicy("127.0.0.0/8", "")
	m := Mapping{Labels: map[string]string{"app": "test"}, ErrorPattern: "error", WarningPattern: "warn", StoreExcerpts: true, SensitivePatterns: []string{`customer\[[^]]+\]`}}
	o, e := p.Logs(context.Background(), Source{ID: "boundary", URL: up.URL}, m, end.Add(-15*time.Minute), end)
	if e != nil || len(o.Errors) != 1 {
		t.Fatalf("collection: %+v %v", o, e)
	}
	if strings.Contains(o.Errors[0].Sample, "PRIVATE-ID") || !strings.Contains(o.Errors[0].Sample, "[redacted]") || len(o.Errors[0].Sample) > 512 {
		t.Fatalf("unsafe excerpt: %q", o.Errors[0].Sample)
	}
	// All patterns see original context, including overlapping matches and patterns
	// whose replacement would otherwise prevent a subsequent match.
	m.SensitivePatterns = []string{`customer`, `customer\[[^]]+\]`}
	o, e = p.Logs(context.Background(), Source{ID: "overlap", URL: up.URL}, m, end.Add(-15*time.Minute), end)
	if e != nil || strings.Contains(o.Errors[0].Sample, "PRIVATE-ID") {
		t.Fatal("overlapping redaction leaked", e)
	}
}

func TestSharedSourceWaitsAndCancels(t *testing.T) {
	p, _ := NewPolicy("127.0.0.0/8", "")
	entered := make(chan struct{}, 3)
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		json.NewEncoder(w).Encode(map[string]string{"status": "success"})
	}))
	defer up.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	source := Source{ID: "shared", Kind: "prometheus", URL: up.URL}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 3)
	for range 2 {
		go func() { done <- p.Test(ctx, source) }()
	}
	for range 2 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	waiting, stop := context.WithTimeout(ctx, 30*time.Millisecond)
	if e := p.Test(waiting, source); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("waiting request ignored cancellation: %v", e)
	}
	stop()
	go func() { done <- p.Test(ctx, source) }()
	select {
	case e := <-done:
		t.Fatalf("third request failed instead of waiting: %v", e)
	case <-entered:
		t.Fatal("source limit exceeded")
	case <-time.After(30 * time.Millisecond):
	}
	// Waiting for this source must not block unrelated sources.
	other := httptest.NewServer(demo.Handler())
	defer other.Close()
	if e := p.Test(ctx, Source{ID: "other", Kind: "prometheus", URL: other.URL}); e != nil {
		t.Fatal(e)
	}
	close(release)
	for range 3 {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sources) != 0 {
		t.Fatal("source waiters leaked")
	}
}

func TestIndependentSourceSelectorsAndLegacyMappings(t *testing.T) {
	p, _ := NewPolicy("127.0.0.0/8", "")
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if r.URL.Path == "/api/v1/query" {
			if !strings.Contains(query, `job="checkout"`) || strings.Contains(query, `app=`) {
				t.Errorf("metrics selector: %s", query)
			}
		} else if !strings.Contains(query, `app="checkout"`) || strings.Contains(query, `job=`) {
			t.Errorf("logs selector: %s", query)
		}
		demo.Handler().ServeHTTP(w, r)
	}))
	defer up.Close()
	m := Mapping{PrometheusLabels: map[string]string{"job": "checkout"}, LokiLabels: map[string]string{"app": "checkout"}, Metric: "http_requests_total", ErrorLabels: map[string]string{"status": "500"}, ErrorPattern: "error", WarningPattern: "warn"}
	if e := m.Validate(true, true); e != nil {
		t.Fatal(e)
	}
	end := time.Now().UTC().Truncate(5 * time.Minute)
	s := Source{ID: "separate", URL: up.URL}
	if _, e := p.Count(context.Background(), s, m, end.Add(-15*time.Minute), end, true); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Logs(context.Background(), s, m, end.Add(-15*time.Minute), end); e != nil {
		t.Fatal(e)
	}
	legacy := m
	legacy.PrometheusLabels = nil
	legacy.LokiLabels = nil
	legacy.Labels = map[string]string{"app": "checkout"}
	normalized := legacy.Canonical(true, true)
	if !reflect.DeepEqual(normalized.MetricsLabels(), legacy.Labels) || !reflect.DeepEqual(normalized.LogsLabels(), legacy.Labels) {
		t.Fatal("legacy mapping lost selectors")
	}
	m.PrometheusLabels = map[string]string{}
	if m.Validate(true, true) == nil {
		t.Fatal("explicit empty selector accepted")
	}
}
