package admin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"modelrouter/internal/config"
	"modelrouter/internal/metrics"
	"modelrouter/internal/proxy"
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

func TestClientKeyPermissions(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	store := router.NewStore(&config.Config{
		Admin: config.AdminConfig{
			Keys: []config.AdminKeyConfig{
				{Name: "key-reader", Key: "key-read-token", Permissions: []string{config.AdminPermissionClientKeysRead}},
				{Name: "key-writer", Key: "key-write-token", Permissions: []string{config.AdminPermissionClientKeysWrite}},
			},
		},
		Auth: config.AuthConfig{
			Enabled: true,
			Keys: []config.ClientKeyConfig{
				{Name: "client-a", Key: "client-token-a", AccessGroup: "default"},
			},
		},
		AccessGroups: map[string]config.AccessGroupConfig{"default": {}},
		Models:       map[string]config.ModelConfig{"model-a": {RouteGroup: "group"}},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy:  config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{{Name: "endpoint", BaseURL: "http://127.0.0.1"}},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder(), configPath)

	readReq := httptest.NewRequest(http.MethodGet, "/admin/client-keys", nil)
	readReq.Header.Set("Authorization", "Bearer key-read-token")
	readResp := httptest.NewRecorder()
	handler.ClientKeys(readResp, readReq)
	if readResp.Code != http.StatusOK {
		t.Fatalf("read client keys status = %d body = %s", readResp.Code, readResp.Body.String())
	}

	writeReq := httptest.NewRequest(http.MethodPut, "/admin/client-keys/client-b", strings.NewReader(`{"key":"client-token-b","access_group":"default"}`))
	writeReq.Header.Set("Authorization", "Bearer key-write-token")
	writeResp := httptest.NewRecorder()
	handler.ClientKeys(writeResp, writeReq)
	if writeResp.Code != http.StatusOK {
		t.Fatalf("write client key status = %d body = %s", writeResp.Code, writeResp.Body.String())
	}

	blockedReadReq := httptest.NewRequest(http.MethodGet, "/admin/client-keys", nil)
	blockedReadReq.Header.Set("Authorization", "Bearer key-write-token")
	blockedReadResp := httptest.NewRecorder()
	handler.ClientKeys(blockedReadResp, blockedReadReq)
	if blockedReadResp.Code != http.StatusForbidden {
		t.Fatalf("write-only key read status = %d body = %s", blockedReadResp.Code, blockedReadResp.Body.String())
	}

	blockedConfigReq := httptest.NewRequest(http.MethodPut, "/admin/config", nil)
	blockedConfigReq.Header.Set("Authorization", "Bearer key-write-token")
	blockedConfigResp := httptest.NewRecorder()
	handler.Config(blockedConfigResp, blockedConfigReq)
	if blockedConfigResp.Code != http.StatusForbidden {
		t.Fatalf("key writer config status = %d body = %s", blockedConfigResp.Code, blockedConfigResp.Body.String())
	}
}

func TestIssueClientKey(t *testing.T) {
	handler, store, configPath := newClientKeyIssuanceHandler(t, true)
	req := httptest.NewRequest(http.MethodPost, "/admin/client-keys", strings.NewReader(`{"name":"client-b","access_group":"default"}`))
	req.Header.Set("Authorization", "Bearer key-write-token")
	resp := httptest.NewRecorder()
	handler.ClientKeys(resp, req)

	if resp.Code != http.StatusCreated {
		t.Fatalf("issue status = %d body = %s", resp.Code, resp.Body.String())
	}
	if got := resp.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var issued config.ClientKeyConfig
	if err := json.Unmarshal(resp.Body.Bytes(), &issued); err != nil {
		t.Fatalf("decode issued key: %v", err)
	}
	if issued.Name != "client-b" || issued.AccessGroup != "default" {
		t.Fatalf("issued key = %+v", issued)
	}
	if !strings.HasPrefix(issued.Key, "mr-") {
		t.Fatalf("issued key prefix = %q", issued.Key)
	}
	random, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(issued.Key, "mr-"))
	if err != nil || len(random) != 32 {
		t.Fatalf("issued key random part length = %d, error = %v", len(random), err)
	}

	loaded, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if !hasClientKey(loaded.Auth.Keys, issued) || !hasClientKey(store.Get().Config.Auth.Keys, issued) {
		t.Fatalf("issued key was not persisted and activated: %+v", issued)
	}

	proxyHandler := proxy.NewHandler(store, metrics.NewRecorder())
	defer proxyHandler.Close()
	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer "+issued.Key)
	modelsResp := httptest.NewRecorder()
	proxyHandler.Models(modelsResp, modelsReq)
	if modelsResp.Code != http.StatusOK {
		t.Fatalf("issued key authentication status = %d body = %s", modelsResp.Code, modelsResp.Body.String())
	}

	readReq := httptest.NewRequest(http.MethodGet, "/admin/client-keys/client-b", nil)
	readReq.Header.Set("Authorization", "Bearer key-read-token")
	readResp := httptest.NewRecorder()
	handler.ClientKeys(readResp, readReq)
	if strings.Contains(readResp.Body.String(), issued.Key) || !strings.Contains(readResp.Body.String(), "********") {
		t.Fatalf("client key response was not redacted: %s", readResp.Body.String())
	}
}

