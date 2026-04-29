package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"modelrouter/internal/config"
	"modelrouter/internal/metrics"
	"modelrouter/internal/router"
)

func TestChatCompletionsProxiesRequest(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("unexpected upstream path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-key" {
			t.Fatalf("unexpected authorization header: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"choices": []any{},
			"usage": map[string]int{
				"prompt_tokens":     3,
				"completion_tokens": 4,
				"total_tokens":      7,
			},
		})
	}))
	defer upstream.Close()

	store := router.NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", BaseURL: upstream.URL, APIKey: "upstream-key"},
				},
			},
		},
	})
	recorder := metrics.NewRecorder()
	handler := NewHandler(store, recorder)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handler.ChatCompletions(rr, req)
	handler.Close()

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	stats := recorder.Snapshot()
	if len(stats.Items) != 1 {
		t.Fatalf("expected one stats item, got %d", len(stats.Items))
	}
	if stats.Items[0].TotalTokens != 7 {
		t.Fatalf("total tokens = %d", stats.Items[0].TotalTokens)
	}
}

func TestChatCompletionsRejectsMissingAPIKey(t *testing.T) {
	store := router.NewStore(&config.Config{
		Auth: config.AuthConfig{
			Enabled: true,
			Keys: []config.ClientKeyConfig{
				{Name: "client", Key: "secret", AccessGroup: "group-a"},
			},
		},
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", BaseURL: "http://127.0.0.1"},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[]}`))
	rr := httptest.NewRecorder()

	handler.ChatCompletions(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
}

func TestChatCompletionsRejectsDisallowedModel(t *testing.T) {
	store := router.NewStore(&config.Config{
		Auth: config.AuthConfig{
			Enabled: true,
			Keys: []config.ClientKeyConfig{
				{Name: "client", Key: "secret", AccessGroup: "group-a"},
			},
		},
		AccessGroups: map[string]config.AccessGroupConfig{
			"group-a": {AllowedModels: []string{"allowed"}},
		},
		Models: map[string]config.ModelConfig{
			"blocked": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", BaseURL: "http://127.0.0.1"},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"blocked","messages":[]}`))
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	handler.ChatCompletions(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
}

func TestChatCompletionsRewritesUpstreamModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if req.Model != "Provider/RealModel" {
			t.Fatalf("upstream model = %q", req.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	store := router.NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"public-model": {
				RouteGroup: "group",
			},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", Model: "Provider/RealModel", BaseURL: upstream.URL},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public-model","messages":[]}`))
	rr := httptest.NewRecorder()

	handler.ChatCompletions(rr, req)
	handler.Close()

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
}

func TestEndpointModelOverridesModelUpstreamModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if req.Model != "Endpoint/Model" {
			t.Fatalf("upstream model = %q", req.Model)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer upstream.Close()

	store := router.NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"public-model": {
				RouteGroup:    "group",
				UpstreamModel: "Model/Default",
			},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", Model: "Endpoint/Model", BaseURL: upstream.URL},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"public-model","messages":[]}`))
	rr := httptest.NewRecorder()

	handler.ChatCompletions(rr, req)
	handler.Close()

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
}

func TestChatCompletionsSkipsEndpointAtMaxConcurrency(t *testing.T) {
	firstCalled := false
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalled = true
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer first.Close()

	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer second.Close()

	store := router.NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyFirstAvailable,
				Endpoints: []config.EndpointConfig{
					{Name: "first", BaseURL: first.URL, MaxConcurrency: 1},
					{Name: "second", BaseURL: second.URL},
				},
			},
		},
	})

	route, err := store.Get().Pick("demo", "127.0.0.1")
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if !route.TryAcquire("first") {
		t.Fatal("failed to pre-acquire first endpoint")
	}
	defer route.Release("first")

	handler := NewHandler(store, metrics.NewRecorder())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[]}`))
	rr := httptest.NewRecorder()

	handler.ChatCompletions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if firstCalled {
		t.Fatal("first endpoint should have been skipped")
	}
}

func TestChatCompletionsFallsBackOnBufferedUpstreamFailure(t *testing.T) {
	firstCalled := false
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalled = true
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"upstream failed"}}`))
	}))
	defer first.Close()

	secondCalled := false
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer second.Close()

	store := router.NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyFirstAvailable,
				Endpoints: []config.EndpointConfig{
					{Name: "first", BaseURL: first.URL},
					{Name: "second", BaseURL: second.URL},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[]}`))
	rr := httptest.NewRecorder()

	handler.ChatCompletions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if !firstCalled || !secondCalled {
		t.Fatalf("firstCalled=%v secondCalled=%v", firstCalled, secondCalled)
	}
	if strings.Contains(rr.Body.String(), "upstream failed") {
		t.Fatalf("response should come from fallback endpoint: %s", rr.Body.String())
	}
}

func TestChatCompletionsWritesUsageLogWhenEnabled(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`))
	}))
	defer upstream.Close()

	dir := t.TempDir()
	store := router.NewStore(&config.Config{
		UsageLog: config.UsageLogConfig{Enabled: true, Dir: dir},
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", BaseURL: upstream.URL},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","messages":[]}`))
	rr := httptest.NewRecorder()

	handler.ChatCompletions(rr, req)
	handler.Close()

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	files, err := filepath.Glob(filepath.Join(dir, "usage-*.jsonl"))
	if err != nil {
		t.Fatalf("glob usage logs: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("usage log files = %v", files)
	}
}

func TestChatCompletionsCapturesStreamingUsage(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		streamOptions, ok := req["stream_options"].(map[string]any)
		if !ok || streamOptions["include_usage"] != true {
			t.Fatalf("stream_options.include_usage not injected: %+v", req["stream_options"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	store := router.NewStore(&config.Config{
		Features: config.FeaturesConfig{AutoIncludeStreamUsage: true},
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", BaseURL: upstream.URL},
				},
			},
		},
	})
	recorder := metrics.NewRecorder()
	handler := NewHandler(store, recorder)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"demo","stream":true,"messages":[]}`))
	rr := httptest.NewRecorder()

	handler.ChatCompletions(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	stats := recorder.Snapshot()
	if stats.Summary.TotalTokens != 5 {
		t.Fatalf("total tokens = %d", stats.Summary.TotalTokens)
	}
	if !strings.Contains(rr.Body.String(), "data: [DONE]") {
		t.Fatalf("stream body missing DONE: %s", rr.Body.String())
	}
}

