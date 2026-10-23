package pipeline

import (
	"alfa.local/shared/jobs"
	"alfa.local/shared/llm"
	"testing"
	"time"
)

func TestProviderPolicySurvivesGRPC(t *testing.T) {
	for _, original := range []*llm.ProviderError{{Code: "llm_outcome_unknown", Ambiguous: true}, {Code: "provider_rate_limited", Retryable: true, Wait: 7 * time.Second}, {Code: "provider_http_401"}} {
		_, err := Result(nil, original)
		decision := jobs.Policy(err)
		if decision.Reason != original.Code || decision.Terminal != original.Terminal() || decision.Uncertain != original.Ambiguous || decision.Delay != original.Wait {
			t.Fatal(decision, err)
		}
	}
}
