package proxy

import "encoding/json"

type usageInfo struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
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
