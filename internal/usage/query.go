package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"modelrouter/internal/config"
)

type Query struct {
	Client     string
	Model      string
	RouteGroup string
	Endpoint   string
	FromUnix   int64
	ToUnix     int64
	Limit      int
	Offset     int
}

type QueryResult struct {
	Total int      `json:"total"`
	Items []Record `json:"items"`
}

type AggregateResult struct {
	Interval  string    `json:"interval"`
	Summary   Aggregate `json:"summary"`
	Series    []Bucket  `json:"series"`
	Breakdown Breakdown `json:"breakdown"`
}

type Aggregate struct {
	Requests                   int64   `json:"requests"`
	Successes                  int64   `json:"successes"`
	Failures                   int64   `json:"failures"`
	ErrorRate                  float64 `json:"error_rate"`
	BytesOut                   int64   `json:"bytes_out"`
	PromptTokens               int64   `json:"prompt_tokens"`
	OutputTokens               int64   `json:"output_tokens"`
	TotalTokens                int64   `json:"total_tokens"`
	AverageLatencyMS           float64 `json:"average_latency_ms"`
	AverageEndToEndTokenRate   float64 `json:"average_end_to_end_token_rate,omitempty"`
	AverageGenerationTokenRate float64 `json:"average_generation_token_rate,omitempty"`
	AverageTTFTMS              float64 `json:"average_ttft_ms,omitempty"`

	totalLatencyMS        float64
	totalEndToEndRate     float64
	endToEndRateSamples   int64
	totalGenerationRate   float64
	generationRateSamples int64
	totalTTFTMS           float64
	ttftSamples           int64
}

type Bucket struct {
	StartUnixSec int64     `json:"start_unix_sec"`
	EndUnixSec   int64     `json:"end_unix_sec"`
	Summary      Aggregate `json:"summary"`
}

type Breakdown struct {
	Clients   []BreakdownItem `json:"clients"`
	Models    []BreakdownItem `json:"models"`
	Endpoints []BreakdownItem `json:"endpoints"`
}

type BreakdownItem struct {
	Name    string    `json:"name"`
	Summary Aggregate `json:"summary"`
}

func QueryRecords(cfg config.UsageLogConfig, query Query) (QueryResult, error) {
	dir := usageDir(cfg)
	records, err := readRecords(dir, query)
	if err != nil {
		return QueryResult{}, err
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].UnixSec == records[j].UnixSec {
			return records[i].Time > records[j].Time
		}
		return records[i].UnixSec > records[j].UnixSec
	})

	total := len(records)
	if query.Limit == 0 || query.Offset >= total {
		return QueryResult{Total: total}, nil
	}
	end := min(query.Offset+query.Limit, total)
	return QueryResult{Total: total, Items: records[query.Offset:end]}, nil
}

func AggregateRecords(cfg config.UsageLogConfig, query Query, interval string, top int) (AggregateResult, error) {
	if top <= 0 {
		top = 10
	}
	if top > 100 {
		top = 100
	}
	bucketSeconds, normalizedInterval := intervalSeconds(interval)
	records, err := readRecords(usageDir(cfg), query)
	if err != nil {
		return AggregateResult{}, err
	}

	result := AggregateResult{
		Interval: normalizedInterval,
	}
	buckets := map[int64]*Bucket{}
	clients := map[string]*Aggregate{}
	models := map[string]*Aggregate{}
	endpoints := map[string]*Aggregate{}

	for _, record := range records {
		addRecord(&result.Summary, record)
		bucketStart := record.UnixSec - record.UnixSec%bucketSeconds
		bucket := buckets[bucketStart]
		if bucket == nil {
			bucket = &Bucket{
				StartUnixSec: bucketStart,
				EndUnixSec:   bucketStart + bucketSeconds,
			}
			buckets[bucketStart] = bucket
		}
		addRecord(&bucket.Summary, record)
		addRecord(aggregateFor(clients, record.Client), record)
		addRecord(aggregateFor(models, record.Model), record)
		addRecord(aggregateFor(endpoints, record.Endpoint), record)
	}

	finalizeAggregate(&result.Summary)
	result.Series = sortedBuckets(buckets)
	for i := range result.Series {
		finalizeAggregate(&result.Series[i].Summary)
	}
	result.Breakdown = Breakdown{
		Clients:   topBreakdown(clients, top),
		Models:    topBreakdown(models, top),
		Endpoints: topBreakdown(endpoints, top),
	}
	return result, nil
}

