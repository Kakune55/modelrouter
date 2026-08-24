package router

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"modelrouter/internal/config"
)

var ErrNoAvailableEndpoint = errors.New("route group has no available endpoint")

type Store struct {
	current atomic.Value
}

type Snapshot struct {
	Config *config.Config
	groups map[string]*GroupRuntime
}

type GroupRuntime struct {
	Name          string
	Config        config.RouteGroupConfig
	health        passiveHealthRuntime
	balancer      Balancer
	endpoints     []config.EndpointConfig
	endpointState []endpointState
}

type passiveHealthRuntime struct {
	enabled          bool
	failureThreshold int
	cooldown         time.Duration
}

type endpointState struct {
	mu                  sync.RWMutex
	consecutiveFailures int
	cooldownUntil       time.Time
	lastFailure         time.Time
	lastSuccess         time.Time
	lastStatusCode      int
	lastError           string
	lastErrorAt         time.Time
	inflight            int
	inflightByClient    map[string]int
}

func NewStore(cfg *config.Config) *Store {
	s := &Store{}
	s.Update(cfg)
	return s
}

func (s *Store) Update(cfg *config.Config) {
	s.current.Store(newSnapshot(cfg))
}

func (s *Store) Get() *Snapshot {
	return s.current.Load().(*Snapshot)
}

func newSnapshot(cfg *config.Config) *Snapshot {
	snap := &Snapshot{
		Config: cfg,
		groups: make(map[string]*GroupRuntime, len(cfg.RouteGroups)),
	}
	for name, group := range cfg.RouteGroups {
		endpoints := append([]config.EndpointConfig(nil), group.Endpoints...)
		health := newPassiveHealthRuntime(group.PassiveHealth)
		snap.groups[name] = &GroupRuntime{
			Name:          name,
			Config:        group,
			health:        health,
			balancer:      NewBalancer(group.Strategy, endpoints),
			endpoints:     endpoints,
			endpointState: make([]endpointState, len(endpoints)),
		}
	}
	return snap
}

func newPassiveHealthRuntime(cfg config.PassiveHealthConfig) passiveHealthRuntime {
	if !cfg.Enabled {
		return passiveHealthRuntime{}
	}
	threshold := cfg.FailureThreshold
	if threshold <= 0 {
		threshold = 1
	}
	cooldownSeconds := cfg.CooldownSeconds
	if cooldownSeconds <= 0 {
		cooldownSeconds = 30
	}
	return passiveHealthRuntime{
		enabled:          true,
		failureThreshold: threshold,
		cooldown:         time.Duration(cooldownSeconds) * time.Second,
	}
}

func (s *Snapshot) Pick(modelName string, clientIP string) (*Route, error) {
	model, ok := s.Config.Models[modelName]
	if !ok {
		return nil, fmt.Errorf("model not configured: %s", modelName)
	}
	group, ok := s.groups[model.RouteGroup]
	if !ok {
		return nil, fmt.Errorf("route group not found: %s", model.RouteGroup)
	}
	candidates := make([]config.EndpointConfig, 0, len(group.endpoints))
	for _, idx := range group.balancer.Order(clientIP) {
		if group.isCooling(idx, time.Now()) {
			continue
		}
		candidates = append(candidates, group.endpoints[idx])
	}
	if len(candidates) > 0 {
		return &Route{
			Model:         modelName,
			UpstreamModel: model.UpstreamModel,
			Group:         group.Name,
			Strategy:      group.Config.Strategy,
			Endpoint:      candidates[0],
			candidates:    candidates,
			group:         group,
		}, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNoAvailableEndpoint, model.RouteGroup)
}

type Route struct {
	Model         string
	UpstreamModel string
	Group         string
	Strategy      string
	Endpoint      config.EndpointConfig
	candidates    []config.EndpointConfig
	group         *GroupRuntime
}

func (r *Route) Candidates() []config.EndpointConfig {
	out := make([]config.EndpointConfig, len(r.candidates))
	copy(out, r.candidates)
	return out
}

func (r *Route) MarkSuccess(endpointName string, statusCode int) {
	r.group.markSuccess(endpointName, statusCode, time.Now())
}

func (r *Route) MarkFailure(endpointName string, statusCode int, err error) {
	r.group.markFailure(endpointName, statusCode, err, time.Now())
}

func (r *Route) TryAcquire(endpointName string) bool {
	return r.group.tryAcquire(endpointName, "", 0)
}

func (r *Route) Release(endpointName string) {
	r.group.release(endpointName, "", 0)

}

func (r *Route) TryAcquireForClient(endpointName, clientName string, limit int) bool {
	return r.group.tryAcquire(endpointName, clientName, limit)
}

func (r *Route) ReleaseForClient(endpointName, clientName string, limit int) {
	r.group.release(endpointName, clientName, limit)
}

func (g *GroupRuntime) isCooling(idx int, now time.Time) bool {
	if !g.health.enabled || idx < 0 || idx >= len(g.endpointState) {
		return false
	}
	state := &g.endpointState[idx]
	state.mu.RLock()
	defer state.mu.RUnlock()
	return now.Before(state.cooldownUntil)
}

