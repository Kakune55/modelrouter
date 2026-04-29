package metrics

import (
	"hash/fnv"
	"sort"
	"sync"
	"time"
)

const recentLimit = 5000
const shardCount = 32
const snapshotTTL = time.Second

type Recorder struct {
	shards [shardCount]recorderShard

	recentMu sync.RWMutex
	recent   []EventRecord
	next     int
	wrapped  bool

	cacheMu  sync.RWMutex
	cached   Snapshot
	cachedAt time.Time
}

type recorderShard struct {
	mu     sync.RWMutex
	series map[string]*Counter
}

type Counter struct {
	Client                     string        `json:"client"`
	Model                      string        `json:"model"`
	RouteGroup                 string        `json:"route_group"`
	Endpoint                   string        `json:"endpoint"`
	Requests                   int64         `json:"requests"`
	Successes                  int64         `json:"successes"`
	Failures                   int64         `json:"failures"`
	BytesOut                   int64         `json:"bytes_out"`
	PromptTokens               int64         `json:"prompt_tokens"`
	OutputTokens               int64         `json:"output_tokens"`
	TotalTokens                int64         `json:"total_tokens"`
	TotalLatency               time.Duration `json:"-"`
	AverageLatencyMS           float64       `json:"average_latency_ms"`
	AverageEndToEndTokenRate   float64       `json:"average_end_to_end_token_rate"`
	AverageGenerationTokenRate float64       `json:"average_generation_token_rate,omitempty"`
	AverageTTFTMS              float64       `json:"average_ttft_ms,omitempty"`
	ErrorRate                  float64       `json:"error_rate"`
	LastRequestUnixSec         int64         `json:"last_request_unix_sec"`
	StatusCodes                map[int]int64 `json:"status_codes,omitempty"`
	TokenRateSamples           int64         `json:"-"`
	GenerationRateSamples      int64         `json:"-"`
	TTFTSamples                int64         `json:"-"`
	TotalEndToEndTokenRate     float64       `json:"-"`
	TotalGenerationTokenRate   float64       `json:"-"`
	TotalTTFTMS                float64       `json:"-"`
}

type Event struct {
	Client             string
	Model              string
	RouteGroup         string
	Endpoint           string
	StatusCode         int
	Duration           time.Duration
	BytesOut           int64
	PromptTokens       int64
	OutputTokens       int64
	TotalTokens        int64
	Streaming          bool
	TTFT               time.Duration
	GenerationDuration time.Duration
	Err                error
}

type EventRecord struct {
	UnixSec             int64   `json:"unix_sec"`
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
	EndToEndTokenRate   float64 `json:"end_to_end_token_rate"`
	GenerationTokenRate float64 `json:"generation_token_rate,omitempty"`
	Success             bool    `json:"success"`
	Error               string  `json:"error,omitempty"`
}

type Snapshot struct {
	GeneratedAtUnixSec int64                    `json:"generated_at_unix_sec"`
	Summary            Summary                  `json:"summary"`
	Windows            map[string]WindowSummary `json:"windows"`
	Items              []Counter                `json:"items"`
	ByClient           []Counter                `json:"by_client"`
	ByModel            []Counter                `json:"by_model"`
	ByEndpoint         []Counter                `json:"by_endpoint"`
	Recent             []EventRecord            `json:"recent,omitempty"`
}

type Summary struct {
	Requests                   int64   `json:"requests"`
	Successes                  int64   `json:"successes"`
	Failures                   int64   `json:"failures"`
	ErrorRate                  float64 `json:"error_rate"`
	BytesOut                   int64   `json:"bytes_out"`
	PromptTokens               int64   `json:"prompt_tokens"`
	OutputTokens               int64   `json:"output_tokens"`
	TotalTokens                int64   `json:"total_tokens"`
	AverageLatencyMS           float64 `json:"average_latency_ms"`
	AverageEndToEndTokenRate   float64 `json:"average_end_to_end_token_rate"`
	AverageGenerationTokenRate float64 `json:"average_generation_token_rate,omitempty"`
	AverageTTFTMS              float64 `json:"average_ttft_ms,omitempty"`
}