func TestIssueClientKeyValidation(t *testing.T) {
	handler, _, _ := newClientKeyIssuanceHandler(t, true)
	tests := []struct {
		name   string
		path   string
		token  string
		body   string
		status int
	}{
		{name: "名称为空", path: "/admin/client-keys", token: "key-write-token", body: `{"access_group":"default"}`, status: http.StatusBadRequest},
		{name: "访问组为空", path: "/admin/client-keys", token: "key-write-token", body: `{"name":"client-b"}`, status: http.StatusBadRequest},
		{name: "访问组不存在", path: "/admin/client-keys", token: "key-write-token", body: `{"name":"client-b","access_group":"missing"}`, status: http.StatusBadRequest},
		{name: "名称已存在", path: "/admin/client-keys", token: "key-write-token", body: `{"name":"client-a","access_group":"default"}`, status: http.StatusConflict},
		{name: "单项路径不支持签发", path: "/admin/client-keys/client-b", token: "key-write-token", body: `{"name":"client-b","access_group":"default"}`, status: http.StatusMethodNotAllowed},
		{name: "读取权限不能签发", path: "/admin/client-keys", token: "key-read-token", body: `{"name":"client-b","access_group":"default"}`, status: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Authorization", "Bearer "+tt.token)
			resp := httptest.NewRecorder()
			handler.ClientKeys(resp, req)
			if resp.Code != tt.status {
				t.Fatalf("status = %d, want %d, body = %s", resp.Code, tt.status, resp.Body.String())
			}
		})
	}
}

func TestIssueClientKeyRejectsDisabledAuth(t *testing.T) {
	handler, _, _ := newClientKeyIssuanceHandler(t, false)
	req := httptest.NewRequest(http.MethodPost, "/admin/client-keys", strings.NewReader(`{"name":"client-b","access_group":"default"}`))
	req.Header.Set("Authorization", "Bearer key-write-token")
	resp := httptest.NewRecorder()
	handler.ClientKeys(resp, req)
	if resp.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d, body = %s", resp.Code, http.StatusConflict, resp.Body.String())
	}
}

func TestIssueClientKeyRetriesCredentialCollisions(t *testing.T) {
	handler, _, _ := newClientKeyIssuanceHandler(t, true)
	generated := []string{"admin-master-token", "client-token-a", "mr-" + strings.Repeat("a", 43)}
	attempt := 0
	handler.clientKeyGenerator = func() (string, error) {
		key := generated[attempt]
		attempt++
		return key, nil
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/client-keys", strings.NewReader(`{"name":"client-b","access_group":"default"}`))
	req.Header.Set("Authorization", "Bearer key-write-token")
	resp := httptest.NewRecorder()
	handler.ClientKeys(resp, req)
	if resp.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", resp.Code, resp.Body.String())
	}
	if attempt != 3 || !strings.Contains(resp.Body.String(), generated[2]) {
		t.Fatalf("generation attempts = %d body = %s", attempt, resp.Body.String())
	}
}

func TestConcurrentIssueClientKeyRejectsDuplicateName(t *testing.T) {
	handler, store, _ := newClientKeyIssuanceHandler(t, true)
	start := make(chan struct{})
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			<-start
			req := httptest.NewRequest(http.MethodPost, "/admin/client-keys", strings.NewReader(`{"name":"client-b","access_group":"default"}`))
			req.Header.Set("Authorization", "Bearer key-write-token")
			resp := httptest.NewRecorder()
			handler.ClientKeys(resp, req)
			statuses <- resp.Code
		}()
	}
	close(start)

	created, conflicts := 0, 0
	for range 2 {
		switch <-statuses {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		}
	}
	if created != 1 || conflicts != 1 {
		t.Fatalf("created = %d, conflicts = %d", created, conflicts)
	}
	if got := len(store.Get().Config.Auth.Keys); got != 2 {
		t.Fatalf("active client key count = %d, want 2", got)
	}
}