func (g *GroupRuntime) markSuccess(endpointName string, statusCode int, now time.Time) {
	idx := g.endpointIndex(endpointName)
	if idx < 0 {
		return
	}
	state := &g.endpointState[idx]
	state.mu.Lock()
	defer state.mu.Unlock()
	state.consecutiveFailures = 0
	state.lastSuccess = now
	if statusCode > 0 {
		state.lastStatusCode = statusCode
	}
}

func (g *GroupRuntime) markFailure(endpointName string, statusCode int, err error, now time.Time) {
	idx := g.endpointIndex(endpointName)
	if idx < 0 {
		return
	}
	state := &g.endpointState[idx]
	state.mu.Lock()
	defer state.mu.Unlock()
	if statusCode > 0 {
		state.lastStatusCode = statusCode
	}
	state.lastError = failureMessage(statusCode, err)
	state.lastErrorAt = now
	state.lastFailure = now
	if !g.health.enabled {
		return
	}
	state.consecutiveFailures++
	if state.consecutiveFailures >= g.health.failureThreshold {
		state.cooldownUntil = now.Add(g.health.cooldown)
		state.consecutiveFailures = 0
	}
}

func failureMessage(statusCode int, err error) string {
	if err != nil {
		return err.Error()
	}
	if statusCode > 0 {
		return fmt.Sprintf("upstream returned status %d", statusCode)
	}
	return "upstream request failed"
}

func (g *GroupRuntime) tryAcquire(endpointName, clientName string, perClientLimit int) bool {
	idx := g.endpointIndex(endpointName)
	if idx < 0 {
		return false
	}
	globalLimit := g.endpoints[idx].MaxConcurrency
	if globalLimit <= 0 && perClientLimit <= 0 {
		return true
	}
	state := &g.endpointState[idx]
	state.mu.Lock()
	defer state.mu.Unlock()
	if globalLimit > 0 && state.inflight >= globalLimit {
		return false
	}
	if perClientLimit > 0 && state.inflightByClient[clientName] >= perClientLimit {
		return false
	}
	state.inflight++
	if perClientLimit > 0 {
		if state.inflightByClient == nil {
			state.inflightByClient = make(map[string]int)
		}
		state.inflightByClient[clientName]++
	}
	return true
}

func (g *GroupRuntime) release(endpointName, clientName string, perClientLimit int) {
	idx := g.endpointIndex(endpointName)
	if idx < 0 || (g.endpoints[idx].MaxConcurrency <= 0 && perClientLimit <= 0) {
		return
	}
	state := &g.endpointState[idx]
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.inflight > 0 {
		state.inflight--
	}
	if perClientLimit <= 0 {
		return
	}
	if inflight := state.inflightByClient[clientName]; inflight > 1 {
		state.inflightByClient[clientName] = inflight - 1
	} else {
		delete(state.inflightByClient, clientName)
	}
}

func (g *GroupRuntime) endpointIndex(endpointName string) int {
	for i, ep := range g.endpoints {
		if ep.Name == endpointName {
			return i
		}
	}
	return -1
}

type EndpointHealth struct {
	RouteGroup          string `json:"route_group"`
	Endpoint            string `json:"endpoint"`
	Model               string `json:"model,omitempty"`
	PassiveHealth       bool   `json:"passive_health"`
	Cooling             bool   `json:"cooling"`
	CooldownUntilUnix   int64  `json:"cooldown_until_unix_sec,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	LastStatusCode      int    `json:"last_status_code,omitempty"`
	LastError           string `json:"last_error,omitempty"`
	LastErrorUnix       int64  `json:"last_error_unix_sec,omitempty"`
	LastFailureUnix     int64  `json:"last_failure_unix_sec,omitempty"`
	LastSuccessUnix     int64  `json:"last_success_unix_sec,omitempty"`
	MaxConcurrency      int    `json:"max_concurrency,omitempty"`
	Inflight            int    `json:"inflight"`
}

func (s *Snapshot) Health() []EndpointHealth {
	now := time.Now()
	items := make([]EndpointHealth, 0)
	for _, group := range s.groups {
		for i, ep := range group.endpoints {
			item := EndpointHealth{
				RouteGroup:     group.Name,
				Endpoint:       ep.Name,
				Model:          ep.Model,
				PassiveHealth:  group.health.enabled,
				MaxConcurrency: ep.MaxConcurrency,
			}
			if i < len(group.endpointState) {
				state := &group.endpointState[i]
				state.mu.RLock()
				item.ConsecutiveFailures = state.consecutiveFailures
				item.Cooling = group.health.enabled && now.Before(state.cooldownUntil)
				item.LastStatusCode = state.lastStatusCode
				item.LastError = state.lastError
				if !state.cooldownUntil.IsZero() {
					item.CooldownUntilUnix = state.cooldownUntil.Unix()
				}
				if !state.lastErrorAt.IsZero() {
					item.LastErrorUnix = state.lastErrorAt.Unix()
				}
				if !state.lastFailure.IsZero() {
					item.LastFailureUnix = state.lastFailure.Unix()
				}
				if !state.lastSuccess.IsZero() {
					item.LastSuccessUnix = state.lastSuccess.Unix()
				}
				item.Inflight = state.inflight
				state.mu.RUnlock()
			}
			items = append(items, item)
		}
	}
	return items
}