type WindowSummary struct {
	Seconds                    int64   `json:"seconds"`
	Requests                   int64   `json:"requests"`
	Successes                  int64   `json:"successes"`
	Failures                   int64   `json:"failures"`
	ErrorRate                  float64 `json:"error_rate"`
	RequestsPerMin             float64 `json:"requests_per_min"`
	AverageEndToEndTokenRate   float64 `json:"average_end_to_end_token_rate"`
	AverageGenerationTokenRate float64 `json:"average_generation_token_rate,omitempty"`
	AverageTTFTMS              float64 `json:"average_ttft_ms,omitempty"`
	BytesPerSec                float64 `json:"bytes_per_sec"`
	AverageLatencyMS           float64 `json:"average_latency_ms"`
	P95LatencyMS               float64 `json:"p95_latency_ms"`
}

func NewRecorder() *Recorder {
	recorder := &Recorder{
		recent: make([]EventRecord, 0, recentLimit),
	}
	for i := range recorder.shards {
		recorder.shards[i].series = map[string]*Counter{}
	}
	return recorder
}

func (r *Recorder) Record(ev Event) {
	now := time.Now()
	success := ev.Err == nil && ev.StatusCode >= 200 && ev.StatusCode < 400
	key := ev.Client + "\x00" + ev.Model + "\x00" + ev.RouteGroup + "\x00" + ev.Endpoint
	shard := r.shardFor(key)
	shard.mu.Lock()
	counter := shard.series[key]
	if counter == nil {
		counter = &Counter{
			Client:      ev.Client,
			Model:       ev.Model,
			RouteGroup:  ev.RouteGroup,
			Endpoint:    ev.Endpoint,
			StatusCodes: map[int]int64{},
		}
		shard.series[key] = counter
	}
	counter.Requests++
	if success {
		counter.Successes++
	} else {
		counter.Failures++
	}
	counter.StatusCodes[ev.StatusCode]++
	counter.BytesOut += ev.BytesOut
	counter.PromptTokens += ev.PromptTokens
	counter.OutputTokens += ev.OutputTokens
	counter.TotalTokens += ev.TotalTokens
	counter.TotalLatency += ev.Duration
	if rate := tokenRate(ev.OutputTokens, ev.Duration); rate > 0 {
		counter.TotalEndToEndTokenRate += rate
		counter.TokenRateSamples++
	}
	if rate := tokenRate(ev.OutputTokens, ev.GenerationDuration); rate > 0 {
		counter.TotalGenerationTokenRate += rate
		counter.GenerationRateSamples++
	}
	if ev.TTFT > 0 {
		counter.TotalTTFTMS += float64(ev.TTFT.Microseconds()) / 1000
		counter.TTFTSamples++
	}
	counter.LastRequestUnixSec = now.Unix()
	updateDerived(counter)
	shard.mu.Unlock()

	record := EventRecord{
		UnixSec:             now.Unix(),
		Client:              ev.Client,
		Model:               ev.Model,
		RouteGroup:          ev.RouteGroup,
		Endpoint:            ev.Endpoint,
		StatusCode:          ev.StatusCode,
		DurationMS:          float64(ev.Duration.Microseconds()) / 1000,
		BytesOut:            ev.BytesOut,
		PromptTokens:        ev.PromptTokens,
		OutputTokens:        ev.OutputTokens,
		TotalTokens:         ev.TotalTokens,
		Streaming:           ev.Streaming,
		TTFTMS:              durationMS(ev.TTFT),
		EndToEndTokenRate:   tokenRate(ev.OutputTokens, ev.Duration),
		GenerationTokenRate: tokenRate(ev.OutputTokens, ev.GenerationDuration),
		Success:             success,
	}
	if ev.Err != nil {
		record.Error = ev.Err.Error()
	}
	r.recordRecent(record)
}

func (r *Recorder) Snapshot() Snapshot {
	r.cacheMu.RLock()
	if !r.cachedAt.IsZero() && time.Since(r.cachedAt) < snapshotTTL {
		snapshot := cloneSnapshot(r.cached)
		r.cacheMu.RUnlock()
		return snapshot
	}
	r.cacheMu.RUnlock()

	items := make([]Counter, 0)
	for i := range r.shards {
		shard := &r.shards[i]
		shard.mu.RLock()
		for _, counter := range shard.series {
			items = append(items, cloneCounter(counter))
		}
		shard.mu.RUnlock()
	}
	sortCounters(items)

	recent := r.recentOrdered()
	snapshot := Snapshot{
		GeneratedAtUnixSec: time.Now().Unix(),
		Summary:            summarize(items),
		Windows: map[string]WindowSummary{
			"1m": summarizeWindow(recent, 60),
			"5m": summarizeWindow(recent, 300),
		},
		Items:      items,
		ByClient:   aggregate(items, "client"),
		ByModel:    aggregate(items, "model"),
		ByEndpoint: aggregate(items, "endpoint"),
		Recent:     recent,
	}
	r.cacheMu.Lock()
	r.cached = cloneSnapshot(snapshot)
	r.cachedAt = time.Now()
	r.cacheMu.Unlock()
	return snapshot
}

