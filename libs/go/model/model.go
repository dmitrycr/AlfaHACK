package model

import (
	"encoding/json"
	"time"
)

const SchemaVersion = 1

type OKVED struct {
	Section string `json:"section"`
	Class   string `json:"class"`
	Group   string `json:"group"`
}
type Company struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	OKVED        OKVED            `json:"okved"`
	Sector       string           `json:"sector"`
	Region       string           `json:"region"`
	RevenueMinor *int64           `json:"revenue_minor,omitempty"`
	Products     map[string]*bool `json:"products"`
	VED          map[string]any   `json:"ved"`
}
type Channel struct {
	Name     string   `json:"name"`
	Sector   string   `json:"sector,omitempty"`
	Weight   float64  `json:"weight"`
	Source   string   `json:"source"`
	Evidence []string `json:"evidence"`
}
type Profile struct {
	CompanyID      string              `json:"company_id"`
	Version        int                 `json:"version"`
	RulesetVersion string              `json:"ruleset_version"`
	Channels       []Channel           `json:"channels"`
	FactorExposure map[string]*float64 `json:"factor_exposure"`
	Coverage       *float64            `json:"coverage"`
	BuiltAt        time.Time           `json:"built_at"`
	Synthetic      bool                `json:"synthetic"`
}
type Article struct {
	ID             string     `json:"id"`
	Revision       int        `json:"revision"`
	SourceID       string     `json:"source_id"`
	ExternalID     string     `json:"external_id"`
	OriginalURL    string     `json:"original_url"`
	URL            string     `json:"url"`
	Title          string     `json:"title"`
	Text           string     `json:"text"`
	PublishedAt    *time.Time `json:"published_at"`
	ModifiedAt     *time.Time `json:"modified_at,omitempty"`
	CollectedAt    time.Time  `json:"collected_at"`
	RawDate        string     `json:"raw_date"`
	ContentHash    string     `json:"content_hash"`
	DuplicateGroup string     `json:"duplicate_group"`
	Scope          string     `json:"scope"`
	Origin         string     `json:"origin"`
	Partial        bool       `json:"partial"`
	Flags          []string   `json:"quality_flags"`
	AdapterVersion string     `json:"adapter_version"`
	ConfigVersion  string     `json:"config_version"`
	CorrelationID  string     `json:"correlation_id"`
}

// ArticleSummary bounds list responses; full text is available through GetArticle.
type ArticleSummary struct {
	ID             string     `json:"id"`
	Revision       int        `json:"revision"`
	SourceID       string     `json:"source_id"`
	URL            string     `json:"url"`
	Title          string     `json:"title"`
	Excerpt        string     `json:"excerpt"`
	PublishedAt    *time.Time `json:"published_at"`
	CollectedAt    time.Time  `json:"collected_at"`
	DuplicateGroup string     `json:"duplicate_group"`
	Scope          string     `json:"scope"`
	Origin         string     `json:"origin"`
}
type Envelope struct {
	MessageID     string          `json:"message_id"`
	SchemaVersion int             `json:"schema_version"`
	Type          string          `json:"type"`
	EntityID      string          `json:"entity_id"`
	EntityVersion int             `json:"entity_version"`
	Scope         string          `json:"scope"`
	CorrelationID string          `json:"correlation_id"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Payload       json.RawMessage `json:"payload"`
}
type Tags struct {
	Type            string   `json:"type"`
	Sectors         []string `json:"sectors"`
	OKVEDClasses    []string `json:"okved_classes"`
	Factors         []string `json:"factors"`
	Importance      int      `json:"importance"`
	InputRevision   int      `json:"input_revision"`
	ProviderVersion string   `json:"provider_version"`
}
type Event struct {
	ID        string       `json:"id"`
	Scope     string       `json:"scope"`
	Title     string       `json:"title"`
	Version   int          `json:"version"`
	Status    string       `json:"status"`
	Articles  []ArticleRef `json:"articles"`
	Tags      *Tags        `json:"tags,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}
type ArticleRef struct {
	ArticleID string `json:"article_id"`
	Revision  int    `json:"revision"`
	SourceID  string `json:"source_id"`
	URL       string `json:"url"`
}
type Submission struct {
	ID        string    `json:"id"`
	CompanyID string    `json:"company_id"`
	UserID    string    `json:"user_id"`
	Key       string    `json:"-"`
	Status    string    `json:"status"`
	ArticleID string    `json:"article_id,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}
type Page[T any] struct {
	Items  []T `json:"items"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total"`
}
