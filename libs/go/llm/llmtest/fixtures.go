// Package llmtest provides explicitly synthetic fixtures for integration tests.
package llmtest

import (
	"alfa.local/shared/llm"
	"alfa.local/shared/model"
	"time"
)

func Tag() llm.TagInput {
	return llm.TagInput{EventID: "event-test", InputRevision: 1, Title: "Изменение ключевой ставки", Text: "Синтетическая новость о стоимости финансирования компаний.", Sources: []llm.Source{{URL: "https://news.example.test/rate", Name: "Синтетический источник", PublishedAt: time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC), Kind: "news"}}, CollectedAt: time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)}
}
func Context() llm.InsightInput {
	t := Tag()
	event := llm.ReadyEvent{EventID: t.EventID, Title: t.Title, Summary: t.Title, Sources: t.Sources, Type: "macro", Sectors: []string{"Финансы"}, OKVED: []string{}, Companies: []string{}, Regions: []string{}, Factors: []string{"ставка"}, Direction: "unclear", Horizon: "short", Importance: 4, CollectedAt: t.CollectedAt}
	return llm.InsightInput{Company: model.Company{ID: "company-test", Name: "Синтетическая компания", Sector: "Металлургия", Region: "Тестовый регион", Products: map[string]*bool{}, VED: map[string]any{}}, Profile: model.Profile{CompanyID: "company-test", Version: 1, RulesetVersion: "test-v1", Channels: []model.Channel{}, FactorExposure: map[string]*float64{}, BuiltAt: t.CollectedAt, Synthetic: true}, Prepared: llm.Prepared{SchemaVersion: llm.ContractVersion, EventVersion: 2, InputRevision: 1, Scope: "public", PromptVersion: llm.PromptVersion("tag"), VocabularyVersion: llm.VocabularyVersion, ProviderVersion: "mock-v1", Synthetic: true, Event: event}, Match: llm.Match{CompanyID: "company-test", EventID: t.EventID, ProfileVersion: 1, EventVersion: 2, RulesetVersion: "mock-match-v1", Score: 0.5, Breakdown: []llm.Contribution{{Channel: "industry", Weight: 1, Match: 0.5, Contribution: 0.5}}, Synthetic: true}, Features: []llm.Feature{{ID: "company.sector", Text: "Отрасль: металлургия"}, {ID: "company.region", Text: "Регион: тестовый"}}, Bank: []llm.BankExcerpt{}}
}
