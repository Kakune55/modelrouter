package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
