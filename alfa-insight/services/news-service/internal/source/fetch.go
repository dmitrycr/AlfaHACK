package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const MaxBody = 2 << 20

type FetchRequest struct {
	URL          string
	AllowedHosts []string
	ETag         string
	Modified     string
}
type FetchResponse struct {
	Status   int
	Body     []byte
	ETag     string
	Modified string
	URL      string
}
type Fetcher interface {
	Fetch(context.Context, FetchRequest) (FetchResponse, error)
}
type FetchError struct {
	Code      string
	Retryable bool
	After     time.Duration
}

func (e FetchError) Error() string { return e.Code }

type FixtureFetcher struct {
	FS        fs.FS
	Resources map[string]Resource
}

func (f FixtureFetcher) Fetch(ctx context.Context, r FetchRequest) (FetchResponse, error) {
	if e := ctx.Err(); e != nil {
		return FetchResponse{}, e
	}
	if e := AllowedURL(r.URL, r.AllowedHosts); e != nil {
		return FetchResponse{}, e
	}
	resource, ok := f.Resources[r.URL]
	if !ok {
		return FetchResponse{}, FetchError{Code: "unknown_mock_url"}
	}
	if resource.Status == 0 {
		resource.Status = 200
	}
	out := FetchResponse{Status: resource.Status, URL: r.URL, ETag: resource.Headers["ETag"], Modified: resource.Headers["Last-Modified"]}
	if out.Status == 200 && ((r.ETag != "" && r.ETag == out.ETag) || (r.ETag == "" && r.Modified != "" && r.Modified == out.Modified)) {
		out.Status = 304
		return out, nil
	}
	if e := statusError(out.Status, resource.Headers["Retry-After"], time.Now()); e != nil {
		return out, e
	}
	if out.Status == 304 {
		return out, nil
	}
	if !fs.ValidPath(resource.File) {
		return out, errors.New("invalid fixture path")
	}
	b, e := fs.ReadFile(f.FS, resource.File)
	if e != nil {
		return out, e
	}
	if len(b) > MaxBody {
		return out, FetchError{Code: "response_too_large"}
	}
	out.Body = b
	return out, nil
}

type HTTPFetcher struct{ client *http.Client }

func NewHTTPFetcher() *HTTPFetcher {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		if port != "80" && port != "443" {
			return nil, errors.New("port_not_allowed")
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil {
			return nil, e
		}
		if len(ips) == 0 {
			return nil, errors.New("no_address")
		}
		for _, ip := range ips {
			if !PublicIP(ip) {
				return nil, errors.New("private_address_blocked")
			}
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return &HTTPFetcher{client: &http.Client{Transport: t, Timeout: 10 * time.Second}}
}
func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, block := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96"} {
		if netip.MustParsePrefix(block).Contains(ip) {
			return false
		}
	}
	return true
}
func (f *HTTPFetcher) Fetch(ctx context.Context, r FetchRequest) (FetchResponse, error) {
	var out FetchResponse
	if e := AllowedURL(r.URL, r.AllowedHosts); e != nil {
		return out, e
	}
	client := *f.client
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("too_many_redirects")
		}
		return AllowedURL(req.URL.String(), r.AllowedHosts)
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if e != nil {
		return out, e
	}
	req.Header.Set("User-Agent", "AlfaInsight-MockDevelopment/0.1")
	if r.ETag != "" {
		req.Header.Set("If-None-Match", r.ETag)
	}
	if r.Modified != "" {
		req.Header.Set("If-Modified-Since", r.Modified)
	}
	resp, e := client.Do(req)
	if e != nil {
		return out, FetchError{Code: "transport_error", Retryable: true}
	}
	defer resp.Body.Close()
	out = FetchResponse{Status: resp.StatusCode, ETag: resp.Header.Get("ETag"), Modified: resp.Header.Get("Last-Modified"), URL: resp.Request.URL.String()}
	if e = statusError(resp.StatusCode, resp.Header.Get("Retry-After"), time.Now()); e != nil {
		return out, e
	}
	if resp.StatusCode == 304 {
		return out, nil
	}
	out.Body, e = io.ReadAll(io.LimitReader(resp.Body, MaxBody+1))
	if e != nil {
		return out, FetchError{Code: "body_read_failed", Retryable: true}
	}
	if len(out.Body) > MaxBody {
		return out, FetchError{Code: "response_too_large"}
	}
	return out, nil
}
func statusError(code int, retry string, now time.Time) error {
	if code == 200 || code == 304 {
		return nil
	}
	e := FetchError{Code: fmt.Sprintf("http_%d", code), Retryable: code == 429 || code >= 500}
	if seconds, err := strconv.Atoi(retry); err == nil && seconds >= 0 {
		e.After = time.Duration(seconds) * time.Second
	} else if d, err := http.ParseTime(retry); err == nil && d.After(now) {
		e.After = d.Sub(now)
	}
	if e.After > 24*time.Hour {
		e.After = 24 * time.Hour
	}
	return e
}
func CanonicalURL(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errors.New("invalid_url")
	}
	u.Fragment = ""
	u.Host = strings.ToLower(u.Host)
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") || k == "gclid" || k == "fbclid" {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}
