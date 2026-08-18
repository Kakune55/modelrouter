package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestValidateRejectsEmptyRequestDefaultField(t *testing.T) {
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
						RequestDefaults: map[string]any{
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

func TestHTTPTimeouts(t *testing.T) {
	tests := []struct {
		name      string
		http      HTTPConfig
		wantIdle  time.Duration
		wantTotal time.Duration
	}{
		{
			name:      "defaults to idle timeout only",
			wantIdle:  120 * time.Second,
			wantTotal: 0,
		},
		{
			name:      "explicit idle and total timeouts",
			http:      HTTPConfig{IdleTimeoutSeconds: 30, TotalTimeoutSeconds: 600},
			wantIdle:  30 * time.Second,
			wantTotal: 600 * time.Second,
		},
		{
			name:      "deprecated timeout remains a total timeout",
			http:      HTTPConfig{TimeoutSeconds: 90},
			wantIdle:  120 * time.Second,
			wantTotal: 90 * time.Second,
		},
		{
			name:      "new total timeout takes precedence over deprecated timeout",
			http:      HTTPConfig{TimeoutSeconds: 90, TotalTimeoutSeconds: 300},
			wantIdle:  120 * time.Second,
			wantTotal: 300 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{HTTP: tt.http}
			if got := cfg.IdleTimeout(); got != tt.wantIdle {
				t.Fatalf("IdleTimeout() = %s, want %s", got, tt.wantIdle)
			}
			if got := cfg.TotalTimeout(); got != tt.wantTotal {
				t.Fatalf("TotalTimeout() = %s, want %s", got, tt.wantTotal)
			}
		})
	}
}

func TestValidateRejectsNegativeHTTPTimeouts(t *testing.T) {
	for _, httpConfig := range []HTTPConfig{
		{TimeoutSeconds: -1},
		{IdleTimeoutSeconds: -1},
		{TotalTimeoutSeconds: -1},
	} {
		cfg := &Config{
			HTTP: httpConfig,
			Models: map[string]ModelConfig{
				"demo": {RouteGroup: "group"},
			},
			RouteGroups: map[string]RouteGroupConfig{
				"group": {
					Strategy:  StrategyRoundRobin,
					Endpoints: []EndpointConfig{{Name: "ep", BaseURL: "http://localhost:8081"}},
				},
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatalf("expected validation error for HTTP config %+v", httpConfig)
		}
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