func TestStreamingDetectsReasoningContentAsFirstToken(t *testing.T) {
	event := parseSSEEvent([]byte(`data: {"choices":[{"delta":{"reasoning_content":"thinking"}}]}`))
	if !event.HasContent {
		t.Fatal("expected reasoning_content to count as generated text")
	}

	roleOnly := parseSSEEvent([]byte(`data: {"choices":[{"delta":{"role":"assistant"}}]}`))
	if roleOnly.HasContent {
		t.Fatal("role-only delta should not count as generated text")
	}
}

func TestChatCompletionsRejectsUnknownModel(t *testing.T) {
	store := router.NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", BaseURL: "http://127.0.0.1"},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing"}`))
	rr := httptest.NewRecorder()

	handler.ChatCompletions(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
}

func TestModelsListsConfiguredModels(t *testing.T) {
	store := router.NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"z-model": {RouteGroup: "group"},
			"a-model": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", BaseURL: "http://127.0.0.1"},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()

	handler.Models(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}

	var resp modelsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Object != "list" {
		t.Fatalf("object = %s", resp.Object)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("model count = %d", len(resp.Data))
	}
	if resp.Data[0].ID != "a-model" || resp.Data[1].ID != "z-model" {
		t.Fatalf("models not sorted: %+v", resp.Data)
	}
}

func TestModelsFiltersByAPIKey(t *testing.T) {
	store := router.NewStore(&config.Config{
		Auth: config.AuthConfig{
			Enabled: true,
			Keys: []config.ClientKeyConfig{
				{Name: "client", Key: "secret", AccessGroup: "group-a"},
			},
		},
		AccessGroups: map[string]config.AccessGroupConfig{
			"group-a": {AllowedModels: []string{"a-model"}},
		},
		Models: map[string]config.ModelConfig{
			"z-model": {RouteGroup: "group"},
			"a-model": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", BaseURL: "http://127.0.0.1"},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	handler.Models(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}

	var resp modelsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "a-model" {
		t.Fatalf("models = %+v", resp.Data)
	}
}

func TestModelsExcludesBlockedModels(t *testing.T) {
	store := router.NewStore(&config.Config{
		Auth: config.AuthConfig{
			Enabled: true,
			Keys: []config.ClientKeyConfig{
				{Name: "client", Key: "secret", AccessGroup: "group-a"},
			},
		},
		AccessGroups: map[string]config.AccessGroupConfig{
			"group-a": {BlockedModels: []string{"blocked-model"}},
		},
		Models: map[string]config.ModelConfig{
			"allowed-model": {RouteGroup: "group"},
			"blocked-model": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", BaseURL: "http://127.0.0.1"},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	handler.Models(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}

	var resp modelsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "allowed-model" {
		t.Fatalf("models = %+v", resp.Data)
	}
}

func TestModelsSupportsAccessPatterns(t *testing.T) {
	store := router.NewStore(&config.Config{
		Auth: config.AuthConfig{
			Enabled: true,
			Keys: []config.ClientKeyConfig{
				{Name: "client", Key: "secret", AccessGroup: "group-a"},
			},
		},
		AccessGroups: map[string]config.AccessGroupConfig{
			"group-a": {
				AllowedModels: []string{"qwen-*", "deepseek-r?"},
				BlockedModels: []string{"*-private", "qwen-bad"},
			},
		},
		Models: map[string]config.ModelConfig{
			"qwen-good":       {RouteGroup: "group"},
			"qwen-private":    {RouteGroup: "group"},
			"qwen-bad":        {RouteGroup: "group"},
			"deepseek-r1":     {RouteGroup: "group"},
			"deepseek-r10":    {RouteGroup: "group"},
			"unmatched-model": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "upstream", BaseURL: "http://127.0.0.1"},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder())

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rr := httptest.NewRecorder()

	handler.Models(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}

	var resp modelsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	got := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		got = append(got, model.ID)
	}
	want := []string{"deepseek-r1", "qwen-good"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("models = %v, want %v", got, want)
	}
}
