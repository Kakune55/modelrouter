package proxy

import (
	"testing"
	"time"

	"modelrouter/internal/config"
)

func TestClientLimiterMaxConcurrency(t *testing.T) {
	limiter := newClientLimiter()
	limit := config.RateLimitConfig{MaxConcurrency: 1}
	now := time.Now()

	if decision := limiter.acquire("client", limit, now); !decision.Allowed {
		t.Fatalf("first acquire rejected: %s", decision.Reason)
	}
	if decision := limiter.acquire("client", limit, now); decision.Allowed {
		t.Fatal("second acquire should be rejected")
	}
	limiter.release("client")
	if decision := limiter.acquire("client", limit, now); !decision.Allowed {
		t.Fatalf("acquire after release rejected: %s", decision.Reason)
	}
}

func TestClientLimiterRequestsPerMinute(t *testing.T) {
	limiter := newClientLimiter()
	limit := config.RateLimitConfig{RequestsPerMinute: 1}
	now := time.Now()

	if decision := limiter.acquire("client", limit, now); !decision.Allowed {
		t.Fatalf("first acquire rejected: %s", decision.Reason)
	}
	limiter.release("client")
	if decision := limiter.acquire("client", limit, now.Add(10*time.Second)); decision.Allowed {
		t.Fatal("second request in same window should be rejected")
	}
	if decision := limiter.acquire("client", limit, now.Add(time.Minute)); !decision.Allowed {
		t.Fatalf("request after window reset rejected: %s", decision.Reason)
	}
}

func TestClientLimitStatusIncludesEndpointConcurrencyLimit(t *testing.T) {
	limiter := newClientLimiter()
	cfg := &config.Config{
		Auth: config.AuthConfig{
			Keys: []config.ClientKeyConfig{{Name: "client", AccessGroup: "group"}},
		},
		AccessGroups: map[string]config.AccessGroupConfig{
			"group": {
				RateLimit: config.RateLimitConfig{MaxConcurrencyPerEndpoint: 4},
			},
		},
	}

	items := limiter.status(cfg, time.Now())
	if len(items) != 1 || items[0].MaxConcurrencyPerEndpoint != 4 {
		t.Fatalf("limit status = %+v", items)
	}
}