func updateDerived(c *Counter) {
	if c.Requests > 0 {
		c.AverageLatencyMS = float64(c.TotalLatency.Microseconds()) / 1000 / float64(c.Requests)
		c.ErrorRate = float64(c.Failures) / float64(c.Requests)
	}
	seconds := c.TotalLatency.Seconds()
	if seconds > 0 {
		_ = seconds
	}
	if c.TokenRateSamples > 0 {
		c.AverageEndToEndTokenRate = c.TotalEndToEndTokenRate / float64(c.TokenRateSamples)
	}
	if c.GenerationRateSamples > 0 {
		c.AverageGenerationTokenRate = c.TotalGenerationTokenRate / float64(c.GenerationRateSamples)
	}
	if c.TTFTSamples > 0 {
		c.AverageTTFTMS = c.TotalTTFTMS / float64(c.TTFTSamples)
	}
}

func (r *Recorder) recordRecent(record EventRecord) {
	r.recentMu.Lock()
	defer r.recentMu.Unlock()
	if len(r.recent) < recentLimit {
		r.recent = append(r.recent, record)
		return
	}
	r.recent[r.next] = record
	r.next = (r.next + 1) % recentLimit
	r.wrapped = true
}

func (r *Recorder) recentOrdered() []EventRecord {
	r.recentMu.RLock()
	defer r.recentMu.RUnlock()
	if !r.wrapped {
		out := make([]EventRecord, len(r.recent))
		copy(out, r.recent)
		return out
	}
	out := make([]EventRecord, 0, len(r.recent))
	out = append(out, r.recent[r.next:]...)
	out = append(out, r.recent[:r.next]...)
	return out
}

func (r *Recorder) shardFor(key string) *recorderShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return &r.shards[int(h.Sum32())%len(r.shards)]
}

func cloneCounter(counter *Counter) Counter {
	copy := *counter
	copy.StatusCodes = make(map[int]int64, len(counter.StatusCodes))
	for code, count := range counter.StatusCodes {
		copy.StatusCodes[code] = count
	}
	updateDerived(&copy)
	return copy
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := snapshot
	clone.Windows = make(map[string]WindowSummary, len(snapshot.Windows))
	for key, value := range snapshot.Windows {
		clone.Windows[key] = value
	}
	clone.Items = cloneCounters(snapshot.Items)
	clone.ByClient = cloneCounters(snapshot.ByClient)
	clone.ByModel = cloneCounters(snapshot.ByModel)
	clone.ByEndpoint = cloneCounters(snapshot.ByEndpoint)
	clone.Recent = append([]EventRecord(nil), snapshot.Recent...)
	return clone
}

func cloneCounters(items []Counter) []Counter {
	out := make([]Counter, len(items))
	for i := range items {
		out[i] = cloneCounter(&items[i])
	}
	return out
}

func aggregate(items []Counter, dimension string) []Counter {
	index := map[string]*Counter{}
	for _, item := range items {
		key := aggregateKey(item, dimension)
		counter := index[key]
		if counter == nil {
			counter = &Counter{StatusCodes: map[int]int64{}}
			switch dimension {
			case "client":
				counter.Client = item.Client
			case "model":
				counter.Model = item.Model
			case "endpoint":
				counter.RouteGroup = item.RouteGroup
				counter.Endpoint = item.Endpoint
			}
			index[key] = counter
		}
		addCounter(counter, item)
	}
	out := make([]Counter, 0, len(index))
	for _, counter := range index {
		updateDerived(counter)
		out = append(out, *counter)
	}
	sortCounters(out)
	return out
}

func aggregateKey(item Counter, dimension string) string {
	switch dimension {
	case "client":
		return item.Client
	case "model":
		return item.Model
	case "endpoint":
		return item.RouteGroup + "\x00" + item.Endpoint
	default:
		return item.Client + "\x00" + item.Model + "\x00" + item.RouteGroup + "\x00" + item.Endpoint
	}
}

