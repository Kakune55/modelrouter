package metrics

import (
	"sort"
	"sync"
	"time"
)

type Recorder struct {
	mu     sync.RWMutex
	series map[string]*Counter
}

type Counter struct {
	Model              string        `json:"model"`
	RouteGroup         string        `json:"route_group"`
	Endpoint           string        `json:"endpoint"`
	Requests           int64         `json:"requests"`
	Successes          int64         `json:"successes"`
	Failures           int64         `json:"failures"`
	BytesOut           int64         `json:"bytes_out"`
	PromptTokens       int64         `json:"prompt_tokens"`
	OutputTokens       int64         `json:"output_tokens"`
	TotalTokens        int64         `json:"total_tokens"`
	TotalLatency       time.Duration `json:"-"`
	AverageLatencyMS   float64       `json:"average_latency_ms"`
	AverageTokenRate   float64       `json:"average_token_rate"`
	LastRequestUnixSec int64         `json:"last_request_unix_sec"`
}

type Event struct {
	Model        string
	RouteGroup   string
	Endpoint     string
	StatusCode   int
	Duration     time.Duration
	BytesOut     int64
	PromptTokens int64
	OutputTokens int64
	TotalTokens  int64
	Err          error
}

type Snapshot struct {
	GeneratedAtUnixSec int64     `json:"generated_at_unix_sec"`
	Items              []Counter `json:"items"`
}

func NewRecorder() *Recorder {
	return &Recorder{series: map[string]*Counter{}}
}

func (r *Recorder) Record(ev Event) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := ev.Model + "\x00" + ev.RouteGroup + "\x00" + ev.Endpoint
	counter := r.series[key]
	if counter == nil {
		counter = &Counter{Model: ev.Model, RouteGroup: ev.RouteGroup, Endpoint: ev.Endpoint}
		r.series[key] = counter
	}
	counter.Requests++
	if ev.Err == nil && ev.StatusCode >= 200 && ev.StatusCode < 500 {
		counter.Successes++
	} else {
		counter.Failures++
	}
	counter.BytesOut += ev.BytesOut
	counter.PromptTokens += ev.PromptTokens
	counter.OutputTokens += ev.OutputTokens
	counter.TotalTokens += ev.TotalTokens
	counter.TotalLatency += ev.Duration
	counter.LastRequestUnixSec = time.Now().Unix()
	updateDerived(counter)
}

func (r *Recorder) Snapshot() Snapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	items := make([]Counter, 0, len(r.series))
	for _, counter := range r.series {
		copy := *counter
		updateDerived(&copy)
		items = append(items, copy)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Model != items[j].Model {
			return items[i].Model < items[j].Model
		}
		if items[i].RouteGroup != items[j].RouteGroup {
			return items[i].RouteGroup < items[j].RouteGroup
		}
		return items[i].Endpoint < items[j].Endpoint
	})
	return Snapshot{GeneratedAtUnixSec: time.Now().Unix(), Items: items}
}

func updateDerived(c *Counter) {
	if c.Requests > 0 {
		c.AverageLatencyMS = float64(c.TotalLatency.Milliseconds()) / float64(c.Requests)
	}
	seconds := c.TotalLatency.Seconds()
	if seconds > 0 {
		c.AverageTokenRate = float64(c.OutputTokens) / seconds
	}
}