func newClientKeyIssuanceHandler(t *testing.T, authEnabled bool) (*Handler, *router.Store, string) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.json")
	store := router.NewStore(&config.Config{
		Admin: config.AdminConfig{
			Token: "admin-master-token",
			Keys: []config.AdminKeyConfig{
				{Name: "key-reader", Key: "key-read-token", Permissions: []string{config.AdminPermissionClientKeysRead}},
				{Name: "key-writer", Key: "key-write-token", Permissions: []string{config.AdminPermissionClientKeysWrite}},
			},
		},
		Auth: config.AuthConfig{
			Enabled: authEnabled,
			Keys: []config.ClientKeyConfig{
				{Name: "client-a", Key: "client-token-a", AccessGroup: "default"},
			},
		},
		AccessGroups: map[string]config.AccessGroupConfig{"default": {}},
		Models:       map[string]config.ModelConfig{"model-a": {RouteGroup: "group"}},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy:  config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{{Name: "endpoint", BaseURL: "http://127.0.0.1"}},
			},
		},
	})
	return NewHandler(store, metrics.NewRecorder(), configPath), store, configPath
}

func hasClientKey(keys []config.ClientKeyConfig, expected config.ClientKeyConfig) bool {
	return slices.Contains(keys, expected)
}

func TestClientKeyPermissionHierarchy(t *testing.T) {
	tests := []struct {
		name        string
		permissions []string
		required    string
		allowed     bool
	}{
		{name: "精确读取权限", permissions: []string{config.AdminPermissionClientKeysRead}, required: config.AdminPermissionClientKeysRead, allowed: true},
		{name: "读取权限不能写入", permissions: []string{config.AdminPermissionClientKeysRead}, required: config.AdminPermissionClientKeysWrite},
		{name: "精确写入权限", permissions: []string{config.AdminPermissionClientKeysWrite}, required: config.AdminPermissionClientKeysWrite, allowed: true},
		{name: "写入权限不能读取", permissions: []string{config.AdminPermissionClientKeysWrite}, required: config.AdminPermissionClientKeysRead},
		{name: "配置读取保持兼容", permissions: []string{config.AdminPermissionConfigRead}, required: config.AdminPermissionClientKeysRead, allowed: true},
		{name: "配置写入保持兼容", permissions: []string{config.AdminPermissionConfigWrite}, required: config.AdminPermissionClientKeysWrite, allowed: true},
		{name: "管理读取继承", permissions: []string{config.AdminPermissionRead}, required: config.AdminPermissionClientKeysRead, allowed: true},
		{name: "管理写入继承", permissions: []string{config.AdminPermissionWrite}, required: config.AdminPermissionClientKeysWrite, allowed: true},
		{name: "管理全部权限", permissions: []string{config.AdminPermissionAll}, required: config.AdminPermissionClientKeysWrite, allowed: true},
		{name: "Key 写入", permissions: []string{config.AdminPermissionClientKeysWrite}, required: config.AdminPermissionConfigWrite},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := config.AdminKeyConfig{Permissions: tt.permissions}
			if got := adminKeyAllows(key, tt.required); got != tt.allowed {
				t.Fatalf("adminKeyAllows() = %v, want %v", got, tt.allowed)
			}
		})
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

func TestConcurrentConfigUpdatesPreserveChanges(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	store := router.NewStore(&config.Config{
		Models: map[string]config.ModelConfig{"model-a": {RouteGroup: "group"}},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy:  config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{{Name: "endpoint", BaseURL: "http://127.0.0.1"}},
			},
		},
	})
	handler := NewHandler(store, metrics.NewRecorder(), configPath)

	const updates = 8
	start := make(chan struct{})
	errs := make(chan error, updates)
	for i := range updates {
		name := "model-" + strconv.Itoa(i)
		go func() {
			<-start
			errs <- handler.updateConfig(func(cfg *config.Config) {
				// 放大旧实现中读取、修改和写回之间的竞态窗口。
				time.Sleep(5 * time.Millisecond)
				cfg.Models[name] = config.ModelConfig{RouteGroup: "group"}
			})
		}()
	}
	close(start)

	for range updates {
		if err := <-errs; err != nil {
			t.Fatalf("updateConfig() error = %v", err)
		}
	}

	loaded, err := config.LoadFile(configPath)
	if err != nil {
		t.Fatalf("load persisted config: %v", err)
	}
	if got, want := len(loaded.Models), updates+1; got != want {
		t.Fatalf("persisted model count = %d, want %d", got, want)
	}
	if got, want := len(store.Get().Config.Models), updates+1; got != want {
		t.Fatalf("active model count = %d, want %d", got, want)
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
