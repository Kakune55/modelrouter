package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	StrategyRoundRobin         = "round_robin"
	StrategyRandom             = "random"
	StrategyWeightedRoundRobin = "weighted_round_robin"
	StrategyWeightedRandom     = "weighted_random"
	StrategyIPHash             = "ip_hash"
	StrategyFirstAvailable     = "first_available"

	AdminPermissionAll         = "admin:*"
	AdminPermissionRead        = "admin:read"
	AdminPermissionWrite       = "admin:write"
	AdminPermissionConfigRead  = "config:read"
	AdminPermissionConfigWrite = "config:write"
	AdminPermissionMetricsRead = "metrics:read"
	AdminPermissionHealthRead  = "health:read"
	AdminPermissionLimitsRead  = "limits:read"
)

type Config struct {
	Models       map[string]ModelConfig       `json:"models"`
	RouteGroups  map[string]RouteGroupConfig  `json:"route_groups"`
	HTTP         HTTPConfig                   `json:"http,omitempty"`
	Admin        AdminConfig                  `json:"admin,omitempty"`
	Auth         AuthConfig                   `json:"auth,omitempty"`
	AccessGroups map[string]AccessGroupConfig `json:"access_groups,omitempty"`
	Features     FeaturesConfig               `json:"features,omitempty"`
	UsageLog     UsageLogConfig               `json:"usage_log,omitempty"`
}

type HTTPConfig struct {
	TimeoutSeconds       int   `json:"timeout_seconds,omitempty"`
	IdleTimeoutSeconds   int   `json:"idle_timeout_seconds,omitempty"`
	TotalTimeoutSeconds  int   `json:"total_timeout_seconds,omitempty"`
	MaxResponseBodyBytes int64 `json:"max_response_body_bytes,omitempty"`
}

type AdminConfig struct {
	Token string           `json:"token,omitempty"`
	Keys  []AdminKeyConfig `json:"keys,omitempty"`
}

type AdminKeyConfig struct {
	Name        string   `json:"name"`
	Key         string   `json:"key"`
	Permissions []string `json:"permissions"`
}

type FeaturesConfig struct {
	AutoIncludeStreamUsage bool `json:"auto_include_stream_usage,omitempty"`
}

type UsageLogConfig struct {
	Enabled        bool   `json:"enabled,omitempty"`
	Dir            string `json:"dir,omitempty"`
	RetentionHours int    `json:"retention_hours,omitempty"`
}

type AuthConfig struct {
	Enabled bool              `json:"enabled,omitempty"`
	Keys    []ClientKeyConfig `json:"keys,omitempty"`
}

type ClientKeyConfig struct {
	Name        string `json:"name"`
	Key         string `json:"key"`
	AccessGroup string `json:"access_group"`
}

type AccessGroupConfig struct {
	AllowedModels []string        `json:"allowed_models,omitempty"`
	BlockedModels []string        `json:"blocked_models,omitempty"`
	RateLimit     RateLimitConfig `json:"rate_limit,omitempty"`
}

type RateLimitConfig struct {
	MaxConcurrency    int `json:"max_concurrency,omitempty"`
	RequestsPerMinute int `json:"requests_per_minute,omitempty"`
}

type ModelConfig struct {
	RouteGroup    string `json:"route_group"`
	UpstreamModel string `json:"upstream_model,omitempty"`
}

type RouteGroupConfig struct {
	Strategy      string              `json:"strategy"`
	PassiveHealth PassiveHealthConfig `json:"passive_health,omitempty"`
	Endpoints     []EndpointConfig    `json:"endpoints"`
}

type PassiveHealthConfig struct {
	Enabled          bool `json:"enabled,omitempty"`
	FailureThreshold int  `json:"failure_threshold,omitempty"`
	CooldownSeconds  int  `json:"cooldown_seconds,omitempty"`
}

