package metrics

import (
	"errors"
	"testing"
	"time"
)

func TestRecorderSnapshotAggregatesMetrics(t *testing.T) {
	recorder := NewRecorder()
	recorder.Record(Event{
		Client:             "client-a",
		Model:              "model-a",
		RouteGroup:         "group-a",
		Endpoint:           "endpoint-a",
		StatusCode:         200,
		Duration:           100 * time.Millisecond,
		BytesOut:           1000,
		PromptTokens:       10,
		OutputTokens:       20,
		TotalTokens:        30,
		Streaming:          true,
		TTFT:               50 * time.Millisecond,
		GenerationDuration: 80 * time.Millisecond,
	})
	recorder.Record(Event{
		Client:     "client-a",
		Model:      "model-a",
		RouteGroup: "group-a",
		Endpoint:   "endpoint-a",
		StatusCode: 500,
		Duration:   300 * time.Millisecond,
		Err:        errors.New("upstream failed"),
	})

	snapshot := recorder.Snapshot()
	if snapshot.Summary.Requests != 2 {
		t.Fatalf("requests = %d", snapshot.Summary.Requests)
	}
	if snapshot.Summary.Successes != 1 {
		t.Fatalf("successes = %d", snapshot.Summary.Successes)
	}
	if snapshot.Summary.Failures != 1 {
		t.Fatalf("failures = %d", snapshot.Summary.Failures)
	}
	if snapshot.Summary.ErrorRate != 0.5 {
		t.Fatalf("error rate = %f", snapshot.Summary.ErrorRate)
	}
	if len(snapshot.ByClient) != 1 || snapshot.ByClient[0].Client != "client-a" {
		t.Fatalf("by client = %+v", snapshot.ByClient)
	}
	if len(snapshot.ByModel) != 1 || snapshot.ByModel[0].Model != "model-a" {
		t.Fatalf("by model = %+v", snapshot.ByModel)
	}
	if snapshot.Windows["1m"].Requests != 2 {
		t.Fatalf("1m requests = %d", snapshot.Windows["1m"].Requests)
	}
	if snapshot.Windows["1m"].AverageEndToEndTokenRate != 200 {
		t.Fatalf("1m end-to-end token rate = %f", snapshot.Windows["1m"].AverageEndToEndTokenRate)
	}
	if snapshot.Windows["1m"].AverageGenerationTokenRate != 250 {
		t.Fatalf("1m generation token rate = %f", snapshot.Windows["1m"].AverageGenerationTokenRate)
	}
	if snapshot.Windows["1m"].AverageTTFTMS != 50 {
		t.Fatalf("1m average ttft = %f", snapshot.Windows["1m"].AverageTTFTMS)
	}
	if len(snapshot.Recent) != 2 {
		t.Fatalf("recent count = %d", len(snapshot.Recent))
	}
	if snapshot.Recent[0].EndToEndTokenRate != 200 {
		t.Fatalf("recent end-to-end token rate = %f", snapshot.Recent[0].EndToEndTokenRate)
	}
	if snapshot.Recent[0].GenerationTokenRate != 250 {
		t.Fatalf("recent generation token rate = %f", snapshot.Recent[0].GenerationTokenRate)
	}
	if snapshot.Recent[0].TTFTMS != 50 {
		t.Fatalf("recent ttft = %f", snapshot.Recent[0].TTFTMS)
	}
}

func TestRecorderAggregatesAcrossShards(t *testing.T) {
	recorder := NewRecorder()
	for range 100 {
		recorder.Record(Event{
			Client:     "client",
			Model:      "model",
			RouteGroup: "group",
			Endpoint:   "endpoint",
			StatusCode: 200,
			Duration:   time.Millisecond,
		})
	}

	snapshot := recorder.Snapshot()
	if snapshot.Summary.Requests != 100 {
		t.Fatalf("requests = %d", snapshot.Summary.Requests)
	}
	if len(snapshot.Items) != 1 {
		t.Fatalf("items = %d", len(snapshot.Items))
	}
	if snapshot.Items[0].Requests != 100 {
		t.Fatalf("item requests = %d", snapshot.Items[0].Requests)
	}
}
