package usage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"modelrouter/internal/config"
	"modelrouter/internal/metrics"
)

const (
	defaultDir            = "usage_logs"
	defaultRetentionHours = 24 * 30
	bufferSize            = 4096
	batchSize             = 100
	flushInterval         = time.Second
	cleanupInterval       = time.Hour
)

type Logger struct {
	ch          chan queuedRecord
	done        chan struct{}
	wg          sync.WaitGroup
	mu          sync.RWMutex
	closeOnce   sync.Once
	closed      bool
	lastCleanup map[string]time.Time
}

type queuedRecord struct {
	dir       string
	retention time.Duration
	at        time.Time
	record    Record
}

type Record struct {
	UnixSec             int64   `json:"unix_sec"`
	Time                string  `json:"time"`
	Client              string  `json:"client"`
	Model               string  `json:"model"`
	RouteGroup          string  `json:"route_group"`
	Endpoint            string  `json:"endpoint"`
	StatusCode          int     `json:"status_code"`
	DurationMS          float64 `json:"duration_ms"`
	BytesOut            int64   `json:"bytes_out"`
	PromptTokens        int64   `json:"prompt_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	Streaming           bool    `json:"streaming"`
	TTFTMS              float64 `json:"ttft_ms,omitempty"`
	EndToEndTokenRate   float64 `json:"end_to_end_token_rate,omitempty"`
	GenerationTokenRate float64 `json:"generation_token_rate,omitempty"`
	Success             bool    `json:"success"`
	Error               string  `json:"error,omitempty"`
}

func NewLogger() *Logger {
	logger := &Logger{
		ch:          make(chan queuedRecord, bufferSize),
		done:        make(chan struct{}),
		lastCleanup: map[string]time.Time{},
	}
	logger.wg.Add(1)
	go logger.run()
	return logger
}

func (l *Logger) Record(cfg config.UsageLogConfig, ev metrics.Event) error {
	if l == nil || !cfg.Enabled {
		return nil
	}
	now := time.Now()
	item := queuedRecord{
		dir:       usageDir(cfg),
		retention: retention(cfg),
		at:        now,
		record:    recordFromEvent(now, ev),
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		return nil
	}
	select {
	case l.ch <- item:
	default:
		// Prefer preserving proxy latency over blocking request completion on slow disks.
	}
	return nil
}

func (l *Logger) Close() {
	if l == nil {
		return
	}
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.mu.Unlock()
		close(l.done)
		l.wg.Wait()
	})
}

func (l *Logger) run() {
	defer l.wg.Done()
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	batch := make([]queuedRecord, 0, batchSize)
	for {
		select {
		case item := <-l.ch:
			batch = append(batch, item)
			if len(batch) >= batchSize {
				l.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				l.flush(batch)
				batch = batch[:0]
			}
		case <-l.done:
			for {
				select {
				case item := <-l.ch:
					batch = append(batch, item)
				default:
					if len(batch) > 0 {
						l.flush(batch)
					}
					return
				}
			}
		}
	}
}

func (l *Logger) flush(batch []queuedRecord) {
	groups := map[string][]queuedRecord{}
	for _, item := range batch {
		if err := l.prepareDir(item); err != nil {
			continue
		}
		path := filepath.Join(item.dir, fileName(item.at))
		groups[path] = append(groups[path], item)
	}
	for path, items := range groups {
		_ = writeRecords(path, items)
	}
}

func (l *Logger) prepareDir(item queuedRecord) error {
	if err := os.MkdirAll(item.dir, 0o755); err != nil {
		return err
	}
	last := l.lastCleanup[item.dir]
	if last.IsZero() || item.at.Sub(last) >= cleanupInterval {
		if err := cleanup(item.dir, item.retention, item.at); err != nil {
			return err
		}
		l.lastCleanup[item.dir] = item.at
	}
	return nil
}

func writeRecords(path string, items []queuedRecord) (err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, file.Close())
	}()
	encoder := json.NewEncoder(file)
	for _, item := range items {
		if err := encoder.Encode(item.record); err != nil {
			return err
		}
	}
	return nil
}

func usageDir(cfg config.UsageLogConfig) string {
	if strings.TrimSpace(cfg.Dir) == "" {
		return defaultDir
	}
	return cfg.Dir
}

func retention(cfg config.UsageLogConfig) time.Duration {
	if cfg.RetentionHours <= 0 {
		return time.Duration(defaultRetentionHours) * time.Hour
	}
	return time.Duration(cfg.RetentionHours) * time.Hour
}

func fileName(now time.Time) string {
	return "usage-" + now.Format("2006-01-02") + ".jsonl"
}

func cleanup(dir string, keep time.Duration, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := now.Add(-keep)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "usage-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func recordFromEvent(now time.Time, ev metrics.Event) Record {
	out := Record{
		UnixSec:             now.Unix(),
		Time:                now.Format(time.RFC3339Nano),
		Client:              ev.Client,
		Model:               ev.Model,
		RouteGroup:          ev.RouteGroup,
		Endpoint:            ev.Endpoint,
		StatusCode:          ev.StatusCode,
		DurationMS:          durationMS(ev.Duration),
		BytesOut:            ev.BytesOut,
		PromptTokens:        ev.PromptTokens,
		OutputTokens:        ev.OutputTokens,
		TotalTokens:         ev.TotalTokens,
		Streaming:           ev.Streaming,
		TTFTMS:              durationMS(ev.TTFT),
		EndToEndTokenRate:   tokenRate(ev.OutputTokens, ev.Duration),
		GenerationTokenRate: tokenRate(ev.OutputTokens, ev.GenerationDuration),
		Success:             ev.Err == nil && ev.StatusCode >= 200 && ev.StatusCode < 400,
	}
	if ev.Err != nil {
		out.Error = trimError(ev.Err.Error())
	}
	return out
}

func durationMS(value time.Duration) float64 {
	if value <= 0 {
		return 0
	}
	return float64(value.Microseconds()) / 1000
}

func tokenRate(tokens int64, duration time.Duration) float64 {
	if tokens <= 0 || duration <= 0 {
		return 0
	}
	return float64(tokens) / duration.Seconds()
}

func trimError(value string) string {
	const max = 512
	if len(value) <= max {
		return value
	}
	return value[:max]
}
