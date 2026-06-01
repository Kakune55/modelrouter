package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
		Admin: AdminConfig{
			Keys: []AdminKeyConfig{
				{Name: "dashboard", Key: "admin-read-token", Permissions: []string{AdminPermissionRead}},
			},
		},
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

func TestValidateRejectsEmptyRequestOverrideField(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]RouteGroupConfig{
			"group": {
				Strategy: StrategyRoundRobin,
				Endpoints: []EndpointConfig{
					{
						Name:    "ep",
						BaseURL: "http://localhost:8081",
						RequestOverrides: map[string]any{
							" ": 0.7,
						},
					},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateAcceptsWeightedStrategies(t *testing.T) {
	for _, strategy := range []string{StrategyWeightedRoundRobin, StrategyWeightedRandom} {
		cfg := &Config{
			Models: map[string]ModelConfig{
				"demo": {RouteGroup: "group"},
			},
			RouteGroups: map[string]RouteGroupConfig{
				"group": {
					Strategy: strategy,
					Endpoints: []EndpointConfig{
						{Name: "ep", BaseURL: "http://localhost:8081", Weight: 2},
					},
				},
			},
		}

		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate() strategy %q error = %v", strategy, err)
		}
	}
}

func TestMaxResponseBodyBytesDefault(t *testing.T) {
	cfg := &Config{}

	if got := cfg.MaxResponseBodyBytes(); got != 64<<20 {
		t.Fatalf("MaxResponseBodyBytes() = %d", got)
	}
}

func TestValidateRejectsNegativeMaxResponseBodyBytes(t *testing.T) {
	cfg := &Config{
		HTTP: HTTPConfig{MaxResponseBodyBytes: -1},
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

func TestSaveFileWritesLoadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := &Config{
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

	if err := SaveFile(path, cfg); err != nil {
		t.Fatalf("SaveFile() error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat saved file: %v", err)
	}
	loaded, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if _, ok := loaded.Models["demo"]; !ok {
		t.Fatalf("loaded models = %+v", loaded.Models)
	}
}

func TestValidateRejectsInvalidAdminPermission(t *testing.T) {
	cfg := &Config{
		Admin: AdminConfig{
			Keys: []AdminKeyConfig{
				{Name: "dashboard", Key: "admin-read-token", Permissions: []string{"unknown"}},
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

func TestValidateRejectsEmptyEndpointHeaderName(t *testing.T) {
	cfg := &Config{
		Models: map[string]ModelConfig{
			"demo": {RouteGroup: "group"},
		},
		RouteGroups: map[string]RouteGroupConfig{
			"group": {
				Strategy: StrategyRoundRobin,
				Endpoints: []EndpointConfig{
					{Name: "ep", BaseURL: "http://localhost:8081", Headers: map[string]string{"": "value"}},
				},
			},
		},
	}

	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}
