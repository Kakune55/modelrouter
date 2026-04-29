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

	first.MarkFailure("a")

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
	route.MarkFailure("a")

	if _, err := store.Get().Pick("demo", "127.0.0.1"); err == nil {
		t.Fatal("expected no available endpoint error")
	}
}
