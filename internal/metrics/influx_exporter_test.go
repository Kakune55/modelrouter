package metrics

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"modelrouter/internal/config"
)

type capturedInfluxBatch struct {
	config config.InfluxDBConfig
	lines  []string
}

type fakeInfluxBatchWriter struct {
	mu       sync.Mutex
	results  []error
	batches  []capturedInfluxBatch
	called   chan struct{}
	block    chan struct{}
	attempts int
}

func (w *fakeInfluxBatchWriter) WriteBatch(ctx context.Context, cfg config.InfluxDBConfig, lines []string) error {
	w.mu.Lock()
	w.attempts++
	w.batches = append(w.batches, capturedInfluxBatch{config: cfg, lines: append([]string(nil), lines...)})
	var result error
	if len(w.results) > 0 {
		result = w.results[0]
		w.results = w.results[1:]
	}
	w.mu.Unlock()
	if w.called != nil {
		select {
		case w.called <- struct{}{}:
		default:
		}
	}
	if w.block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-w.block:
		}
	}
	return result
}

func (w *fakeInfluxBatchWriter) snapshot() (int, []capturedInfluxBatch) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.attempts, append([]capturedInfluxBatch(nil), w.batches...)
}

func TestInfluxDBExporterFlushesAtBatchSize(t *testing.T) {
	writer := &fakeInfluxBatchWriter{called: make(chan struct{}, 1)}
	exporter := newTestInfluxDBExporter(testInfluxConfig(2), writer)
	t.Cleanup(func() { closeInfluxExporter(t, exporter) })

	exporter.Record(Event{Client: "client-a", StatusCode: 200})
	exporter.Record(Event{Client: "client-b", StatusCode: 201})
	waitInfluxWriterCall(t, writer.called)
	waitInfluxStatus(t, exporter, func(status InfluxDBExporterStatus) bool {
		return status.WrittenPoints == 2
	})

	attempts, batches := writer.snapshot()
	if attempts != 1 || len(batches) != 1 || len(batches[0].lines) != 2 {
		t.Fatalf("attempts = %d batches = %+v", attempts, batches)
	}
}

func TestInfluxDBExporterSkipsWhenDisabled(t *testing.T) {
	writer := &fakeInfluxBatchWriter{}
	cfg := testInfluxConfig(1)
	cfg.Enabled = false
	exporter := newTestInfluxDBExporter(cfg, writer)
	defer closeInfluxExporter(t, exporter)

	exporter.Record(Event{StatusCode: 200})
	if status := exporter.Status(); status.Enabled || status.PendingPoints != 0 || status.DroppedPoints != 0 {
		t.Fatalf("status = %+v", status)
	}
	if attempts, _ := writer.snapshot(); attempts != 0 {
		t.Fatalf("attempts = %d", attempts)
	}
}

func TestInfluxDBExporterRecordsEncodingFailure(t *testing.T) {
	exporter := newTestInfluxDBExporter(testInfluxConfig(1), &fakeInfluxBatchWriter{})
	defer closeInfluxExporter(t, exporter)

	exporter.Record(Event{Client: "bad\nclient", StatusCode: 200})
	status := exporter.Status()
	if status.EncodingFailures != 1 || status.PendingPoints != 0 || status.LastError == "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestInfluxDBExporterFlushesOnInterval(t *testing.T) {
	writer := &fakeInfluxBatchWriter{called: make(chan struct{}, 1)}
	exporter := newTestInfluxDBExporter(testInfluxConfig(100), writer)
	t.Cleanup(func() { closeInfluxExporter(t, exporter) })

	exporter.Record(Event{StatusCode: 200})
	waitInfluxWriterCall(t, writer.called)
	waitInfluxStatus(t, exporter, func(status InfluxDBExporterStatus) bool {
		return status.WrittenPoints == 1
	})
}

func TestInfluxDBExporterRetriesTransientFailures(t *testing.T) {
	writer := &fakeInfluxBatchWriter{
		called: make(chan struct{}, 3),
		results: []error{
			&InfluxDBWriteError{StatusCode: 503},
			context.DeadlineExceeded,
			nil,
		},
	}
	exporter := newTestInfluxDBExporter(testInfluxConfig(1), writer)
	t.Cleanup(func() { closeInfluxExporter(t, exporter) })

	exporter.Record(Event{StatusCode: 200})
	waitInfluxStatus(t, exporter, func(status InfluxDBExporterStatus) bool {
		return status.WrittenPoints == 1
	})
	status := exporter.Status()
	if status.Retries != 2 || status.FailedBatches != 0 || status.DroppedPoints != 0 {
		t.Fatalf("status = %+v", status)
	}
	if attempts, _ := writer.snapshot(); attempts != 3 {
		t.Fatalf("attempts = %d", attempts)
	}
}