func readRecords(dir string, query Query) ([]Record, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !usageFileName(entry.Name()) {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))

	records := make([]Record, 0)
	for _, path := range paths {
		items, err := readRecordFile(path, query)
		if err != nil {
			return nil, err
		}
		records = append(records, items...)
	}
	return records, nil
}

func readRecordFile(path string, query Query) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() {
		_ = file.Close()
	}()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	records := make([]Record, 0)
	for scanner.Scan() {
		var record Record
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		if matchRecord(record, query) {
			records = append(records, record)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func matchRecord(record Record, query Query) bool {
	if query.Client != "" && record.Client != query.Client {
		return false
	}
	if query.Model != "" && record.Model != query.Model {
		return false
	}
	if query.RouteGroup != "" && record.RouteGroup != query.RouteGroup {
		return false
	}
	if query.Endpoint != "" && record.Endpoint != query.Endpoint {
		return false
	}
	if query.FromUnix > 0 && record.UnixSec < query.FromUnix {
		return false
	}
	if query.ToUnix > 0 && record.UnixSec > query.ToUnix {
		return false
	}
	return true
}

func usageFileName(name string) bool {
	return len(name) == len("usage-2006-01-02.jsonl") &&
		name[:len("usage-")] == "usage-" &&
		name[len(name)-len(".jsonl"):] == ".jsonl"
}

func addRecord(aggregate *Aggregate, record Record) {
	aggregate.Requests++
	if record.Success {
		aggregate.Successes++
	} else {
		aggregate.Failures++
	}
	aggregate.BytesOut += record.BytesOut
	aggregate.PromptTokens += record.PromptTokens
	aggregate.OutputTokens += record.OutputTokens
	aggregate.TotalTokens += record.TotalTokens
	aggregate.totalLatencyMS += record.DurationMS
	if record.EndToEndTokenRate > 0 {
		aggregate.totalEndToEndRate += record.EndToEndTokenRate
		aggregate.endToEndRateSamples++
	}
	if record.GenerationTokenRate > 0 {
		aggregate.totalGenerationRate += record.GenerationTokenRate
		aggregate.generationRateSamples++
	}
	if record.TTFTMS > 0 {
		aggregate.totalTTFTMS += record.TTFTMS
		aggregate.ttftSamples++
	}
}

func finalizeAggregate(aggregate *Aggregate) {
	if aggregate.Requests > 0 {
		aggregate.ErrorRate = float64(aggregate.Failures) / float64(aggregate.Requests)
		aggregate.AverageLatencyMS = aggregate.totalLatencyMS / float64(aggregate.Requests)
	}
	if aggregate.endToEndRateSamples > 0 {
		aggregate.AverageEndToEndTokenRate = aggregate.totalEndToEndRate / float64(aggregate.endToEndRateSamples)
	}
	if aggregate.generationRateSamples > 0 {
		aggregate.AverageGenerationTokenRate = aggregate.totalGenerationRate / float64(aggregate.generationRateSamples)
	}
	if aggregate.ttftSamples > 0 {
		aggregate.AverageTTFTMS = aggregate.totalTTFTMS / float64(aggregate.ttftSamples)
	}
}

func aggregateFor(items map[string]*Aggregate, name string) *Aggregate {
	if name == "" {
		name = "unknown"
	}
	item := items[name]
	if item == nil {
		item = &Aggregate{}
		items[name] = item
	}
	return item
}

func sortedBuckets(items map[int64]*Bucket) []Bucket {
	out := make([]Bucket, 0, len(items))
	for _, item := range items {
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartUnixSec < out[j].StartUnixSec
	})
	return out
}

func topBreakdown(items map[string]*Aggregate, limit int) []BreakdownItem {
	out := make([]BreakdownItem, 0, len(items))
	for name, summary := range items {
		item := BreakdownItem{Name: name, Summary: *summary}
		finalizeAggregate(&item.Summary)
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Summary.TotalTokens == out[j].Summary.TotalTokens {
			return out[i].Summary.Requests > out[j].Summary.Requests
		}
		return out[i].Summary.TotalTokens > out[j].Summary.TotalTokens
	})
	if len(out) > limit {
		return out[:limit]
	}
	return out
}

func intervalSeconds(interval string) (int64, string) {
	switch interval {
	case "minute":
		return int64(time.Minute / time.Second), "minute"
	case "day":
		return int64(24 * time.Hour / time.Second), "day"
	default:
		return int64(time.Hour / time.Second), "hour"
	}
}
