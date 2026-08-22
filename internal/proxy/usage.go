package proxy

import "encoding/json"

type usageInfo struct {
	PromptTokens            int64                   `json:"prompt_tokens"`
	CompletionTokens        int64                   `json:"completion_tokens"`
	TotalTokens             int64                   `json:"total_tokens"`
	PromptTokensDetails     promptTokensDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails completionTokensDetails `json:"completion_tokens_details"`
}

type promptTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type completionTokensDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

func parseUsage(body []byte) usageInfo {
	var resp struct {
		Usage usageInfo `json:"usage"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return usageInfo{}
	}
	return resp.Usage
}
