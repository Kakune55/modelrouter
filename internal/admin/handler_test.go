package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"modelrouter/internal/config"
	"modelrouter/internal/metrics"
	"modelrouter/internal/router"
)

func TestMetricsFiltersAndPaginates(t *testing.T) {
	store := router.NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"model-a": {RouteGroup: "group"},
			"model-b": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "endpoint", BaseURL: "http://127.0.0.1"},
				},
			},
		},
	})
	recorder := metrics.NewRecorder()
	recorder.Record(metrics.Event{
		Client:     "client-a",
		Model:      "model-a",
		RouteGroup: "group",
		Endpoint:   "endpoint",
		StatusCode: 200,
		Duration:   time.Millisecond,
	})
	recorder.Record(metrics.Event{
		Client:     "client-b",
		Model:      "model-b",
		RouteGroup: "group",
		Endpoint:   "endpoint",
		StatusCode: 200,
		Duration:   time.Millisecond,
	})
	handler := NewHandler(store, recorder, "")

	req := httptest.NewRequest(http.MethodGet, "/admin/metrics?client=client-a&limit=1&offset=0", nil)
	rr := httptest.NewRecorder()

	handler.Metrics(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Meta struct {
			Total    int `json:"total"`
			Returned int `json:"returned"`
		} `json:"meta"`
		Items []metrics.Counter `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Meta.Total != 1 || resp.Meta.Returned != 1 {
		t.Fatalf("meta = %+v", resp.Meta)
	}
	if len(resp.Items) != 1 || resp.Items[0].Client != "client-a" {
		t.Fatalf("items = %+v", resp.Items)
	}
}

func TestAdminRequiresTokenWhenConfigured(t *testing.T) {
	store := router.NewStore(&config.Config{
		Admin: config.AdminConfig{Token: "admin-token"},
		Models: map[string]config.ModelConfig{
			"model-a": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "endpoint", BaseURL: "http://127.0.0.1"},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder(), "")

	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/admin/config", nil)
	unauthorized := httptest.NewRecorder()
	handler.Config(unauthorized, unauthorizedReq)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d body = %s", unauthorized.Code, unauthorized.Body.String())
	}

	authorizedReq := httptest.NewRequest(http.MethodGet, "/admin/config", nil)
	authorizedReq.Header.Set("Authorization", "Bearer admin-token")
	authorized := httptest.NewRecorder()
	handler.Config(authorized, authorizedReq)
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body = %s", authorized.Code, authorized.Body.String())
	}
}

func TestAdminConfigRedactsSecrets(t *testing.T) {
	store := router.NewStore(&config.Config{
		Admin: config.AdminConfig{Token: "admin-token"},
		Auth: config.AuthConfig{
			Enabled: true,
			Keys: []config.ClientKeyConfig{
				{Name: "client", Key: "client-token", AccessGroup: "default"},
			},
		},
		AccessGroups: map[string]config.AccessGroupConfig{
			"default": {},
		},
		Models: map[string]config.ModelConfig{
			"model-a": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "endpoint", BaseURL: "http://127.0.0.1", APIKey: "upstream-token"},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder(), "")

	req := httptest.NewRequest(http.MethodGet, "/admin/config", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	rr := httptest.NewRecorder()
	handler.Config(rr, req)

	body := rr.Body.String()
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, body)
	}
	for _, secret := range []string{"admin-token", "client-token", "upstream-token"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked secret %q: %s", secret, body)
		}
	}
}
