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
	case config.StrategyWeightedRoundRobin:
		return newWeightedRoundRobinBalancer(endpoints)
	case config.StrategyWeightedRandom:
		return newWeightedRandomBalancer(endpoints)
	case config.StrategyIPHash:
		return &ipHashBalancer{endpoints: endpoints}
	case config.StrategyFirstAvailable:
		return &firstBalancer{endpoints: endpoints}
	default:
		return &roundRobinBalancer{endpoints: endpoints}
	}
}

type roundRobinBalancer struct {
	next      atomic.Uint64
	endpoints []config.EndpointConfig
}

func (b *roundRobinBalancer) Order(_ string) []int {
	order := make([]int, 0, len(b.endpoints))
	if len(b.endpoints) == 0 {
		return order
	}
	idx := int(b.next.Add(1)-1) % len(b.endpoints)
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

type weightedRoundRobinBalancer struct {
	mu        sync.Mutex
	endpoints []config.EndpointConfig
	weights   []int
	current   []int
	total     int
}

func newWeightedRoundRobinBalancer(endpoints []config.EndpointConfig) Balancer {
	weights, total := endpointWeights(endpoints)
	return &weightedRoundRobinBalancer{
		endpoints: endpoints,
		weights:   weights,
		current:   make([]int, len(endpoints)),
		total:     total,
	}
}

func (b *weightedRoundRobinBalancer) Order(_ string) []int {
	if len(b.endpoints) == 0 {
		return nil
	}
	b.mu.Lock()
	best := 0
	for i, weight := range b.weights {
		b.current[i] += weight
		if b.current[i] > b.current[best] {
			best = i
		}
	}
	b.current[best] -= b.total
	b.mu.Unlock()

	return orderStartingAt(len(b.endpoints), best)
}

type weightedRandomBalancer struct {
	mu        sync.Mutex
	rng       *rand.Rand
	endpoints []config.EndpointConfig
	weights   []int
	total     int
}

func newWeightedRandomBalancer(endpoints []config.EndpointConfig) Balancer {
	weights, total := endpointWeights(endpoints)
	return &weightedRandomBalancer{
		endpoints: endpoints,
		weights:   weights,
		total:     total,
	}
}

func (b *weightedRandomBalancer) Order(_ string) []int {
	if len(b.endpoints) == 0 {
		return nil
	}
	b.mu.Lock()
	if b.rng == nil {
		b.rng = rand.New(rand.NewSource(rand.Int63()))
	}
	remaining := b.rng.Intn(b.total)
	best := 0
	for i, weight := range b.weights {
		remaining -= weight
		if remaining < 0 {
			best = i
			break
		}
	}
	order := orderStartingAt(len(b.endpoints), best)
	if len(order) > 2 {
		tail := order[1:]
		b.rng.Shuffle(len(tail), func(i, j int) {
			tail[i], tail[j] = tail[j], tail[i]
		})
	}
	b.mu.Unlock()
	return order
}

func endpointWeights(endpoints []config.EndpointConfig) ([]int, int) {
	weights := make([]int, len(endpoints))
	total := 0
	for i, endpoint := range endpoints {
		weight := endpoint.Weight
		if weight <= 0 {
			weight = 1
		}
		weights[i] = weight
		total += weight
	}
	return weights, total
}

func orderStartingAt(length int, start int) []int {
	order := make([]int, 0, length)
	if length == 0 {
		return order
	}
	for i := range length {
		order = append(order, (start+i)%length)
	}
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
