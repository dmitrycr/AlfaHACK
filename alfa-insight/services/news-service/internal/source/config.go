package source

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/andybalholm/cascadia"
)

//go:embed fixtures/*
var Fixtures embed.FS

type Source struct {
	MockResources map[string]Resource `json:"mock_resources,omitempty"`
	ID            string              `json:"id"`
	Kind          string              `json:"kind"`
	URL           string              `json:"url"`
	Enabled       bool                `json:"enabled"`
	Priority      int                 `json:"priority"`
	PollSeconds   int                 `json:"poll_seconds"`
	Timezone      string              `json:"timezone"`
	Charset       string              `json:"charset,omitempty"`
	DateLayouts   []string            `json:"date_layouts"`
	FullText      bool                `json:"full_text"`
	AllowedHosts  []string            `json:"allowed_hosts"`
	Selectors     map[string]string   `json:"selectors"`
	JSONFields    map[string]string   `json:"json_fields"`
	MaxPages      int                 `json:"max_pages"`
	MaxItems      int                 `json:"max_items"`
	Version       string              `json:"version"`
}
type Resource struct {
	File    string            `json:"file"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}
type Config struct {
	Version   string              `json:"version"`
	Sources   []Source            `json:"sources"`
	Resources map[string]Resource `json:"resources"`
}

func Load(data []byte) (Config, error) {
	var c Config
	if e := json.Unmarshal(data, &c); e != nil {
		return c, e
	}
	if e := c.Validate(); e != nil {
		return c, e
	}
	for i := range c.Sources {
		c.Sources[i].MockResources = c.Resources
	}
	return c, nil
}
func DefaultConfig() (Config, error) {
	b, e := fs.ReadFile(Fixtures, "fixtures/sources.json")
	if e != nil {
		return Config{}, e
	}
	return Load(b)
}
func (c Config) Validate() error {
	if c.Version == "" || len(c.Sources) == 0 || len(c.Sources) > 100 {
		return errors.New("missing sources")
	}
	seen := map[string]bool{}
	for _, s := range c.Sources {
		if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`).MatchString(s.ID) || seen[s.ID] || s.Version == "" || s.PollSeconds < 1 || s.Priority < 0 || s.Priority > 10 || s.MaxPages < 1 || s.MaxPages > 10 || s.MaxItems < 1 || s.MaxItems > 1000 {
			return fmt.Errorf("invalid source %q", s.ID)
		}
		seen[s.ID] = true
		if s.Kind != "rss" && s.Kind != "atom" && s.Kind != "html" && s.Kind != "json" {
			return errors.New("unsupported adapter")
		}
		if _, e := time.LoadLocation(s.Timezone); e != nil {
			return fmt.Errorf("source %s: invalid timezone", s.ID)
		}
		if e := AllowedURL(s.URL, s.AllowedHosts); e != nil {
			return fmt.Errorf("source %s: %w", s.ID, e)
		}
		for _, selector := range s.Selectors {
			if selector != "" {
				if _, e := cascadia.Compile(selector); e != nil {
					return fmt.Errorf("invalid selector: %w", e)
				}
			}
		}
		if s.Kind == "html" && s.Selectors["items"] == "" {
			return errors.New("HTML list requires items selector")
		}
		if s.Kind == "json" && s.JSONFields["items"] == "" {
			return errors.New("JSON source requires items mapping")
		}
	}
	return nil
}
func AllowedURL(raw string, hosts []string) error {
	if len(raw) > 4096 {
		return errors.New("url_too_long")
	}
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return errors.New("invalid_url")
	}
	for _, host := range hosts {
		if strings.EqualFold(u.Hostname(), host) {
			return nil
		}
	}
	return errors.New("host_not_allowed")
}
func Resolve(base, reference string) string {
	if strings.TrimSpace(reference) == "" {
		return ""
	}
	b, e := url.Parse(base)
	if e != nil {
		return ""
	}
	r, e := url.Parse(reference)
	if e != nil {
		return ""
	}
	return b.ResolveReference(r).String()
}
