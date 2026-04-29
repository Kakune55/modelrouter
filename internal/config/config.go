package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	StrategyRoundRobin     = "round_robin"
	StrategyRandom         = "random"
	StrategyIPHash         = "ip_hash"
	StrategyFirstAvailable = "first_available"
)

type Config struct {
	Models      map[string]ModelConfig      `json:"models"`
	RouteGroups map[string]RouteGroupConfig `json:"route_groups"`
	HTTP        HTTPConfig                  `json:"http,omitempty"`
}

type HTTPConfig struct {
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
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
	Name           string `json:"name"`
	Model          string `json:"model,omitempty"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key,omitempty"`
	Weight         int    `json:"weight,omitempty"`
	MaxConcurrency int    `json:"max_concurrency,omitempty"`
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

func (c *Config) Timeout() time.Duration {
	if c.HTTP.TimeoutSeconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(c.HTTP.TimeoutSeconds) * time.Second
}

func validStrategy(strategy string) bool {
	switch strategy {
	case StrategyRoundRobin, StrategyRandom, StrategyIPHash, StrategyFirstAvailable:
		return true
	default:
		return false
	}
}
