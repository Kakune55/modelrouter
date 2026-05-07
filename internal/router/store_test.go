package router

import (
	"testing"

	"modelrouter/internal/config"
)

func TestSnapshotPick(t *testing.T) {
	store := NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "a", BaseURL: "http://a"},
					{Name: "b", BaseURL: "http://b"},
				},
			},
		},
	})

	first, err := store.Get().Pick("demo", "127.0.0.1")
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	second, err := store.Get().Pick("demo", "127.0.0.1")
	if err != nil {
		t.Fatalf("Pick() second error = %v", err)
	}
	if first.Endpoint.Name == second.Endpoint.Name {
		t.Fatalf("round robin did not rotate: first=%s second=%s", first.Endpoint.Name, second.Endpoint.Name)
	}
}

func TestIPHashStableForSameIP(t *testing.T) {
	store := NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyIPHash,
				Endpoints: []config.EndpointConfig{
					{Name: "a", BaseURL: "http://a"},
					{Name: "b", BaseURL: "http://b"},
				},
			},
		},
	})

	first, err := store.Get().Pick("demo", "10.0.0.5")
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	second, err := store.Get().Pick("demo", "10.0.0.5")
	if err != nil {
		t.Fatalf("Pick() second error = %v", err)
	}
	if first.Endpoint.Name != second.Endpoint.Name {
		t.Fatalf("ip hash changed endpoint: first=%s second=%s", first.Endpoint.Name, second.Endpoint.Name)
	}
}

func TestWeightedRoundRobinUsesEndpointWeights(t *testing.T) {
	store := NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyWeightedRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "a", BaseURL: "http://a", Weight: 2},
					{Name: "b", BaseURL: "http://b", Weight: 1},
				},
			},
		},
	})

	counts := map[string]int{}
	for i := 0; i < 6; i++ {
		route, err := store.Get().Pick("demo", "127.0.0.1")
		if err != nil {
			t.Fatalf("Pick() error = %v", err)
		}
		counts[route.Endpoint.Name]++
	}

	if counts["a"] != 4 || counts["b"] != 2 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestWeightedRoundRobinDeduplicatesFallbackCandidates(t *testing.T) {
	store := NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyWeightedRoundRobin,
				Endpoints: []config.EndpointConfig{
					{Name: "a", BaseURL: "http://a", Weight: 3},
					{Name: "b", BaseURL: "http://b", Weight: 1},
				},
			},
		},
	})

	route, err := store.Get().Pick("demo", "127.0.0.1")
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	candidates := route.Candidates()
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, candidates = %+v", len(candidates), candidates)
	}
	if candidates[0].Name == candidates[1].Name {
		t.Fatalf("duplicate fallback candidate: %+v", candidates)
	}
}

func TestWeightedRandomReturnsAllCandidatesOnce(t *testing.T) {
	store := NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyWeightedRandom,
				Endpoints: []config.EndpointConfig{
					{Name: "a", BaseURL: "http://a", Weight: 2},
					{Name: "b", BaseURL: "http://b", Weight: 1},
					{Name: "c", BaseURL: "http://c", Weight: 1},
				},
			},
		},
	})

	route, err := store.Get().Pick("demo", "127.0.0.1")
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	seen := map[string]bool{}
	for _, endpoint := range route.Candidates() {
		if seen[endpoint.Name] {
			t.Fatalf("duplicate candidate %q in %+v", endpoint.Name, route.Candidates())
		}
		seen[endpoint.Name] = true
	}
	if len(seen) != 3 {
		t.Fatalf("seen = %+v", seen)
	}
}

func TestPassiveHealthSkipsCoolingEndpoint(t *testing.T) {
	store := NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyFirstAvailable,
				PassiveHealth: config.PassiveHealthConfig{
					Enabled:          true,
					FailureThreshold: 1,
					CooldownSeconds:  60,
				},
				Endpoints: []config.EndpointConfig{
					{Name: "a", BaseURL: "http://a"},
					{Name: "b", BaseURL: "http://b"},
				},
			},
		},
	})

	first, err := store.Get().Pick("demo", "127.0.0.1")
	if err != nil {
		t.Fatalf("Pick() first error = %v", err)
	}
	if first.Endpoint.Name != "a" {
		t.Fatalf("first endpoint = %s", first.Endpoint.Name)
	}

	first.MarkFailure("a", 502, nil)

	second, err := store.Get().Pick("demo", "127.0.0.1")
	if err != nil {
		t.Fatalf("Pick() second error = %v", err)
	}
	if second.Endpoint.Name != "b" {
		t.Fatalf("second endpoint = %s", second.Endpoint.Name)
	}
}

func TestPassiveHealthReturnsErrorWhenAllEndpointsCooling(t *testing.T) {
	store := NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyFirstAvailable,
				PassiveHealth: config.PassiveHealthConfig{
					Enabled:          true,
					FailureThreshold: 1,
					CooldownSeconds:  60,
				},
				Endpoints: []config.EndpointConfig{
					{Name: "a", BaseURL: "http://a"},
				},
			},
		},
	})

	route, err := store.Get().Pick("demo", "127.0.0.1")
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	route.MarkFailure("a", 502, nil)

	if _, err := store.Get().Pick("demo", "127.0.0.1"); err == nil {
		t.Fatal("expected no available endpoint error")
	}
}

func TestHealthIncludesRecentEndpointStatus(t *testing.T) {
	store := NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyFirstAvailable,
				Endpoints: []config.EndpointConfig{
					{Name: "a", BaseURL: "http://a"},
				},
			},
		},
	})

	route, err := store.Get().Pick("demo", "127.0.0.1")
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	route.MarkFailure("a", 503, nil)

	health := store.Get().Health()
	if len(health) != 1 {
		t.Fatalf("health = %+v", health)
	}
	if health[0].LastStatusCode != 503 {
		t.Fatalf("last status = %d", health[0].LastStatusCode)
	}
	if health[0].LastError == "" || health[0].LastErrorUnix == 0 || health[0].LastFailureUnix == 0 {
		t.Fatalf("health item missing recent error fields: %+v", health[0])
	}

	route.MarkSuccess("a", 200)
	health = store.Get().Health()
	if health[0].LastStatusCode != 200 || health[0].LastSuccessUnix == 0 {
		t.Fatalf("health item missing success fields: %+v", health[0])
	}
}

func TestEndpointMaxConcurrency(t *testing.T) {
	store := NewStore(&config.Config{
		Models: map[string]config.ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]config.RouteGroupConfig{
			"group": {
				Strategy: config.StrategyFirstAvailable,
				Endpoints: []config.EndpointConfig{
					{Name: "a", BaseURL: "http://a", MaxConcurrency: 1},
				},
			},
		},
	})

	route, err := store.Get().Pick("demo", "127.0.0.1")
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if !route.TryAcquire("a") {
		t.Fatal("first acquire failed")
	}
	if route.TryAcquire("a") {
		t.Fatal("second acquire should be limited")
	}
	route.Release("a")
	if !route.TryAcquire("a") {
		t.Fatal("acquire after release failed")
	}
	route.Release("a")
}
