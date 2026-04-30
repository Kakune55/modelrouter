package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestAdminKeyPermissions(t *testing.T) {
	store := router.NewStore(&config.Config{
		Admin: config.AdminConfig{
			Keys: []config.AdminKeyConfig{
				{Name: "dashboard", Key: "read-token", Permissions: []string{config.AdminPermissionRead}},
				{Name: "config-writer", Key: "write-token", Permissions: []string{config.AdminPermissionConfigWrite}},
			},
		},
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

	readReq := httptest.NewRequest(http.MethodGet, "/admin/metrics", nil)
	readReq.Header.Set("Authorization", "Bearer read-token")
	readResp := httptest.NewRecorder()
	handler.Metrics(readResp, readReq)
	if readResp.Code != http.StatusOK {
		t.Fatalf("read status = %d body = %s", readResp.Code, readResp.Body.String())
	}

	writeBody := []byte(`{"models":{"model-a":{"route_group":"group"}},"route_groups":{"group":{"strategy":"round_robin","endpoints":[{"name":"endpoint","base_url":"http://127.0.0.1"}]}}}`)
	blockedReq := httptest.NewRequest(http.MethodPut, "/admin/config", bytes.NewReader(writeBody))
	blockedReq.Header.Set("Authorization", "Bearer read-token")
	blockedResp := httptest.NewRecorder()
	handler.Config(blockedResp, blockedReq)
	if blockedResp.Code != http.StatusForbidden {
		t.Fatalf("blocked status = %d body = %s", blockedResp.Code, blockedResp.Body.String())
	}

	writeReq := httptest.NewRequest(http.MethodPut, "/admin/config", bytes.NewReader(writeBody))
	writeReq.Header.Set("Authorization", "Bearer write-token")
	writeResp := httptest.NewRecorder()
	handler.Config(writeResp, writeReq)
	if writeResp.Code != http.StatusOK {
		t.Fatalf("write status = %d body = %s", writeResp.Code, writeResp.Body.String())
	}
}

func TestAdminConfigRedactsSecrets(t *testing.T) {
	store := router.NewStore(&config.Config{
		Admin: config.AdminConfig{
			Token: "admin-token",
			Keys: []config.AdminKeyConfig{
				{Name: "dashboard", Key: "dashboard-token", Permissions: []string{config.AdminPermissionRead}},
			},
		},
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
	for _, secret := range []string{"admin-token", "dashboard-token", "client-token", "upstream-token"} {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked secret %q: %s", secret, body)
		}
	}
}

func TestUsageReturnsHistory(t *testing.T) {
	dir := t.TempDir()
	body := []byte(`{"unix_sec":100,"time":"2026-04-29T00:00:01Z","client":"client-a","model":"model-a","route_group":"group","endpoint":"endpoint","status_code":200,"total_tokens":7,"success":true}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "usage-2026-04-29.jsonl"), body, 0o644); err != nil {
		t.Fatalf("write usage file: %v", err)
	}

	store := router.NewStore(&config.Config{
		UsageLog: config.UsageLogConfig{Enabled: true, Dir: dir},
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

	req := httptest.NewRequest(http.MethodGet, "/admin/usage?client=client-a&limit=10", nil)
	rr := httptest.NewRecorder()
	handler.Usage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
		Items []struct {
			Client     string `json:"client"`
			TotalToken int64  `json:"total_tokens"`
		} `json:"items"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Meta.Total != 1 || len(resp.Items) != 1 || resp.Items[0].Client != "client-a" || resp.Items[0].TotalToken != 7 {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestUsageSummaryReturnsAggregate(t *testing.T) {
	dir := t.TempDir()
	body := []byte(
		`{"unix_sec":100,"time":"2026-04-29T00:00:01Z","client":"client-a","model":"model-a","route_group":"group","endpoint":"endpoint","status_code":200,"total_tokens":7,"success":true}` + "\n" +
			`{"unix_sec":200,"time":"2026-04-29T00:00:02Z","client":"client-a","model":"model-a","route_group":"group","endpoint":"endpoint","status_code":500,"total_tokens":3,"success":false}` + "\n",
	)
	if err := os.WriteFile(filepath.Join(dir, "usage-2026-04-29.jsonl"), body, 0o644); err != nil {
		t.Fatalf("write usage file: %v", err)
	}

	store := router.NewStore(&config.Config{
		UsageLog: config.UsageLogConfig{Enabled: true, Dir: dir},
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

	req := httptest.NewRequest(http.MethodGet, "/admin/usage/summary?interval=minute&top=5", nil)
	rr := httptest.NewRecorder()
	handler.Usage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Result struct {
			Interval string `json:"interval"`
			Summary  struct {
				Requests    int64 `json:"requests"`
				TotalTokens int64 `json:"total_tokens"`
				Failures    int64 `json:"failures"`
			} `json:"summary"`
			Series []any `json:"series"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Result.Interval != "minute" || resp.Result.Summary.Requests != 2 || resp.Result.Summary.TotalTokens != 10 || resp.Result.Summary.Failures != 1 || len(resp.Result.Series) != 2 {
		t.Fatalf("resp = %+v", resp)
	}
}