type EndpointConfig struct {
	Name             string            `json:"name"`
	Model            string            `json:"model,omitempty"`
	BaseURL          string            `json:"base_url"`
	APIKey           string            `json:"api_key,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	RequestDefaults  map[string]any    `json:"request_defaults,omitempty"`
	RequestOverrides map[string]any    `json:"request_overrides,omitempty"`
	Weight           int               `json:"weight,omitempty"`
	MaxConcurrency   int               `json:"max_concurrency,omitempty"`
}

func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func DecodeJSON(data []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func SaveFile(path string, cfg *Config) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("config path must not be empty")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config is nil")
	}
	if len(c.Models) == 0 {
		return errors.New("models must not be empty")
	}
	if len(c.RouteGroups) == 0 {
		return errors.New("route_groups must not be empty")
	}
	if c.HTTP.TimeoutSeconds < 0 {
		return errors.New("http.timeout_seconds must not be negative")
	}
	if c.HTTP.IdleTimeoutSeconds < 0 {
		return errors.New("http.idle_timeout_seconds must not be negative")
	}
	if c.HTTP.TotalTimeoutSeconds < 0 {
		return errors.New("http.total_timeout_seconds must not be negative")
	}
	if c.HTTP.MaxResponseBodyBytes < 0 {
		return errors.New("http.max_response_body_bytes must not be negative")
	}
	if c.UsageLog.RetentionHours < 0 {
		return errors.New("usage_log.retention_hours must not be negative")
	}
	if err := c.validateAdmin(); err != nil {
		return err
	}
	if err := c.validateAuth(); err != nil {
		return err
	}

	for name, model := range c.Models {
		if strings.TrimSpace(name) == "" {
			return errors.New("model name must not be empty")
		}
		if strings.TrimSpace(model.RouteGroup) == "" {
			return fmt.Errorf("model %q route_group must not be empty", name)
		}
		if _, ok := c.RouteGroups[model.RouteGroup]; !ok {
			return fmt.Errorf("model %q references missing route_group %q", name, model.RouteGroup)
		}
	}

	for name, group := range c.RouteGroups {
		if strings.TrimSpace(name) == "" {
			return errors.New("route_group name must not be empty")
		}
		if !validStrategy(group.Strategy) {
			return fmt.Errorf("route_group %q has unsupported strategy %q", name, group.Strategy)
		}
		if len(group.Endpoints) == 0 {
			return fmt.Errorf("route_group %q endpoints must not be empty", name)
		}
		if group.PassiveHealth.FailureThreshold < 0 {
			return fmt.Errorf("route_group %q passive_health.failure_threshold must not be negative", name)
		}
		if group.PassiveHealth.CooldownSeconds < 0 {
			return fmt.Errorf("route_group %q passive_health.cooldown_seconds must not be negative", name)
		}
		seen := map[string]struct{}{}
		for i, ep := range group.Endpoints {
			if strings.TrimSpace(ep.Name) == "" {
				return fmt.Errorf("route_group %q endpoint %d name must not be empty", name, i)
			}
			if _, ok := seen[ep.Name]; ok {
				return fmt.Errorf("route_group %q has duplicate endpoint %q", name, ep.Name)
			}
			seen[ep.Name] = struct{}{}
			if strings.TrimSpace(ep.BaseURL) == "" {
				return fmt.Errorf("route_group %q endpoint %q base_url must not be empty", name, ep.Name)
			}
			for headerName := range ep.Headers {
				if strings.TrimSpace(headerName) == "" {
					return fmt.Errorf("route_group %q endpoint %q has empty header name", name, ep.Name)
				}
			}
			for fieldName := range ep.RequestDefaults {
				if strings.TrimSpace(fieldName) == "" {
					return fmt.Errorf("route_group %q endpoint %q has empty request_defaults field name", name, ep.Name)
				}
			}
			for fieldName := range ep.RequestOverrides {
				if strings.TrimSpace(fieldName) == "" {
					return fmt.Errorf("route_group %q endpoint %q has empty request_overrides field name", name, ep.Name)
				}
			}
			if ep.Weight < 0 {
				return fmt.Errorf("route_group %q endpoint %q weight must not be negative", name, ep.Name)
			}
			if ep.MaxConcurrency < 0 {
				return fmt.Errorf("route_group %q endpoint %q max_concurrency must not be negative", name, ep.Name)
			}
		}
	}
	return nil
}

func (c *Config) validateAdmin() error {
	names := map[string]struct{}{}
	keys := map[string]struct{}{}
	if strings.TrimSpace(c.Admin.Token) != "" {
		keys[c.Admin.Token] = struct{}{}
	}
	for i, key := range c.Admin.Keys {
		if strings.TrimSpace(key.Name) == "" {
			return fmt.Errorf("admin.keys[%d].name must not be empty", i)
		}
		if strings.TrimSpace(key.Key) == "" {
			return fmt.Errorf("admin.keys[%d].key must not be empty", i)
		}
		if len(key.Permissions) == 0 {
			return fmt.Errorf("admin key %q permissions must not be empty", key.Name)
		}
		if _, ok := names[key.Name]; ok {
			return fmt.Errorf("admin key name %q is duplicated", key.Name)
		}
		names[key.Name] = struct{}{}
		if _, ok := keys[key.Key]; ok {
			return fmt.Errorf("admin key value for %q is duplicated", key.Name)
		}
		keys[key.Key] = struct{}{}
		for _, permission := range key.Permissions {
			if !validAdminPermission(permission) {
				return fmt.Errorf("admin key %q has unsupported permission %q", key.Name, permission)
			}
		}
	}
	return nil
}

func (c *Config) validateAuth() error {
	if !c.Auth.Enabled {
		return nil
	}
	if len(c.Auth.Keys) == 0 {
		return errors.New("auth.keys must not be empty when auth is enabled")
	}
	if len(c.AccessGroups) == 0 {
		return errors.New("access_groups must not be empty when auth is enabled")
	}
	names := map[string]struct{}{}
	keys := map[string]struct{}{}
	for i, key := range c.Auth.Keys {
		if strings.TrimSpace(key.Name) == "" {
			return fmt.Errorf("auth.keys[%d].name must not be empty", i)
		}
		if strings.TrimSpace(key.Key) == "" {
			return fmt.Errorf("auth.keys[%d].key must not be empty", i)
		}
		if strings.TrimSpace(key.AccessGroup) == "" {
			return fmt.Errorf("auth.keys[%d].access_group must not be empty", i)
		}
		if _, ok := c.AccessGroups[key.AccessGroup]; !ok {
			return fmt.Errorf("auth key %q references missing access_group %q", key.Name, key.AccessGroup)
		}
		if _, ok := names[key.Name]; ok {
			return fmt.Errorf("auth key name %q is duplicated", key.Name)
		}
		names[key.Name] = struct{}{}
		if _, ok := keys[key.Key]; ok {
			return fmt.Errorf("auth key value for %q is duplicated", key.Name)
		}
		keys[key.Key] = struct{}{}
	}
	for name, access := range c.AccessGroups {
		if strings.TrimSpace(name) == "" {
			return errors.New("access group name must not be empty")
		}
		for _, model := range access.AllowedModels {
			if strings.TrimSpace(model) == "" {
				return fmt.Errorf("access group %q has empty allowed model", name)
			}
		}
		for _, model := range access.BlockedModels {
			if strings.TrimSpace(model) == "" {
				return fmt.Errorf("access group %q has empty blocked model", name)
			}
		}
		if access.RateLimit.MaxConcurrency < 0 {
			return fmt.Errorf("access group %q rate_limit.max_concurrency must not be negative", name)
		}
		if access.RateLimit.RequestsPerMinute < 0 {
			return fmt.Errorf("access group %q rate_limit.requests_per_minute must not be negative", name)
		}
	}
	return nil
}

func validAdminPermission(permission string) bool {
	switch permission {
	case AdminPermissionAll,
		AdminPermissionRead,
		AdminPermissionWrite,
		AdminPermissionConfigRead,
		AdminPermissionConfigWrite,
		AdminPermissionMetricsRead,
		AdminPermissionHealthRead,
		AdminPermissionLimitsRead:
		return true
	default:
		return false
	}
}

func (c *Config) IdleTimeout() time.Duration {
	if c.HTTP.IdleTimeoutSeconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(c.HTTP.IdleTimeoutSeconds) * time.Second
}

func (c *Config) TotalTimeout() time.Duration {
	if c.HTTP.TotalTimeoutSeconds > 0 {
		return time.Duration(c.HTTP.TotalTimeoutSeconds) * time.Second
	}
	// timeout_seconds is the deprecated name for the total request timeout.
	// Keeping it as a fallback preserves the behavior of existing configs.
	if c.HTTP.TimeoutSeconds > 0 {
		return time.Duration(c.HTTP.TimeoutSeconds) * time.Second
	}
	return 0
}

func (c *Config) MaxResponseBodyBytes() int64 {
	if c.HTTP.MaxResponseBodyBytes <= 0 {
		return 64 << 20
	}
	return c.HTTP.MaxResponseBodyBytes
}

func validStrategy(strategy string) bool {
	switch strategy {
	case StrategyRoundRobin, StrategyRandom, StrategyWeightedRoundRobin, StrategyWeightedRandom, StrategyIPHash, StrategyFirstAvailable:
		return true
	default:
		return false
	}
}
