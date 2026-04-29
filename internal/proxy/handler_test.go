package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
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
