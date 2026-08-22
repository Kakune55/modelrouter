package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"modelrouter/internal/config"
	"modelrouter/internal/metrics"
)

func TestRecordWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger()

	err := logger.Record(config.UsageLogConfig{Enabled: true, Dir: dir}, metrics.Event{
		Client:             "client-a",
		Model:              "model-a",
		RouteGroup:         "group",
		Endpoint:           "endpoint",
		StatusCode:         200,
		Duration:           2 * time.Second,
		BytesOut:           128,
		PromptTokens:       10,
		OutputTokens:       20,
		TotalTokens:        30,
		Streaming:          true,
		TTFT:               100 * time.Millisecond,
		GenerationDuration: time.Second,
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	logger.Close()

	files, err := filepath.Glob(filepath.Join(dir, "usage-*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read usage log: %v", err)
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if record.Client != "client-a" || record.TotalTokens != 30 || !record.Success {
		t.Fatalf("record = %+v", record)
	}
	if record.EndToEndTokenRate != 10 || record.GenerationTokenRate != 20 {
		t.Fatalf("rates = %+v", record)
	}
}

func TestRecordDisabledDoesNotCreateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "disabled")
	logger := NewLogger()

	if err := logger.Record(config.UsageLogConfig{Enabled: false, Dir: dir}, metrics.Event{}); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	logger.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir should not exist, stat err = %v", err)
	}
}

func TestCleanupRemovesExpiredFiles(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "usage-2000-01-01.jsonl")
	if err := os.WriteFile(oldPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write old file: %v", err)
	}
	oldTime := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	logger := NewLogger()
	err := logger.Record(config.UsageLogConfig{Enabled: true, Dir: dir, RetentionHours: 1}, metrics.Event{
		Client: "client-a",
		Model:  "model-a",
	})
	if err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	logger.Close()
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old file should be removed, stat err = %v", err)
	}
}

func TestCloseFlushesQueuedRecords(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger()

	for range 3 {
		if err := logger.Record(config.UsageLogConfig{Enabled: true, Dir: dir}, metrics.Event{
			Client: "client-a",
			Model:  "model-a",
		}); err != nil {
			t.Fatalf("Record() error = %v", err)
		}
	}
	logger.Close()

	files, err := filepath.Glob(filepath.Join(dir, "usage-*.jsonl"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %v", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read usage log: %v", err)
	}
	lines := strings.Count(string(data), "\n")
	if lines != 3 {
		t.Fatalf("line count = %d body = %s", lines, string(data))
	}
}
