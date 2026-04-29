package proxy

import (
	"sync"
	"time"

	"modelrouter/internal/config"
)

type clientLimiter struct {
	mu      sync.Mutex
	clients map[string]*clientLimitState
}

type clientLimitState struct {
	inflight       int
	windowStart    time.Time
	windowRequests int
}

type clientLimitStatus struct {
	Client             string `json:"client"`
	AccessGroup        string `json:"access_group"`
	MaxConcurrency     int    `json:"max_concurrency"`
	Inflight           int    `json:"inflight"`
	RequestsPerMinute  int    `json:"requests_per_minute"`
	WindowRequests     int    `json:"window_requests"`
	WindowResetUnixSec int64  `json:"window_reset_unix_sec,omitempty"`
}

type clientLimitDecision struct {
	Allowed bool
	Reason  string
}

func newClientLimiter() *clientLimiter {
	return &clientLimiter{clients: map[string]*clientLimitState{}}
}

func (l *clientLimiter) acquire(client string, limit config.RateLimitConfig, now time.Time) clientLimitDecision {
	if client == "" {
		client = "anonymous"
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	state := l.clients[client]
	if state == nil {
		state = &clientLimitState{windowStart: now}
		l.clients[client] = state
	}
	if now.Sub(state.windowStart) >= time.Minute {
		state.windowStart = now
		state.windowRequests = 0
	}
	if limit.MaxConcurrency > 0 && state.inflight >= limit.MaxConcurrency {
		return clientLimitDecision{Allowed: false, Reason: "client concurrency limit exceeded"}
	}
	if limit.RequestsPerMinute > 0 && state.windowRequests >= limit.RequestsPerMinute {
		return clientLimitDecision{Allowed: false, Reason: "client rate limit exceeded"}
	}
	state.inflight++
	state.windowRequests++
	return clientLimitDecision{Allowed: true}
}

func (l *clientLimiter) release(client string) {
	if client == "" {
		client = "anonymous"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.clients[client]
	if state == nil {
		return
	}
	if state.inflight > 0 {
		state.inflight--
	}
}

func (l *clientLimiter) status(cfg *config.Config, now time.Time) []clientLimitStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	items := make([]clientLimitStatus, 0, len(cfg.Auth.Keys))
	for _, key := range cfg.Auth.Keys {
		access := cfg.AccessGroups[key.AccessGroup]
		state := l.clients[key.Name]
		item := clientLimitStatus{
			Client:            key.Name,
			AccessGroup:       key.AccessGroup,
			MaxConcurrency:    access.RateLimit.MaxConcurrency,
			RequestsPerMinute: access.RateLimit.RequestsPerMinute,
		}
		if state != nil {
			if now.Sub(state.windowStart) >= time.Minute {
				state.windowStart = now
				state.windowRequests = 0
			}
			item.Inflight = state.inflight
			item.WindowRequests = state.windowRequests
			if !state.windowStart.IsZero() {
				item.WindowResetUnixSec = state.windowStart.Add(time.Minute).Unix()
			}
		}
		items = append(items, item)
	}
	return items
}
