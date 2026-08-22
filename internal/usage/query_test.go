package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"modelrouter/internal/config"
)

func TestQueryRecordsFiltersAndPaginates(t *testing.T) {
	dir := t.TempDir()
	writeTestRecords(t, dir, "usage-2026-04-29.jsonl", []Record{
		{UnixSec: 100, Time: "2026-04-29T00:00:01Z", Client: "client-a", Model: "model-a", RouteGroup: "group", Endpoint: "ep-1", TotalTokens: 10, Success: true},
		{UnixSec: 200, Time: "2026-04-29T00:00:02Z", Client: "client-b", Model: "model-b", RouteGroup: "group", Endpoint: "ep-2", TotalTokens: 20, Success: true},
		{UnixSec: 300, Time: "2026-04-29T00:00:03Z", Client: "client-a", Model: "model-a", RouteGroup: "group", Endpoint: "ep-1", TotalTokens: 30, Success: false},
	})

	result, err := QueryRecords(config.UsageLogConfig{Enabled: true, Dir: dir}, Query{
		Client:   "client-a",
		FromUnix: 100,
		ToUnix:   300,
		Limit:    1,
		Offset:   0,
	})
	if err != nil {
		t.Fatalf("QueryRecords() error = %v", err)
	}
	if result.Total != 2 || len(result.Items) != 1 {
		t.Fatalf("result = %+v", result)
	}
	if result.Items[0].UnixSec != 300 {
		t.Fatalf("items = %+v", result.Items)
	}
}

func TestQueryRecordsMissingDir(t *testing.T) {
	result, err := QueryRecords(config.UsageLogConfig{Dir: filepath.Join(t.TempDir(), "missing")}, Query{Limit: 100})
	if err != nil {
		t.Fatalf("QueryRecords() error = %v", err)
	}
	if result.Total != 0 || len(result.Items) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestAggregateRecords(t *testing.T) {
	dir := t.TempDir()
	writeTestRecords(t, dir, "usage-2026-04-29.jsonl", []Record{
		{UnixSec: 3600, Time: "2026-04-29T01:00:00Z", Client: "client-a", Model: "model-a", Endpoint: "ep-1", StatusCode: 200, DurationMS: 100, TotalTokens: 10, OutputTokens: 5, EndToEndTokenRate: 50, Success: true},
		{UnixSec: 3660, Time: "2026-04-29T01:01:00Z", Client: "client-a", Model: "model-a", Endpoint: "ep-1", StatusCode: 500, DurationMS: 300, TotalTokens: 20, OutputTokens: 10, EndToEndTokenRate: 100, Success: false},
		{UnixSec: 7200, Time: "2026-04-29T02:00:00Z", Client: "client-b", Model: "model-b", Endpoint: "ep-2", StatusCode: 200, DurationMS: 200, TotalTokens: 30, OutputTokens: 20, EndToEndTokenRate: 200, Success: true},
	})

	result, err := AggregateRecords(config.UsageLogConfig{Dir: dir}, Query{Limit: 100}, "hour", 1)
	if err != nil {
		t.Fatalf("AggregateRecords() error = %v", err)
	}
	if result.Interval != "hour" {
		t.Fatalf("interval = %s", result.Interval)
	}
	if result.Summary.Requests != 3 || result.Summary.TotalTokens != 60 || result.Summary.Failures != 1 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	if result.Summary.AverageLatencyMS != 200 {
		t.Fatalf("average latency = %f", result.Summary.AverageLatencyMS)
	}
	if len(result.Series) != 2 {
		t.Fatalf("series = %+v", result.Series)
	}
	if len(result.Breakdown.Models) != 1 || result.Breakdown.Models[0].Name != "model-a" {
		t.Fatalf("breakdown = %+v", result.Breakdown.Models)
	}
}

func writeTestRecords(t *testing.T, dir, name string, records []Record) {
	t.Helper()
	file, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("create records: %v", err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Errorf("close records: %v", err)
		}
	}()
	encoder := json.NewEncoder(file)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			t.Fatalf("encode record: %v", err)
		}
	}
}
