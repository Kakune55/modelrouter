package router

import (
	"hash/fnv"
	"math/rand"
	"sync"
	"sync/atomic"

	"modelrouter/internal/config"
)

type Balancer interface {
	Order(clientIP string) []int
}

func NewBalancer(strategy string, endpoints []config.EndpointConfig) Balancer {
	switch strategy {
	case config.StrategyRandom:
		return &randomBalancer{endpoints: endpoints}
	case config.StrategyIPHash:
		return &ipHashBalancer{endpoints: endpoints}
	case config.StrategyFirstAvailable:
		return &firstBalancer{endpoints: endpoints}
	default:
		return &roundRobinBalancer{endpoints: endpoints}
	}
}

type roundRobinBalancer struct {
	next      uint64
	endpoints []config.EndpointConfig
}

func (b *roundRobinBalancer) Order(_ string) []int {
	order := make([]int, 0, len(b.endpoints))
	if len(b.endpoints) == 0 {
		return order
	}
	idx := int(atomic.AddUint64(&b.next, 1)-1) % len(b.endpoints)
	for i := range b.endpoints {
		order = append(order, (idx+i)%len(b.endpoints))
	}
	return order
}

type randomBalancer struct {
	mu        sync.Mutex
	rng       *rand.Rand
	endpoints []config.EndpointConfig
}

func (b *randomBalancer) Order(_ string) []int {
	order := make([]int, len(b.endpoints))
	for i := range b.endpoints {
		order[i] = i
	}
	if len(order) == 0 {
		return order
	}
	b.mu.Lock()
	if b.rng == nil {
		b.rng = rand.New(rand.NewSource(rand.Int63()))
	}
	b.rng.Shuffle(len(order), func(i, j int) {
		order[i], order[j] = order[j], order[i]
	})
	b.mu.Unlock()
	return order
}

type ipHashBalancer struct {
	endpoints []config.EndpointConfig
}

func (b *ipHashBalancer) Order(clientIP string) []int {
	order := make([]int, 0, len(b.endpoints))
	if len(b.endpoints) == 0 {
		return order
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(clientIP))
	idx := int(h.Sum32()) % len(b.endpoints)
	for i := range b.endpoints {
		order = append(order, (idx+i)%len(b.endpoints))
	}
	return order
}

type firstBalancer struct {
	endpoints []config.EndpointConfig
}

func (b *firstBalancer) Order(_ string) []int {
	order := make([]int, 0, len(b.endpoints))
	for i := range b.endpoints {
		order = append(order, i)
	}
	return order
}
