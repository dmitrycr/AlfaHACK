package llm

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

type failure string

func (e failure) Error() string   { return string(e) }
func (e failure) JobCode() string { return string(e) }

var ErrTemporary = failure("llm_temporary_failure")
var ErrBudget = failure("llm_budget_exceeded")
var ErrBusy = failure("llm_request_in_progress")

type Usage struct {
	InputTokens, OutputTokens int
	CostMinor                 int64
	ReasoningTokens           int
	Known                     bool
	Sent                      bool
}
type Provider interface {
	Version() string
	Complete(context.Context, Call) (json.RawMessage, Usage, error)
}

// Mock never performs network I/O. Token counts and prices are simulated.
// Scenario is startup/test configuration, never a field of an API request.
type Mock struct{ Scenario string }

func (Mock) Version() string           { return "mock-v1" }
func PromptVersion(task string) string { return task + "-backend-v1" }
func (m Mock) Complete(ctx context.Context, c Call) (json.RawMessage, Usage, error) {
	switch m.Scenario {
	case "timeout":
		<-ctx.Done()
		return nil, Usage{}, ctx.Err()
	case "rate_limit", "unavailable":
		return nil, Usage{}, ErrTemporary
	case "invalid_json":
		return json.RawMessage(`{"broken":`), Usage{CostMinor: 1}, nil
	}
	if e := ctx.Err(); e != nil {
		return nil, Usage{}, e
	}
	var out any
	switch c.Task {
	case "profile":
		var in ProfileInput
		_ = Strict(c.Input, &in)
		brief := ProfileBrief{CompanyID: in.Company.ID, ProfileVersion: in.Profile.Version, Summary: "Синтетическая интерпретация подготовленного профиля компании.", Observations: []ProfileObservation{}, Hypotheses: []ProfileObservation{}, Limitations: []string{"Мок-провайдер; числовые показатели не изменяются."}}
		for _, f := range in.Features {
			brief.Observations = append(brief.Observations, ProfileObservation{Text: f.Text, FeatureIDs: []string{f.ID}})
		}
		out = brief
	case "tag":
		var in TagInput
		_ = Strict(c.Input, &in)
		if len(in.Sources) == 0 {
			out = TagResult{Status: "insufficient_data", Reason: "Нет источника с URL и датой публикации."}
			break
		}
		text := strings.ToLower(in.Title + " " + in.Text)
		if strings.Contains(text, "футбол") || strings.Contains(text, "гороскоп") || strings.Contains(text, "рекламная акция") {
			out = TagResult{Status: "noise", Reason: "Синтетический фильтр: непрофильная публикация."}
			break
		}
		e := ReadyEvent{EventID: in.EventID, Title: in.Title, Summary: in.Title, Sources: in.Sources, Type: "corporate", Sectors: []string{}, OKVED: []string{}, Companies: []string{}, Regions: []string{}, Factors: []string{}, Direction: "unclear", Horizon: "short", Importance: 3, CollectedAt: in.CollectedAt}
		if strings.Contains(text, "ставк") {
			e.Type = "macro"
			e.Factors = []string{"ставка"}
			e.Sectors = []string{"Финансы"}
			e.Importance = 4
		}
		if strings.Contains(text, "сталь") || strings.Contains(text, "металлург") {
			e.Type = "industry"
			e.Sectors = append(e.Sectors, "Металлургия и добыча")
			e.Factors = append(e.Factors, "сталь")
		}
		if strings.Contains(text, "строит") || strings.Contains(text, "ипотек") {
			e.Sectors = append(e.Sectors, "Строительство")
		}
		out = TagResult{Status: "ready", Event: &e}
	case "insight", "final":
		var in InsightInput
		_ = Strict(c.Input, &in)
		urls := []string{}
		for _, s := range in.Prepared.Event.Sources {
			urls = append(urls, s.URL)
		}
		ids := []string{}
		texts := []string{}
		for _, f := range in.Features {
			ids = append(ids, f.ID)
			texts = append(texts, f.Text)
		}
		why := "Недостаточно признаков компании для персонального вывода."
		if len(texts) > 0 {
			why = strings.Join(texts, "; ")
		}
		card := Card{Summary: in.Prepared.Event.Summary, Confidence: "low", Limitations: []string{"Синтетическая карточка мок-провайдера; не результат работы реальной модели."}, Blocks: Blocks{What: in.Prepared.Event.Summary, Why: why, Consequences: "Влияние на компанию требует проверки по указанным признакам и источникам.", Actions: "Проверить актуальность источников и оценить влияние с ответственным сотрудником.", BankView: "Нет данных аналитики банка.", Basis: "Исходная новость, версия профиля и объяснение оценки релевантности."}, Statements: []Statement{{Text: in.Prepared.Event.Summary, Layer: "fact", Sources: urls, ProfileFeatures: []string{}}, {Text: "Возможное влияние требует проверки с учётом признаков компании.", Layer: "ai", Sources: urls, ProfileFeatures: ids}}}
		if len(ids) < 2 {
			card.Limitations = append(card.Limitations, "Недостаточно признаков компании: персонализация ограничена.")
		}
		if len(in.Bank) > 0 {
			b := in.Bank[0]
			card.Blocks.BankView = b.Text
			card.Statements = append(card.Statements, Statement{Text: b.Text, Layer: "bank", Sources: []string{b.Source.URL}, ProfileFeatures: []string{}})
		}
		out = card
	case "check":
		var in CheckInput
		_ = Strict(c.Input, &in)
		ok := ValidateCard(in.Context, in.Card) == nil
		reasons := []string{}
		if !ok {
			reasons = append(reasons, "Нарушена структура или ссылки на основания.")
		}
		out = CheckResult{Passed: ok, Reasons: reasons}
	case "chat":
		var in ChatInput
		_ = Strict(c.Input, &in)
		q := strings.ToLower(in.Question)
		a := Answer{Text: "В сохранённом контексте недостаточно данных для ответа на этот вопрос.", Sources: []string{}, InsufficientData: true, Limitations: []string{"Ответ мок-провайдера; внешний поиск не выполнялся."}}
		if strings.Contains(q, "произош") || strings.Contains(q, "источник") || strings.Contains(q, "новост") || strings.Contains(q, "карточк") {
			a.Text = in.Insight.Card.Blocks.What
			a.InsufficientData = false
			for _, s := range in.Context.Prepared.Event.Sources {
				a.Sources = append(a.Sources, s.URL)
			}
		}
		out = a
	default:
		return nil, Usage{}, ErrInvalid
	}
	b, e := json.Marshal(out)
	return b, Usage{InputTokens: (len([]rune(string(c.Input))) + 3) / 4, OutputTokens: (len([]rune(string(b))) + 3) / 4, CostMinor: 1}, e
}

type Knowledge interface {
	Search(context.Context, ReadyEvent) ([]BankExcerpt, error)
}
type MockKnowledge struct{}

func (MockKnowledge) Search(_ context.Context, e ReadyEvent) ([]BankExcerpt, error) {
	if !Contains(e.Factors, "ставка") {
		return []BankExcerpt{}, nil
	}
	return []BankExcerpt{{ID: "synthetic-bank-rate-v1", Source: Source{URL: "https://bank.example.test/analytics/mock-rate", Name: "Синтетическая аналитика банка для тестирования", PublishedAt: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC), Kind: "bank_analytics", FragmentID: "mock-1"}, Text: "Синтетический материал: изменение ставки может повлиять на стоимость финансирования.", Synthetic: true}}, nil
}