func TestInfluxDBExporterDoesNotRetryPermanentFailure(t *testing.T) {
	writer := &fakeInfluxBatchWriter{
		called:  make(chan struct{}, 1),
		results: []error{&InfluxDBWriteError{StatusCode: 400, Body: "invalid line"}},
	}
	exporter := newTestInfluxDBExporter(testInfluxConfig(1), writer)
	t.Cleanup(func() { closeInfluxExporter(t, exporter) })

	exporter.Record(Event{StatusCode: 200})
	waitInfluxStatus(t, exporter, func(status InfluxDBExporterStatus) bool {
		return status.FailedBatches == 1
	})
	status := exporter.Status()
	if status.Retries != 0 || status.DroppedPoints != 1 || status.PendingPoints != 0 {
		t.Fatalf("status = %+v", status)
	}
}

func TestInfluxDBExporterDropsWhenQueueIsFull(t *testing.T) {
	writer := &fakeInfluxBatchWriter{called: make(chan struct{}, 1), block: make(chan struct{})}
	cfg := testInfluxConfig(1)
	cfg.QueueSize = 2
	exporter := newTestInfluxDBExporter(cfg, writer)

	exporter.Record(Event{StatusCode: 200})
	waitInfluxWriterCall(t, writer.called)
	exporter.Record(Event{StatusCode: 201})
	exporter.Record(Event{StatusCode: 202})
	status := exporter.Status()
	if status.PendingPoints != 2 || status.DroppedPoints != 1 {
		t.Fatalf("status = %+v", status)
	}
	close(writer.block)
	closeInfluxExporter(t, exporter)
}

func TestInfluxDBExporterKeepsDestinationSnapshotsSeparate(t *testing.T) {
	writer := &fakeInfluxBatchWriter{called: make(chan struct{}, 2)}
	first := testInfluxConfig(100)
	first.Database = "first"
	exporter := newTestInfluxDBExporter(first, writer)
	t.Cleanup(func() { closeInfluxExporter(t, exporter) })

	exporter.Record(Event{StatusCode: 200})
	second := first
	second.Database = "second"
	exporter.Reconfigure(second)
	exporter.Record(Event{StatusCode: 201})

	waitInfluxStatus(t, exporter, func(status InfluxDBExporterStatus) bool {
		return status.WrittenPoints == 2
	})
	_, batches := writer.snapshot()
	if len(batches) != 2 || batches[0].config.Database != "first" || batches[1].config.Database != "second" {
		t.Fatalf("batches = %+v", batches)
	}
}

func TestInfluxDBExporterCloseFlushesPartialBatch(t *testing.T) {
	writer := &fakeInfluxBatchWriter{called: make(chan struct{}, 1)}
	exporter := newTestInfluxDBExporter(testInfluxConfig(100), writer)
	exporter.Record(Event{StatusCode: 200})

	closeInfluxExporter(t, exporter)
	status := exporter.Status()
	if status.WrittenPoints != 1 || status.PendingPoints != 0 {
		t.Fatalf("status = %+v", status)
	}
}

func TestInfluxDBExporterCloseCancelsBlockedWrite(t *testing.T) {
	writer := &fakeInfluxBatchWriter{called: make(chan struct{}, 1), block: make(chan struct{})}
	exporter := newTestInfluxDBExporter(testInfluxConfig(1), writer)
	exporter.Record(Event{StatusCode: 200})
	waitInfluxWriterCall(t, writer.called)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := exporter.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close() error = %v", err)
	}
	closeInfluxExporter(t, exporter)
	status := exporter.Status()
	if status.PendingPoints != 0 || status.DroppedPoints != 1 || status.FailedBatches != 1 {
		t.Fatalf("status = %+v", status)
	}
}

func newTestInfluxDBExporter(cfg config.InfluxDBConfig, writer InfluxBatchWriter) *InfluxDBExporter {
	return newInfluxDBExporter(cfg, writer, influxExporterOptions{
		flushInterval: func(config.InfluxDBConfig) time.Duration { return 10 * time.Millisecond },
		retryDelays:   []time.Duration{time.Millisecond, time.Millisecond},
	})
}

func testInfluxConfig(batchSize int) config.InfluxDBConfig {
	return config.InfluxDBConfig{
		Enabled:    true,
		APIVersion: 3,
		URL:        "https://influx.example.com",
		Database:   "modelrouter",
		Token:      "secret",
		BatchSize:  batchSize,
		QueueSize:  100,
	}
}

func waitInfluxWriterCall(t *testing.T, called <-chan struct{}) {
	t.Helper()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for InfluxDB writer")
	}
}

func waitInfluxStatus(t *testing.T, exporter *InfluxDBExporter, ready func(InfluxDBExporterStatus) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if ready(exporter.Status()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for exporter status: %+v", exporter.Status())
}

func closeInfluxExporter(t *testing.T, exporter *InfluxDBExporter) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := exporter.Close(ctx); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
