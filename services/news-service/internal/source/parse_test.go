package source

import (
	"context"
	"errors"
	"golang.org/x/text/encoding/charmap"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func TestEveryMockAdapter(t *testing.T) {
	cfg, e := DefaultConfig()
	if e != nil {
		t.Fatal(e)
	}
	fetch := FixtureFetcher{FS: Fixtures, Resources: cfg.Resources}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	valid, rejected := 0, 0
	for _, s := range cfg.Sources {
		t.Run(s.ID, func(t *testing.T) {
			next := s.URL
			for next != "" {
				r, e := fetch.Fetch(context.Background(), FetchRequest{URL: next, AllowedHosts: s.AllowedHosts})
				if e != nil {
					t.Fatal(e)
				}
				d, e := Parse(s, r.Body, next)
				if e != nil {
					t.Fatal(e)
				}
				for _, candidate := range d.Items {
					if candidate.NeedsFetch {
						r, e := fetch.Fetch(context.Background(), FetchRequest{URL: candidate.URL, AllowedHosts: s.AllowedHosts})
						if e != nil {
							t.Fatal(e)
						}
						p, e := ParseArticle(s, r.Body, r.URL)
						if e != nil {
							t.Fatal(e)
						}
						p.ExternalID = candidate.ExternalID
						if p.RawDate == "" {
							p.RawDate = candidate.RawDate
						}
						candidate = p
					}
					a, e := Normalize(s, candidate, "public", "test", now)
					if e != nil {
						if e.Error() == "missing_publication_date" {
							rejected++
							continue
						}
						t.Fatal(e)
					}
					valid++
					if a.Text == "" || a.PublishedAt == nil || a.PublishedAt.Location() != time.UTC {
						t.Fatalf("invalid article: %+v", a)
					}
					if strings.Contains(a.Text, "ignore_previous") {
						t.Fatal("script included")
					}
				}
				next = d.Next
			}
		})
	}
	if valid != 7 || rejected != 1 {
		t.Fatalf("valid=%d rejected=%d", valid, rejected)
	}
}

func TestWindows1251AndHTMLExclusions(t *testing.T) {
	input := `<?xml version="1.0" encoding="windows-1251"?><rss version="2.0"><channel><item><title>Ставка не выросла</title><description>Ставка остаётся 14%.</description><link>https://a.test/news</link><pubDate>Wed, 21 Oct 2026 12:00:00 +0300</pubDate></item></channel></rss>`
	encoded, e := charmap.Windows1251.NewEncoder().Bytes([]byte(input))
	if e != nil {
		t.Fatal(e)
	}
	d, e := Parse(Source{Kind: "rss", FullText: true}, encoded, "https://a.test/feed")
	if e != nil || d.Items[0].Title != "Ставка не выросла" {
		t.Fatal(d, e)
	}
	page := `<html><head><link rel="canonical" href="/canonical"></head><body><h1>Тест</h1><article><p>Ставка не выросла и составляет 14%.</p><div class="ad">Купите рекламу</div></article></body></html>`
	encoded, e = charmap.Windows1251.NewEncoder().Bytes([]byte(page))
	if e != nil {
		t.Fatal(e)
	}
	s := Source{Charset: "windows-1251", AllowedHosts: []string{"a.test"}, Selectors: map[string]string{"exclude": ".ad", "body": "article"}}
	a, e := ParseArticle(s, encoded, "https://a.test/a")
	if e != nil || a.Title != "Тест" || strings.Contains(a.Text, "рекламу") || a.URL != "https://a.test/canonical" {
		t.Fatal(a, e)
	}
	if _, e = Parse(Source{Kind: "rss"}, []byte(`<rss><channel/></rss><rss/>`), "https://a.test/feed"); e == nil {
		t.Fatal("second XML root accepted")
	}
}
func TestAtomPublicationIsNotUpdated(t *testing.T) {
	s := Source{Kind: "atom", FullText: true}
	b := `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>urn:one</id><title>T</title><updated>2026-10-22T12:00:00Z</updated><link href="https://atom.test/a"/><content>Body</content></entry></feed>`
	d, e := Parse(s, []byte(b), "https://atom.test/feed")
	if e != nil {
		t.Fatal(e)
	}
	if d.Items[0].RawDate != "" || d.Items[0].RawModified == "" {
		t.Fatal("updated used as publication date")
	}
}
func TestMalformedFormats(t *testing.T) {
	for _, tc := range []struct{ kind, body string }{{"rss", "<rss><channel>"}, {"rss", `<!DOCTYPE rss [<!ENTITY x SYSTEM "file:///etc/passwd">]><rss/>`}, {"atom", "<rss/>"}, {"json", `{"items":{}}`}, {"json", `{"items":[]} {}`}} {
		s := Source{Kind: tc.kind, JSONFields: map[string]string{"items": "items"}}
		if _, e := Parse(s, []byte(tc.body), "https://a.test"); e == nil {
			t.Fatalf("accepted invalid %s", tc.kind)
		}
	}
}
func TestNormalizationPreservesMeaning(t *testing.T) {
	cfg, _ := DefaultConfig()
	s := cfg.Sources[0]
	c := Candidate{URL: "https://rss-full.test/a?id=7&utm_source=x#fragment", Title: "Ставка не изменена", Text: "<p>Ставка не выросла и остаётся 14%, а не 16%.</p>", RawDate: "2026-10-21T12:00:00+03:00"}
	a, e := Normalize(s, c, "public", "trace", time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if a.URL != "https://rss-full.test/a?id=7" || !strings.Contains(a.Text, "не выросла") || !strings.Contains(a.Text, "14%") || !strings.Contains(a.Text, "16%") {
		t.Fatalf("meaning lost: %+v", a)
	}
	c.RawDate = ""
	if _, e = Normalize(s, c, "public", "trace", time.Now()); e == nil {
		t.Fatal("missing publication date accepted")
	}
	c.URL = ""
	c.RawDate = "2026-10-21"
	if _, e = Normalize(s, c, "public", "trace", time.Now()); e == nil {
		t.Fatal("missing URL accepted")
	}
}
func TestFixtureFetcherNeverFallsBackToNetwork(t *testing.T) {
	cfg, _ := DefaultConfig()
	f := FixtureFetcher{FS: Fixtures, Resources: cfg.Resources}
	_, e := f.Fetch(context.Background(), FetchRequest{URL: "https://rss-full.test/unknown", AllowedHosts: []string{"rss-full.test"}})
	var fe FetchError
	if !errors.As(e, &fe) || fe.Code != "unknown_mock_url" {
		t.Fatalf("unexpected %v", e)
	}
	r, e := f.Fetch(context.Background(), FetchRequest{URL: "https://rss-full.test/feed", AllowedHosts: []string{"rss-full.test"}, ETag: `"mock-v1"`})
	if e != nil || r.Status != 304 {
		t.Fatalf("conditional response %v %v", r, e)
	}
}
func TestURLAndNetworkPolicy(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "::ffff:127.0.0.1", "192.0.2.1"} {
		if PublicIP(netip.MustParseAddr(ip)) {
			t.Fatalf("private/reserved IP allowed %s", ip)
		}
	}
	if !PublicIP(netip.MustParseAddr("1.1.1.1")) {
		t.Fatal("public IP blocked")
	}
	for _, u := range []string{"file:///etc/passwd", "http://user:pass@a.test/x", "http://a.test.evil/x"} {
		if AllowedURL(u, []string{"a.test"}) == nil {
			t.Fatalf("unsafe URL %s", u)
		}
	}
}
func TestHTTPStatusAndBodyLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rate":
			w.Header().Set("Retry-After", "120")
			w.WriteHeader(429)
		case "/large":
			w.Write([]byte(strings.Repeat("x", MaxBody+1)))
		case "/redirect":
			http.Redirect(w, r, "http://not-allowed.test/", 302)
		default:
			w.Header().Set("ETag", `"v1"`)
			w.Write([]byte("ok"))
		}
	}))
	defer server.Close()
	f := &HTTPFetcher{client: server.Client()}
	host := strings.TrimPrefix(server.URL, "http://")
	host = strings.Split(host, ":")[0]
	for _, tc := range []struct{ path, code string }{{"/rate", "http_429"}, {"/large", "response_too_large"}, {"/redirect", "transport_error"}} {
		_, e := f.Fetch(context.Background(), FetchRequest{URL: server.URL + tc.path, AllowedHosts: []string{host}})
		if e == nil || !strings.Contains(e.Error(), tc.code) {
			t.Fatalf("%s: %v", tc.path, e)
		}
	}
}
func TestDateAndConfiguration(t *testing.T) {
	s := Source{Timezone: "Europe/Moscow"}
	p, e := ParseDate("21.10.2026 12:30", s)
	if e != nil || p.Hour() != 9 {
		t.Fatalf("timezone: %v %v", p, e)
	}
	if _, e = ParseDate("nonsense", s); e == nil {
		t.Fatal("invalid date accepted")
	}
	cfg, _ := DefaultConfig()
	cfg.Sources[0].Timezone = "Not/AZone"
	if cfg.Validate() == nil {
		t.Fatal("invalid timezone accepted")
	}
	b, _ := fs.ReadFile(Fixtures, "fixtures/sources.json")
	cfg, _ = Load(b)
	cfg.Sources[3].Selectors["items"] = "["
	if cfg.Validate() == nil {
		t.Fatal("invalid selector accepted")
	}
}
