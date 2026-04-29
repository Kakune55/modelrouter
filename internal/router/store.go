package router

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"modelrouter/internal/config"
)

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
	for _, idx := range group.balancer.Order(clientIP) {
		if group.isCooling(idx, time.Now()) {
			continue
		}
		ep := group.endpoints[idx]
		return &Route{
			Model:         modelName,
			UpstreamModel: model.UpstreamModel,
			Group:         group.Name,
			Strategy:      group.Config.Strategy,
			Endpoint:      ep,
			EndpointID:    idx,
			group:         group,
		}, nil
	}
	return nil, fmt.Errorf("route group has no available endpoint: %s", model.RouteGroup)
}

type Route struct {
	Model         string
	UpstreamModel string
	Group         string
	Strategy      string
	Endpoint      config.EndpointConfig
	EndpointID    int
	group         *GroupRuntime
}

func (r *Route) Fallbacks() []config.EndpointConfig {
	if r.Strategy != config.StrategyFirstAvailable {
		return nil
	}
	out := make([]config.EndpointConfig, 0, len(r.group.endpoints)-1)
	for i, ep := range r.group.endpoints {
		if i == r.EndpointID {
			continue
		}
		if r.group.isCooling(i, time.Now()) {
			continue
		}
		out = append(out, ep)
	}
	return out
}

func (r *Route) MarkSuccess(endpointName string) {
	r.group.markSuccess(endpointName, time.Now())
}

func (r *Route) MarkFailure(endpointName string) {
	r.group.markFailure(endpointName, time.Now())
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

func (g *GroupRuntime) markSuccess(endpointName string, now time.Time) {
	if !g.health.enabled {
		return
	}
	idx := g.endpointIndex(endpointName)
	if idx < 0 {
		return
	}
	state := &g.endpointState[idx]
	state.mu.Lock()
	defer state.mu.Unlock()
	state.consecutiveFailures = 0
	state.lastSuccess = now
}

func (g *GroupRuntime) markFailure(endpointName string, now time.Time) {
	if !g.health.enabled {
		return
	}
	idx := g.endpointIndex(endpointName)
	if idx < 0 {
		return
	}
	state := &g.endpointState[idx]
	state.mu.Lock()
	defer state.mu.Unlock()
	state.consecutiveFailures++
	state.lastFailure = now
	if state.consecutiveFailures >= g.health.failureThreshold {
		state.cooldownUntil = now.Add(g.health.cooldown)
		state.consecutiveFailures = 0
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
	LastFailureUnix     int64  `json:"last_failure_unix_sec,omitempty"`
	LastSuccessUnix     int64  `json:"last_success_unix_sec,omitempty"`
}

func (s *Snapshot) Health() []EndpointHealth {
	now := time.Now()
	items := make([]EndpointHealth, 0)
	for _, group := range s.groups {
		for i, ep := range group.endpoints {
			item := EndpointHealth{
				RouteGroup:    group.Name,
				Endpoint:      ep.Name,
				Model:         ep.Model,
				PassiveHealth: group.health.enabled,
			}
			if i < len(group.endpointState) {
				state := &group.endpointState[i]
				state.mu.RLock()
				item.ConsecutiveFailures = state.consecutiveFailures
				item.Cooling = group.health.enabled && now.Before(state.cooldownUntil)
				if !state.cooldownUntil.IsZero() {
					item.CooldownUntilUnix = state.cooldownUntil.Unix()
				}
				if !state.lastFailure.IsZero() {
					item.LastFailureUnix = state.lastFailure.Unix()
				}
				if !state.lastSuccess.IsZero() {
					item.LastSuccessUnix = state.lastSuccess.Unix()
				}
				state.mu.RUnlock()
			}
			items = append(items, item)
		}
	}
	return items
}
