package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ErrInvalid = errors.New("invalid_llm_contract")
var Sectors = []string{"Нефть и газ", "Металлургия и добыча", "Энергетика", "Строительство", "Недвижимость", "Оптовая торговля", "Розничная торговля", "Автомобили", "Транспорт и логистика", "ИТ и телеком", "Финансы", "Агро и пищевая промышленность", "Химия и удобрения", "Машиностроение и промышленность", "Услуги и прочее", "Здравоохранение и социальная сфера"}
var Factors = []string{"ставка", "инфляция", "USD", "CNY", "EUR", "нефть", "газ", "сталь", "уголь", "удобрения", "зерно", "золото", "тарифы", "налоги", "санкции", "пошлины"}
var channels = []string{"industry", "direct_industry", "competitor", "demand", "supply", "financial", "currency", "fx_ved", "commodity", "regional", "regulatory"}
var okved = regexp.MustCompile(`^[0-9]{2}$`)

func Strict(b []byte, out any) error {
	if len(b) == 0 || len(b) > 2<<20 {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(out); e != nil {
		return ErrInvalid
	}
	if d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func Contains(values []string, s string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}
func bounded(s string, n int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= n
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1 }
func ValidateSource(s Source) bool {
	u, e := url.Parse(s.URL)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil && len(s.URL) <= 2048 && bounded(s.Name, 300) && !s.PublishedAt.IsZero() && Contains([]string{"news", "bank_analytics"}, s.Kind)
}
func ValidateTagInput(in TagInput) error {
	if !bounded(in.EventID, 128) || in.InputRevision < 1 || !bounded(in.Title, 500) || !bounded(in.Text, 20000) || in.CollectedAt.IsZero() || len(in.Sources) > 10 {
		return ErrInvalid
	}
	for _, s := range in.Sources {
		if !ValidateSource(s) || s.Kind != "news" {
			return ErrInvalid
		}
	}
	return nil
}
func ValidateTagResult(in TagInput, r TagResult) error {
	if r.Status != "ready" {
		if !Contains([]string{"noise", "insufficient_data"}, r.Status) || r.Event != nil || !bounded(r.Reason, 500) {
			return ErrInvalid
		}
		return nil
	}
	if r.Event == nil || r.Reason != "" {
		return ErrInvalid
	}
	e := r.Event
	if e.EventID != in.EventID || e.Title != in.Title || !e.CollectedAt.Equal(in.CollectedAt) || !bounded(e.Summary, 2000) || len(e.Sources) == 0 || e.Importance < 1 || e.Importance > 5 || !Contains([]string{"regulation", "macro", "commodity", "corporate", "industry", "regional", "sanctions"}, e.Type) || !Contains([]string{"positive", "negative", "unclear"}, e.Direction) || !Contains([]string{"short", "medium", "long"}, e.Horizon) {
		return ErrInvalid
	}
	a, _ := json.Marshal(e.Sources)
	b, _ := json.Marshal(in.Sources)
	if !bytes.Equal(a, b) {
		return ErrInvalid
	}
	for _, pair := range []struct{ got, allowed []string }{{e.Sectors, Sectors}, {e.Factors, Factors}} {
		if pair.got == nil || len(pair.got) > 20 {
			return ErrInvalid
		}
		seen := map[string]bool{}
		for _, s := range pair.got {
			if !Contains(pair.allowed, s) || seen[s] {
				return ErrInvalid
			}
			seen[s] = true
		}
	}
	if e.OKVED == nil || e.Companies == nil || e.Regions == nil || len(e.OKVED) > 30 || len(e.Companies) > 30 || len(e.Regions) > 30 {
		return ErrInvalid
	}
	for _, v := range e.OKVED {
		if !okved.MatchString(v) {
			return ErrInvalid
		}
	}
	for _, list := range [][]string{e.Companies, e.Regions} {
		for _, v := range list {
			if !bounded(v, 300) {
				return ErrInvalid
			}
		}
	}
	return nil
}
func ValidateMatch(m Match) error {
	if !bounded(m.CompanyID, 128) || !bounded(m.EventID, 128) || m.ProfileVersion < 1 || m.EventVersion < 1 || !bounded(m.RulesetVersion, 128) || !finite(m.Score) || len(m.Breakdown) == 0 || len(m.Breakdown) > 50 {
		return ErrInvalid
	}
	for _, b := range m.Breakdown {
		if !Contains(channels, b.Channel) || !finite(b.Weight) || !finite(b.Match) || !finite(b.Contribution) || math.Abs(b.Contribution-b.Weight*b.Match) > 0.001 {
			return ErrInvalid
		}
	}
	return nil
}
func ValidateInsightInput(in InsightInput) error {
	if in.BusinessProfile != nil && ValidateProfileBrief(ProfileInput{Company: in.Company, Profile: in.Profile, Features: in.Features}, *in.BusinessProfile) != nil {
		return ErrInvalid
	}
	if ValidateMatch(in.Match) != nil || in.Company.ID == "" || in.Profile.CompanyID != in.Company.ID || in.Match.CompanyID != in.Company.ID || in.Profile.Version != in.Match.ProfileVersion || in.Match.EventID != in.Prepared.Event.EventID || in.Match.EventVersion != in.Prepared.EventVersion || in.Prepared.SchemaVersion != ContractVersion || !Contains([]string{"public", "company:" + in.Company.ID}, in.Prepared.Scope) || len(in.Features) > 50 || len(in.Bank) > 5 {
		return ErrInvalid
	}
	e := in.Prepared.Event
	if ValidateTagResult(TagInput{EventID: e.EventID, InputRevision: in.Prepared.InputRevision, Title: e.Title, Text: e.Summary, Sources: e.Sources, CollectedAt: e.CollectedAt}, TagResult{Status: "ready", Event: &e}) != nil {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, f := range in.Features {
		if !bounded(f.ID, 128) || !bounded(f.Text, 1000) || seen[f.ID] {
			return ErrInvalid
		}
		seen[f.ID] = true
	}
	for _, b := range in.Bank {
		if !bounded(b.ID, 128) || !bounded(b.Text, 4000) || !ValidateSource(b.Source) || b.Source.Kind != "bank_analytics" {
			return ErrInvalid
		}
	}
	return nil
}
func allowedSources(in InsightInput) map[string]string {
	out := map[string]string{}
	for _, s := range in.Prepared.Event.Sources {
		out[s.URL] = "fact"
	}
	for _, b := range in.Bank {
		out[b.Source.URL] = "bank"
	}
	return out
}
func ValidateCard(in InsightInput, c Card) error {
	if !bounded(c.Summary, 2000) || len(strings.Fields(c.Summary)) > 120 || !Contains([]string{"low", "medium", "high"}, c.Confidence) || len(c.Statements) < 1 || len(c.Statements) > 20 || c.Limitations == nil || len(c.Limitations) > 10 {
		return ErrInvalid
	}
	for _, s := range []string{c.Blocks.What, c.Blocks.Why, c.Blocks.Consequences, c.Blocks.Actions, c.Blocks.BankView, c.Blocks.Basis} {
		if !bounded(s, 3000) {
			return ErrInvalid
		}
	}
	sources := allowedSources(in)
	features := map[string]bool{}
	for _, f := range in.Features {
		features[f.ID] = true
	}
	used := map[string]bool{}
	for _, s := range c.Statements {
		if !bounded(s.Text, 3000) || !Contains([]string{"fact", "bank", "ai"}, s.Layer) || s.Sources == nil || s.ProfileFeatures == nil || len(s.Sources) > 10 || len(s.ProfileFeatures) > 50 {
			return ErrInvalid
		}
		for _, ref := range s.Sources {
			layer, ok := sources[ref]
			if !ok || (s.Layer != "ai" && layer != s.Layer) {
				return ErrInvalid
			}
		}
		for _, id := range s.ProfileFeatures {
			if !features[id] {
				return ErrInvalid
			}
			used[id] = true
		}
		if (s.Layer == "fact" || s.Layer == "bank") && len(s.Sources) == 0 {
			return ErrInvalid
		}
		if s.Layer == "ai" && len(s.Sources)+len(s.ProfileFeatures) == 0 {
			return ErrInvalid
		}
	}
	if len(in.Features) >= 2 && len(used) < 2 {
		return ErrInvalid
	}
	if len(in.Features) < 2 && (c.Confidence != "low" || len(c.Limitations) == 0) {
		return ErrInvalid
	}
	if len(in.Bank) == 0 && c.Blocks.BankView != "Нет данных аналитики банка." {
		return ErrInvalid
	}
	return nil
}
func ValidateChat(in ChatInput) error {
	if ValidateInsightInput(in.Context) != nil || ValidateCard(in.Context, in.Insight.Card) != nil || in.Insight.CompanyID != in.Context.Company.ID || !bounded(in.Question, 4000) || len(in.History) > 12 {
		return ErrInvalid
	}
	for _, m := range in.History {
		if !Contains([]string{"user", "assistant"}, m.Role) || !bounded(m.Text, 4000) {
			return ErrInvalid
		}
	}
	return nil
}
func ValidateAnswer(in ChatInput, a Answer) error {
	if !bounded(a.Text, 4000) || a.Sources == nil || len(a.Sources) > 10 || a.Limitations == nil || len(a.Limitations) > 10 || (!a.InsufficientData && len(a.Sources) == 0) {
		return ErrInvalid
	}
	refs := allowedSources(in.Context)
	for _, s := range a.Sources {
		if _, ok := refs[s]; !ok {
			return ErrInvalid
		}
	}
	return nil
}

func ValidateInput(c Call) error {
	if c.Scope != "public" && (!strings.HasPrefix(c.Scope, "company:") || len(c.Scope) <= 8) {
		return ErrInvalid
	}
	switch c.Task {
	case "profile":
		var in ProfileInput
		if Strict(c.Input, &in) != nil || c.Scope != "company:"+in.Company.ID {
			return ErrInvalid
		}
		return ValidateProfileInput(in)
	case "tag":
		var in TagInput
		if Strict(c.Input, &in) != nil {
			return ErrInvalid
		}
		return ValidateTagInput(in)
	case "insight", "final":
		var in InsightInput
		if Strict(c.Input, &in) != nil || c.Scope != "company:"+in.Company.ID {
			return ErrInvalid
		}
		return ValidateInsightInput(in)
	case "check":
		var in CheckInput
		if Strict(c.Input, &in) != nil || c.Scope != "company:"+in.Context.Company.ID || ValidateInsightInput(in.Context) != nil {
			return ErrInvalid
		}
		return ValidateCard(in.Context, in.Card)
	case "chat":
		var in ChatInput
		if Strict(c.Input, &in) != nil || c.Scope != "company:"+in.Context.Company.ID {
			return ErrInvalid
		}
		return ValidateChat(in)
	default:
		return ErrInvalid
	}
}
func ValidateOutput(c Call, b []byte) error {
	switch c.Task {
	case "profile":
		var in ProfileInput
		var out ProfileBrief
		_ = Strict(c.Input, &in)
		if Strict(b, &out) != nil {
			return ErrInvalid
		}
		return ValidateProfileBrief(in, out)
	case "tag":
		var in TagInput
		var out TagResult
		_ = Strict(c.Input, &in)
		if Strict(b, &out) != nil {
			return ErrInvalid
		}
		return ValidateTagResult(in, out)
	case "insight", "final":
		var in InsightInput
		var out Card
		_ = Strict(c.Input, &in)
		if Strict(b, &out) != nil {
			return ErrInvalid
		}
		return ValidateCard(in, out)
	case "check":
		var out CheckResult
		if Strict(b, &out) != nil || out.Reasons == nil || len(out.Reasons) > 10 || (!out.Passed && len(out.Reasons) == 0) {
			return ErrInvalid
		}
		return nil
	case "chat":
		var in ChatInput
		var out Answer
		_ = Strict(c.Input, &in)
		if Strict(b, &out) != nil {
			return ErrInvalid
		}
		return ValidateAnswer(in, out)
	}
	return ErrInvalid
}

func ValidateProfileInput(in ProfileInput) error {
	if !bounded(in.Company.ID, 128) || in.Profile.CompanyID != in.Company.ID || in.Profile.Version < 1 || in.Features == nil || len(in.Features) > 50 {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, f := range in.Features {
		if !bounded(f.ID, 128) || !bounded(f.Text, 1000) || seen[f.ID] {
			return ErrInvalid
		}
		seen[f.ID] = true
	}
	return nil
}
func ValidateProfileBrief(in ProfileInput, out ProfileBrief) error {
	if out.CompanyID != in.Company.ID || out.ProfileVersion != in.Profile.Version || !bounded(out.Summary, 2000) || out.Observations == nil || out.Hypotheses == nil || out.Limitations == nil || len(out.Observations) > 50 || len(out.Hypotheses) > 10 || len(out.Limitations) > 10 {
		return ErrInvalid
	}
	ids := map[string]bool{}
	for _, f := range in.Features {
		ids[f.ID] = true
	}
	for _, list := range [][]ProfileObservation{out.Observations, out.Hypotheses} {
		for _, v := range list {
			if !bounded(v.Text, 1000) || len(v.FeatureIDs) < 1 || len(v.FeatureIDs) > 10 {
				return ErrInvalid
			}
			for _, id := range v.FeatureIDs {
				if !ids[id] {
					return ErrInvalid
				}
			}
		}
	}
	for _, v := range out.Limitations {
		if !bounded(v, 1000) {
			return ErrInvalid
		}
	}
	return nil
}
