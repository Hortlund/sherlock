package connectors

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Policy struct {
	CIDRs   []netip.Prefix
	Hosts   map[string]bool
	slots   chan struct{}
	mu      sync.Mutex
	sources map[string]*sourceSlots
}

func NewPolicy(cidrs, hosts string) (*Policy, error) {
	p := &Policy{Hosts: map[string]bool{}, slots: make(chan struct{}, 16), sources: map[string]*sourceSlots{}}
	for _, s := range strings.Split(cidrs, ",") {
		if s = strings.TrimSpace(s); s != "" {
			v, e := netip.ParsePrefix(s)
			if e != nil {
				return nil, errors.New("invalid SHERLOCK_ALLOWED_CIDRS")
			}
			p.CIDRs = append(p.CIDRs, v)
		}
	}
	for _, s := range strings.Split(hosts, ",") {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			p.Hosts[s] = true
		}
	}
	return p, nil
}
func ValidateURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || len(raw) > 2048 {
		return errors.New("Use an HTTP(S) base URL without credentials, query, or fragment.")
	}
	return nil
}
func (p *Policy) allowed(host string, ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip == netip.MustParseAddr("100.100.100.200") || ip == netip.MustParseAddr("fd00:ec2::254") {
		return false
	}
	// Never allow cloud metadata, multicast, unspecified, or link-local addresses.
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	for _, prefix := range p.CIDRs {
		if prefix.Contains(ip) {
			return true
		}
	}
	if ip.IsLoopback() {
		return false
	}
	if ip.IsPrivate() {
		return p.Hosts[strings.ToLower(host)]
	}
	return true
}
func (p *Policy) dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, errors.New("Invalid destination.")
	}
	ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if e != nil || len(ips) == 0 {
		return nil, errors.New("Source hostname could not be resolved.")
	}
	for _, ip := range ips {
		if !p.allowed(host, ip) {
			return nil, errors.New("Source destination is blocked by the server network policy.")
		}
	}
	var last error
	for _, ip := range ips {
		c, e := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return c, nil
		}
		last = e
	}
	return nil, last
}

type Credential struct {
	Username string `json:"username"`
	Secret   string `json:"secret"`
}
type Source struct {
	ID, Kind, URL, AuthMode, Tenant, CAPEM string
	Credential                             Credential
	Before                                 func(context.Context) error
}
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Message }
func SafeError(err error) (string, string) {
	var e *Error
	if errors.As(err, &e) {
		return e.Code, e.Message
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "Source request timed out."
	}
	return "unavailable", "Source could not be reached. Check URL, network policy, TLS, and credentials."
}

type sourceSlots struct {
	slots chan struct{}
	users int
}

func (p *Policy) acquireSource(ctx context.Context, id string) (func(), error) {
	p.mu.Lock()
	entry := p.sources[id]
	if entry == nil {
		entry = &sourceSlots{slots: make(chan struct{}, 2)}
		p.sources[id] = entry
	}
	entry.users++
	p.mu.Unlock()
	cleanup := func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		entry.users--
		if entry.users == 0 {
			delete(p.sources, id)
		}
	}
	select {
	case entry.slots <- struct{}{}:
		return func() { <-entry.slots; cleanup() }, nil
	case <-ctx.Done():
		cleanup()
		return nil, ctx.Err()
	}
}

