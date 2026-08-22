package proxy

import "testing"

func TestParseUsageDetails(t *testing.T) {
	usage := parseUsage([]byte(`{
		"usage": {
			"prompt_tokens": 12,
			"completion_tokens": 8,
			"total_tokens": 20,
			"prompt_tokens_details": {"cached_tokens": 5},
			"completion_tokens_details": {"reasoning_tokens": 3}
		}
	}`))
	if usage.PromptTokens != 12 || usage.CompletionTokens != 8 || usage.TotalTokens != 20 ||
		usage.PromptTokensDetails.CachedTokens != 5 || usage.CompletionTokensDetails.ReasoningTokens != 3 {
		t.Fatalf("usage = %+v", usage)
	}
}
