package service

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"alfa.local/news-service/internal/repository/postgres"
	"alfa.local/news-service/internal/source"
	"alfa.local/shared/model"
	"alfa.local/shared/platform"
	"github.com/jackc/pgx/v5"
)

type Collector struct {
	Store   postgres.Store
	Fetcher source.Fetcher
	Now     func() time.Time
}

func (c *Collector) fetcher(s source.Source) source.Fetcher {
	fetcher := c.Fetcher
	if fixture, ok := fetcher.(source.FixtureFetcher); ok && len(s.MockResources) > 0 {
		fixture.Resources = s.MockResources
		return fixture
	}
	return fetcher
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now().UTC()
}
func (c *Collector) Collect(ctx context.Context, s source.Source, owner string) error {
	parsed, e := url.Parse(s.URL)
	if e != nil {
		return e
	}
	host := parsed.Hostname()
	acquired, e := c.Store.AcquireHost(ctx, host, owner)
	if e != nil {
		return e
	}
	if !acquired {
		if e = c.Store.DeferSource(ctx, s.ID, owner); e != nil {
			return e
		}
		return postgres.ErrHostBusy
	}
	defer c.releaseHost(ctx, host, owner)
	stage, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	run, e := c.Store.NewRun(stage, s, owner)
	if e != nil {
		return e
	}
	runErr := c.discover(stage, s, owner, run)
	if e = c.Store.EndRun(ctx, s, owner, run, runErr); e != nil {
		return e
	}
	return runErr
}
func (c *Collector) releaseHost(ctx context.Context, host, owner string) {
	release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if e := c.Store.ReleaseHost(release, host, owner); e != nil {
		slog.Warn("host lease release pending", "error", e)
	}
}
func (c *Collector) discover(ctx context.Context, s source.Source, owner, run string) error {
	next := s.URL
	visited := map[string]bool{}
	remaining := s.MaxItems
	for page := 0; next != "" && page < s.MaxPages; page++ {
		if visited[next] {
			return errors.New("pagination_cycle")
		}
		visited[next] = true
		tag, modified := c.Store.Cache(ctx, s.ID, next)
		r, e := c.fetcher(s).Fetch(ctx, source.FetchRequest{URL: next, AllowedHosts: s.AllowedHosts, ETag: tag, Modified: modified})
		if e != nil {
			return e
		}
		if r.Status == 304 {
			r.Body, e = c.Store.CachedBody(ctx, s.ID, next)
			if e != nil {
				return e
			}
			r.ETag = tag
			r.Modified = modified
		}
		if e = c.Store.RecordRaw(ctx, "public", s.ID, r); e != nil {
			return e
		}
		d, e := source.Parse(s, r.Body, next)
		if e != nil {
			return e
		}
		if len(d.Items) > remaining {
			return errors.New("source_item_limit_exceeded")
		}
		remaining -= len(d.Items)
		if e = c.Store.Discovered(ctx, s, owner, run, r, d.Items); e != nil {
			return e
		}
		next = d.Next
	}
	if next != "" {
		return errors.New("pagination_limit_exceeded")
	}
	return nil
}
func (c *Collector) ProcessJob(ctx context.Context, j postgres.Job) error {
	var a model.Article
	var response *source.FetchResponse
	if j.Origin == "manual_text" {
		a = model.Article{SourceID: "manual", ExternalID: j.SubmissionID, Title: source.Clean(j.Candidate.Title), Text: source.Clean(j.Candidate.Text), CollectedAt: c.now(), Scope: j.Scope, Origin: "manual", RawDate: j.Candidate.RawDate, AdapterVersion: source.AdapterVersion, ConfigVersion: "manual-v1", CorrelationID: j.CorrelationID, Flags: []string{}}
		if a.Title == "" || a.Text == "" {
			return c.Store.FailJob(ctx, j, errors.New("invalid_manual_text"))
		}
		if a.RawDate != "" {
			date, e := source.ParseDate(a.RawDate, j.Source)
			if e != nil {
				return c.Store.FailJob(ctx, j, e)
			}
			a.PublishedAt = date
		} else {
			a.Flags = append(a.Flags, "publication_date_unknown")
		}
		a.ContentHash = platform.Hash(a.Title + " " + a.Text)
		a.DuplicateGroup = platform.Hash(a.Scope + ":" + a.ContentHash)
	} else {
		candidate := j.Candidate
		if candidate.NeedsFetch {
			if e := source.AllowedURL(candidate.URL, j.Source.AllowedHosts); e != nil {
				return c.Store.FailJob(ctx, j, e)
			}
			address, e := url.Parse(candidate.URL)
			if e != nil {
				return c.Store.FailJob(ctx, j, errors.New("invalid_url"))
			}
			host := address.Hostname()
			acquired, e := c.Store.AcquireHost(ctx, host, j.Owner)
			if e != nil {
				return e
			}
			if !acquired {
				return c.Store.DeferJob(ctx, j)
			}
			defer c.releaseHost(ctx, host, j.Owner)
			fetcher := c.Fetcher
			if fixture, ok := fetcher.(source.FixtureFetcher); ok && len(j.Source.MockResources) > 0 {
				fixture.Resources = j.Source.MockResources
				fetcher = fixture
			}
			r, e := fetcher.Fetch(ctx, source.FetchRequest{URL: candidate.URL, AllowedHosts: j.Source.AllowedHosts})
			if e != nil {
				return c.Store.FailJob(ctx, j, e)
			}
			response = &r
			if e = c.Store.RecordRaw(ctx, j.Scope, j.Source.ID, r); e != nil {
				return e
			}
			parsed, e := source.ParseArticle(j.Source, r.Body, r.URL)
			if e != nil {
				return c.Store.FailJob(ctx, j, e)
			}
			parsed.ExternalID = candidate.ExternalID
			if parsed.RawDate == "" {
				parsed.RawDate = candidate.RawDate
			}
			if parsed.Title == "" {
				parsed.Title = candidate.Title
			}
			candidate = parsed
		}
		var e error
		a, e = source.Normalize(j.Source, candidate, j.Scope, j.CorrelationID, c.now())
		if e != nil {
			return c.Store.FailJob(ctx, j, e)
		}
		a.OriginalURL = j.Candidate.URL
		if j.Origin == "manual_url" {
			a.Origin = "manual_url"
		}
	}
	_, changed, e := c.Store.SaveArticle(ctx, j, a, response)
	if e != nil {
		if e.Error() == "identity_conflict" {
			return c.Store.FailJob(ctx, j, e)
		}
		return e
	}
	slog.Info("article processed", "source_id", a.SourceID, "job_id", j.ID, "correlation_id", j.CorrelationID, "changed", changed)
	return nil
}
func (c *Collector) WorkOnce(ctx context.Context) (bool, error) {
	s, _, owner, e := c.Store.ClaimSource(ctx)
	if e == nil {
		err := c.Collect(ctx, s, owner)
		if errors.Is(err, postgres.ErrHostBusy) {
			return true, nil
		}
		return true, err
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return false, e
	}
	j, e := c.Store.ClaimJob(ctx)
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return true, c.ProcessJob(ctx, j)
}
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		for i := 0; i < 25; i++ {
			did, e := c.WorkOnce(ctx)
			if e != nil {
				slog.Warn("collection step failed", "error", e)
				break
			}
			if !did {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type SubmitInput struct {
	Title       string `json:"title"`
	Text        string `json:"text"`
	URL         string `json:"url"`
	PublishedAt string `json:"published_at,omitempty"`
	Key         string `json:"idempotency_key"`
}

func PrepareSubmission(input SubmitInput, cfg source.Config) (postgres.Job, error) {
	var j postgres.Job
	if len(input.Key) < 8 || len(input.Key) > 128 {
		return j, errors.New("invalid_idempotency_key")
	}
	if (input.Text == "") == (input.URL == "") {
		return j, errors.New("provide_text_or_url")
	}
	if input.Text != "" {
		if strings.TrimSpace(input.Title) == "" || len([]rune(input.Title)) > 500 || len([]rune(input.Text)) > 200000 {
			return j, errors.New("invalid_manual_text")
		}
		j.Source = source.Source{ID: "manual", Timezone: "UTC", Version: "manual-v1"}
		j.Candidate = source.Candidate{Title: input.Title, Text: input.Text, RawDate: input.PublishedAt}
		j.Origin = "manual_text"
		if input.PublishedAt != "" {
			if _, e := source.ParseDate(input.PublishedAt, j.Source); e != nil {
				return j, e
			}
		}
		return j, nil
	}
	if _, ok := cfg.Resources[input.URL]; !ok {
		return j, errors.New("unknown_mock_url")
	}
	for _, s := range cfg.Sources {
		if s.Enabled && source.AllowedURL(input.URL, s.AllowedHosts) == nil {
			j.Source = s
			j.Candidate = source.Candidate{URL: input.URL, ExternalID: input.URL, NeedsFetch: true}
			j.Origin = "manual_url"
			return j, nil
		}
	}
	return j, errors.New("source_not_configured")
}
func RequestHash(input SubmitInput) string {
	input.Key = ""
	b, _ := json.Marshal(input)
	return platform.Hash(string(b))
}