func (p *Policy) Get(ctx context.Context, s Source, path string, values url.Values, out any) error {
	if e := ValidateURL(s.URL); e != nil {
		return &Error{"invalid_url", e.Error()}
	}
	// Wait for this source before taking a global slot, so one busy source
	// cannot occupy the whole global budget. Waiting honors the caller deadline.
	release, err := p.acquireSource(ctx, s.ID)
	if err != nil {
		return err
	}
	defer release()
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	trust, e := x509.SystemCertPool()
	if e != nil {
		trust = x509.NewCertPool()
	}
	if s.CAPEM != "" && !trust.AppendCertsFromPEM([]byte(s.CAPEM)) {
		return &Error{"tls", "The configured CA certificate is invalid."}
	}
	transport := &http.Transport{Proxy: nil, DialContext: p.dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: trust}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second, DisableKeepAlives: true, MaxResponseHeaderBytes: 32 * 1024}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirects are not permitted") }}
	u, _ := url.Parse(s.URL)
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = values.Encode()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return &Error{"invalid_url", "Invalid source URL."}
	}
	switch s.AuthMode {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+s.Credential.Secret)
	case "basic":
		req.SetBasicAuth(s.Credential.Username, s.Credential.Secret)
	}
	if s.Tenant != "" {
		req.Header.Set("X-Scope-OrgID", s.Tenant)
	}
	if s.Before != nil {
		if err := s.Before(ctx); err != nil {
			return err
		}
	}
	if before, ok := ctx.Value(requestGuardKey{}).(func(context.Context) error); ok {
		if err := before(ctx); err != nil {
			return err
		}
	}
	res, e := client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ne net.Error
		if errors.As(e, &ne) && ne.Timeout() {
			return &Error{"timeout", "Source request timed out."}
		}
		var cert *tls.CertificateVerificationError
		if errors.As(e, &cert) {
			return &Error{"tls", "TLS verification failed. Check the hostname and trusted CA."}
		}
		if strings.Contains(e.Error(), "blocked by") {
			return &Error{"network_policy", "Source destination is blocked. Configure the deployment network allowlist."}
		}
		return &Error{"unavailable", "Source could not be reached. Check its URL and network access."}
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return &Error{"auth_denied", "Source denied access. Check read-only credentials and tenant."}
	}
	if res.StatusCode == 404 {
		return &Error{"wrong_path", "API path was not found. Check the source type and base URL."}
	}
	if res.StatusCode == 429 {
		return &Error{"rate_limited", "Source rate limit reached. Reduce polling or try later."}
	}
	if res.StatusCode != 200 {
		return &Error{"upstream_error", fmt.Sprintf("Source returned HTTP %d.", res.StatusCode)}
	}
	data, e := io.ReadAll(io.LimitReader(res.Body, 2*1024*1024+1))
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var timeout net.Error
		if errors.As(e, &timeout) && timeout.Timeout() {
			return &Error{"timeout", "Source request timed out."}
		}
		return &Error{"invalid_response", "Could not read the source response."}
	}
	if len(data) > 2*1024*1024 {
		return &Error{"response_limit", "Source response exceeds the 2 MiB limit. Narrow the service selector."}
	}
	if json.Unmarshal(data, out) != nil {
		return &Error{"invalid_response", "Source returned an invalid API response."}
	}
	return nil
}

type Envelope struct {
	Status   string          `json:"status"`
	Data     json.RawMessage `json:"data"`
	Warnings []string        `json:"warnings"`
}

func (p *Policy) Test(ctx context.Context, s Source) error {
	path := "/api/v1/query"
	values := url.Values{"query": {"vector(1)"}, "timeout": {"10s"}}
	if s.Kind == "loki" {
		path = "/loki/api/v1/labels"
		values = url.Values{"start": {strconv.FormatInt(time.Now().Add(-15*time.Minute).UnixNano(), 10)}, "end": {strconv.FormatInt(time.Now().UnixNano(), 10)}}
	}
	var e Envelope
	if err := p.Get(ctx, s, path, values, &e); err != nil {
		return err
	}
	if e.Status != "success" {
		return &Error{"invalid_response", "Source did not return a successful API result."}
	}
	return nil
}
func Selector(labels map[string]string) (string, error) {
	if len(labels) == 0 || len(labels) > 12 {
		return "", errors.New("Choose 1–12 exact-match labels.")
	}
	keys := make([]string, 0, len(labels))
	for k, v := range labels {
		if !LabelName.MatchString(k) || len(v) == 0 || len(v) > 200 {
			return "", errors.New("Invalid label name or value.")
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	terms := []string{}
	for _, k := range keys {
		terms = append(terms, k+"="+strconv.Quote(labels[k]))
	}
	return "{" + strings.Join(terms, ",") + "}", nil
}

type requestGuardKey struct{}

func WithRequestGuard(ctx context.Context, before func(context.Context) error) context.Context {
	return context.WithValue(ctx, requestGuardKey{}, before)
}

// HTTPClient reuses destination-safe DNS/dialing with no redirects or environment proxy.
// Callers must validate their own URL schema and keep telemetry/webhook policies separate.
func (p *Policy) HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, DialContext: p.dial, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: timeout, DisableKeepAlives: true, MaxResponseHeaderBytes: 32 * 1024}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
