package config

import "testing"

func TestValidateRejectsMissingRouteGroup(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"demo": {RouteGroup: "missing"},
		},
		RouteGroups: map[string]RouteGroupConfig{
			"other": {
				Strategy: StrategyRoundRobin,
				Endpoints: []EndpointConfig{
					{Name: "ep", BaseURL: "http://localhost:8081"},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateAcceptsValidConfig(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]RouteGroupConfig{
			"group": {
				Strategy: StrategyRandom,
				Endpoints: []EndpointConfig{
					{Name: "ep", BaseURL: "http://localhost:8081"},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateAcceptsSharedAccessGroup(t *testing.T) {
	cfg := &Config{
		Auth: AuthConfig{
			Enabled: true,
			Keys: []ClientKeyConfig{
				{Name: "client-a", Key: "secret-a", AccessGroup: "shared"},
				{Name: "client-b", Key: "secret-b", AccessGroup: "shared"},
			},
		},
		AccessGroups: map[string]AccessGroupConfig{
			"shared": {AllowedModels: []string{"demo-*"}},
		},
		Models: map[string]ModelConfig{
			"demo-model": {RouteGroup: "group"},
		},
		RouteGroups: map[string]RouteGroupConfig{
			"group": {
				Strategy: StrategyRoundRobin,
				Endpoints: []EndpointConfig{
					{Name: "ep", BaseURL: "http://localhost:8081"},
				},
			},
		},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestValidateRejectsEnabledAuthWithoutKeys(t *testing.T) {
	cfg := &Config{
		Auth: AuthConfig{Enabled: true},
		Models: map[string]ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]RouteGroupConfig{
			"group": {
				Strategy: StrategyRoundRobin,
				Endpoints: []EndpointConfig{
					{Name: "ep", BaseURL: "http://localhost:8081"},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateRejectsMissingAccessGroupReference(t *testing.T) {
	cfg := &Config{
		Auth: AuthConfig{
			Enabled: true,
			Keys: []ClientKeyConfig{
				{Name: "client", Key: "secret", AccessGroup: "missing"},
			},
		},
		Models: map[string]ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]RouteGroupConfig{
			"group": {
				Strategy: StrategyRoundRobin,
				Endpoints: []EndpointConfig{
					{Name: "ep", BaseURL: "http://localhost:8081"},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateRejectsEmptyAccessPattern(t *testing.T) {
	cfg := &Config{
		Auth: AuthConfig{
			Enabled: true,
			Keys: []ClientKeyConfig{
				{Name: "client", Key: "secret", AccessGroup: "group-a"},
			},
		},
		AccessGroups: map[string]AccessGroupConfig{
			"group-a": {BlockedModels: []string{""}},
		},
		Models: map[string]ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]RouteGroupConfig{
			"group": {
				Strategy: StrategyRoundRobin,
				Endpoints: []EndpointConfig{
					{Name: "ep", BaseURL: "http://localhost:8081"},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateRejectsNegativeRateLimit(t *testing.T) {
	cfg := &Config{
		Auth: AuthConfig{
			Enabled: true,
			Keys: []ClientKeyConfig{
				{Name: "client", Key: "secret", AccessGroup: "group-a"},
			},
		},
		AccessGroups: map[string]AccessGroupConfig{
			"group-a": {
				RateLimit: RateLimitConfig{MaxConcurrency: -1},
			},
		},
		Models: map[string]ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]RouteGroupConfig{
			"group": {
				Strategy: StrategyRoundRobin,
				Endpoints: []EndpointConfig{
					{Name: "ep", BaseURL: "http://localhost:8081"},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}
