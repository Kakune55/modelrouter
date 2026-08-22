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

type staticExporterStatusProvider struct {
	status metrics.InfluxDBExporterStatus
}

func (p staticExporterStatusProvider) Status() metrics.InfluxDBExporterStatus {
	return p.status
}

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

func TestOverviewIncludesMetricsExporterStatus(t *testing.T) {
	store := router.NewStore(&config.Config{})
	handler := NewHandler(store, metrics.NewRecorder(), "").WithMetricsExporterStatusProvider(staticExporterStatusProvider{
		status: metrics.InfluxDBExporterStatus{
			Enabled:            true,
			PendingPoints:      2,
			WrittenPoints:      10,
			DroppedPoints:      1,
			LastSuccessUnixSec: 123,
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/admin/overview", nil)
	rr := httptest.NewRecorder()

	handler.Overview(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	var response struct {
		MetricsExporters struct {
			InfluxDB metrics.InfluxDBExporterStatus `json:"influxdb"`
		} `json:"metrics_exporters"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	status := response.MetricsExporters.InfluxDB
	if !status.Enabled || status.PendingPoints != 2 || status.WrittenPoints != 10 || status.DroppedPoints != 1 || status.LastSuccessUnixSec != 123 {
		t.Fatalf("exporter status = %+v", status)
	}
}

func TestPrometheusMetricsUsesAdminAuthAndTextFormat(t *testing.T) {
	store := router.NewStore(&config.Config{
		Admin: config.AdminConfig{Keys: []config.AdminKeyConfig{
			{Name: "prometheus", Key: "metrics-token", Permissions: []string{config.AdminPermissionMetricsRead}},
		}},
		Models: map[string]config.ModelConfig{
			`model-"a"`: {RouteGroup: "group"},
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
		Client:       `client\a`,
		Model:        `model-"a"`,
		RouteGroup:   "group",
		Endpoint:     "endpoint",
		StatusCode:   200,
		Duration:     time.Millisecond,
		BytesOut:     12,
		PromptTokens: 2,
		OutputTokens: 3,
		TotalTokens:  5,
	})
	handler := NewHandler(store, recorder, "")

	unauthorizedReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	unauthorized := httptest.NewRecorder()
	handler.Prometheus(unauthorized, unauthorizedReq)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d body = %s", unauthorized.Code, unauthorized.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer metrics-token")
	rr := httptest.NewRecorder()
	handler.Prometheus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "text/plain") {
		t.Fatalf("content-type = %q", got)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"# TYPE modelrouter_requests_total counter",
		"modelrouter_requests_total 1",
		`modelrouter_route_requests_total{client="client\\a",model="model-\"a\"",route_group="group",endpoint="endpoint"} 1`,
		`modelrouter_route_status_codes_total{client="client\\a",model="model-\"a\"",route_group="group",endpoint="endpoint",status_code="200"} 1`,
		`modelrouter_endpoint_inflight{route_group="group",endpoint="endpoint",model=""} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("prometheus body missing %q:\n%s", want, body)
		}
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
	configPath := filepath.Join(t.TempDir(), "config.json")
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
	var updatedConfig *config.Config
	handler := NewHandler(store, metrics.NewRecorder(), configPath).WithConfigUpdateHook(func(cfg *config.Config) {
		updatedConfig = cfg
	})

	readReq := httptest.NewRequest(http.MethodGet, "/admin/metrics", nil)
	readReq.Header.Set("Authorization", "Bearer read-token")
	readResp := httptest.NewRecorder()
	handler.Metrics(readResp, readReq)
	if readResp.Code != http.StatusOK {
		t.Fatalf("read status = %d body = %s", readResp.Code, readResp.Body.String())
	}

	writeBody := []byte(`{"metrics":{"influxdb":{"enabled":true,"api_version":3,"url":"http://localhost:8181","database":"updated","token":"secret"}},"models":{"model-a":{"route_group":"group"}},"route_groups":{"group":{"strategy":"round_robin","endpoints":[{"name":"endpoint","base_url":"http://127.0.0.1"}]}}}`)
	blockedReq := httptest.NewRequest(http.MethodPut, "/admin/config", bytes.NewReader(writeBody))
	blockedReq.Header.Set("Authorization", "Bearer read-token")
	blockedResp := httptest.NewRecorder()
	handler.Config(blockedResp, blockedReq)
	if blockedResp.Code != http.StatusForbidden {
		t.Fatalf("blocked status = %d body = %s", blockedResp.Code, blockedResp.Body.String())
	}
	if updatedConfig != nil {
		t.Fatal("config update hook called for blocked request")
	}

	writeReq := httptest.NewRequest(http.MethodPut, "/admin/config", bytes.NewReader(writeBody))
	writeReq.Header.Set("Authorization", "Bearer write-token")
	writeResp := httptest.NewRecorder()
	handler.Config(writeResp, writeReq)
	if writeResp.Code != http.StatusOK {
		t.Fatalf("write status = %d body = %s", writeResp.Code, writeResp.Body.String())
	}
	if updatedConfig == nil || updatedConfig.Metrics.InfluxDB.Database != "updated" {
		t.Fatalf("updated config = %+v", updatedConfig)
	}
	if _, err := config.LoadFile(configPath); err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
}

func TestAdminResourceConfigAPIs(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	store := router.NewStore(&config.Config{
		Admin: config.AdminConfig{
			Keys: []config.AdminKeyConfig{
				{Name: "reader", Key: "read-token", Permissions: []string{config.AdminPermissionConfigRead}},
				{Name: "writer", Key: "write-token", Permissions: []string{config.AdminPermissionConfigWrite}},
			},
		},
		Auth: config.AuthConfig{
			Enabled: true,
			Keys: []config.ClientKeyConfig{
				{Name: "client-a", Key: "client-token-a", AccessGroup: "default"},
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
					{Name: "endpoint", BaseURL: "http://127.0.0.1"},
				},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder(), configPath)

	putAdminResource(t, handler.Models, "/admin/models/model-b", `{"route_group":"group"}`)
	putAdminResource(t, handler.Models, "/admin/models/provider%2Fmodel-c", `{"route_group":"group"}`)
	getReq := httptest.NewRequest(http.MethodGet, "/admin/models/model-b", nil)
	getReq.Header.Set("Authorization", "Bearer read-token")
	getResp := httptest.NewRecorder()
	handler.Models(getResp, getReq)
	if getResp.Code != http.StatusOK {
		t.Fatalf("get model status = %d body = %s", getResp.Code, getResp.Body.String())
	}

	putAdminResource(t, handler.AccessGroups, "/admin/access-groups/premium", `{"allowed_models":["model-*"],"rate_limit":{"max_concurrency":2}}`)
	putAdminResource(t, handler.ClientKeys, "/admin/client-keys/client-b", `{"key":"client-token-b","access_group":"premium"}`)

	deleteReq := httptest.NewRequest(http.MethodDelete, "/admin/access-groups/premium", nil)
	deleteReq.Header.Set("Authorization", "Bearer write-token")
	deleteResp := httptest.NewRecorder()
	handler.AccessGroups(deleteResp, deleteReq)
	if deleteResp.Code != http.StatusBadRequest {
		t.Fatalf("delete referenced access group status = %d body = %s", deleteResp.Code, deleteResp.Body.String())
	}

	putAdminResource(t, handler.RouteGroups, "/admin/route-groups/group", `{"strategy":"round_robin","endpoints":[{"name":"endpoint","base_url":"http://127.0.0.1","api_key":"upstream-secret"}]}`)
	routeGroupReq := httptest.NewRequest(http.MethodGet, "/admin/route-groups/group", nil)
	routeGroupReq.Header.Set("Authorization", "Bearer read-token")
	routeGroupResp := httptest.NewRecorder()
	handler.RouteGroups(routeGroupResp, routeGroupReq)
	if routeGroupResp.Code != http.StatusOK {
		t.Fatalf("get route group status = %d body = %s", routeGroupResp.Code, routeGroupResp.Body.String())
	}
	if strings.Contains(routeGroupResp.Body.String(), "upstream-secret") {
		t.Fatalf("route group response leaked api key: %s", routeGroupResp.Body.String())
	}

	cfg, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if _, ok := cfg.Models["model-b"]; !ok {
		t.Fatalf("persisted config missing model-b: %+v", cfg.Models)
	}
	if _, ok := cfg.Models["provider/model-c"]; !ok {
		t.Fatalf("persisted config missing escaped model name: %+v", cfg.Models)
	}
	if !clientKeyExists(cfg.Auth.Keys, "client-b") {
		t.Fatalf("persisted config missing client-b: %+v", cfg.Auth.Keys)
	}
	if cfg.RouteGroups["group"].Endpoints[0].APIKey != "upstream-secret" {
		t.Fatalf("persisted route group lost api key: %+v", cfg.RouteGroups["group"])
	}
}

func putAdminResource(t *testing.T, handler http.HandlerFunc, path string, body string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer write-token")
	resp := httptest.NewRecorder()
	handler(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("put %s status = %d body = %s", path, resp.Code, resp.Body.String())
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
		Metrics: config.MetricsConfig{
			InfluxDB: config.InfluxDBConfig{
				Enabled: true, APIVersion: 3, URL: "http://localhost:8181",
				Database: "modelrouter", Token: "influxdb-token",
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
	for _, secret := range []string{"admin-token", "dashboard-token", "client-token", "upstream-token", "influxdb-token"} {
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