func addCounter(dst *Counter, src Counter) {
	dst.Requests += src.Requests
	dst.Successes += src.Successes
	dst.Failures += src.Failures
	dst.BytesOut += src.BytesOut
	dst.PromptTokens += src.PromptTokens
	dst.OutputTokens += src.OutputTokens
	dst.TotalTokens += src.TotalTokens
	dst.TotalLatency += src.TotalLatency
	dst.TotalEndToEndTokenRate += src.TotalEndToEndTokenRate
	dst.TotalGenerationTokenRate += src.TotalGenerationTokenRate
	dst.TotalTTFTMS += src.TotalTTFTMS
	dst.TokenRateSamples += src.TokenRateSamples
	dst.GenerationRateSamples += src.GenerationRateSamples
	dst.TTFTSamples += src.TTFTSamples
	if src.LastRequestUnixSec > dst.LastRequestUnixSec {
		dst.LastRequestUnixSec = src.LastRequestUnixSec
	}
	for code, count := range src.StatusCodes {
		dst.StatusCodes[code] += count
	}
}

func summarize(items []Counter) Summary {
	var total Counter
	total.StatusCodes = map[int]int64{}
	for _, item := range items {
		addCounter(&total, item)
	}
	updateDerived(&total)
	return Summary{
		Requests:                   total.Requests,
		Successes:                  total.Successes,
		Failures:                   total.Failures,
		ErrorRate:                  total.ErrorRate,
		BytesOut:                   total.BytesOut,
		PromptTokens:               total.PromptTokens,
		OutputTokens:               total.OutputTokens,
		TotalTokens:                total.TotalTokens,
		AverageLatencyMS:           total.AverageLatencyMS,
		AverageEndToEndTokenRate:   total.AverageEndToEndTokenRate,
		AverageGenerationTokenRate: total.AverageGenerationTokenRate,
		AverageTTFTMS:              total.AverageTTFTMS,
	}
}

func summarizeWindow(events []EventRecord, seconds int64) WindowSummary {
	cutoff := time.Now().Unix() - seconds
	var summary WindowSummary
	summary.Seconds = seconds
	var latency []float64
	var endToEndSamples int64
	var generationSamples int64
	var ttftSamples int64
	for _, ev := range events {
		if ev.UnixSec < cutoff {
			continue
		}
		summary.Requests++
		if ev.Success {
			summary.Successes++
		} else {
			summary.Failures++
		}
		if ev.EndToEndTokenRate > 0 {
			summary.AverageEndToEndTokenRate += ev.EndToEndTokenRate
			endToEndSamples++
		}
		if ev.GenerationTokenRate > 0 {
			summary.AverageGenerationTokenRate += ev.GenerationTokenRate
			generationSamples++
		}
		if ev.TTFTMS > 0 {
			summary.AverageTTFTMS += ev.TTFTMS
			ttftSamples++
		}
		summary.BytesPerSec += float64(ev.BytesOut)
		summary.AverageLatencyMS += ev.DurationMS
		latency = append(latency, ev.DurationMS)
	}
	if summary.Requests > 0 {
		summary.ErrorRate = float64(summary.Failures) / float64(summary.Requests)
		summary.RequestsPerMin = float64(summary.Requests) * 60 / float64(seconds)
		summary.BytesPerSec /= float64(seconds)
		summary.AverageLatencyMS /= float64(summary.Requests)
		summary.P95LatencyMS = percentile(latency, 0.95)
	}
	if endToEndSamples > 0 {
		summary.AverageEndToEndTokenRate /= float64(endToEndSamples)
	}
	if generationSamples > 0 {
		summary.AverageGenerationTokenRate /= float64(generationSamples)
	}
	if ttftSamples > 0 {
		summary.AverageTTFTMS /= float64(ttftSamples)
	}
	return summary
}

func tokenRate(outputTokens int64, duration time.Duration) float64 {
	if outputTokens <= 0 || duration <= 0 {
		return 0
	}
	return float64(outputTokens) / duration.Seconds()
}

func durationMS(duration time.Duration) float64 {
	if duration <= 0 {
		return 0
	}
	return float64(duration.Microseconds()) / 1000
}

func percentile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	idx := int(float64(len(values)-1) * p)
	return values[idx]
}

func sortCounters(items []Counter) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Client != items[j].Client {
			return items[i].Client < items[j].Client
		}
		if items[i].Model != items[j].Model {
			return items[i].Model < items[j].Model
		}
		if items[i].RouteGroup != items[j].RouteGroup {
			return items[i].RouteGroup < items[j].RouteGroup
		}
		return items[i].Endpoint < items[j].Endpoint
	})
}
