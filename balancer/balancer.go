// Package balancer 实现推理服务编排层的负载均衡与故障切换（增量模块 #2 之二）。
//
// 从 HealthChecker 取健康实例列表，按权重加权轮询选一个；
// 请求失败时调用 Failover 标记故障实例不健康，并返回下一个健康实例。
package balancer

import (
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/sakura5215/InferGateway/health"
)

// LoadBalancer 加权轮询负载均衡器。
type LoadBalancer struct {
	checker *health.HealthChecker
	mu      sync.Mutex
	rng     *rand.Rand
}

// NewLoadBalancer 创建负载均衡器，绑定一个 HealthChecker。
func NewLoadBalancer(checker *health.HealthChecker) *LoadBalancer {
	return &LoadBalancer{
		checker: checker,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Pick 加权选一个健康实例。权重越高被选中概率越大。
// 无健康实例时返回 error。
func (lb *LoadBalancer) Pick() (*health.Instance, error) {
	healthy := lb.checker.Healthy()
	if len(healthy) == 0 {
		return nil, errors.New("no healthy instance available")
	}
	totalWeight := 0
	for _, inst := range healthy {
		w := inst.Weight
		if w <= 0 {
			w = 1
		}
		totalWeight += w
	}
	lb.mu.Lock()
	r := lb.rng.Intn(totalWeight)
	lb.mu.Unlock()
	for _, inst := range healthy {
		w := inst.Weight
		if w <= 0 {
			w = 1
		}
		r -= w
		if r < 0 {
			return inst, nil
		}
	}
	return healthy[0], nil
}

// Failover 标记失败实例不健康，并返回下一个健康实例。
// 调用方拿到新实例后重试请求。
func (lb *LoadBalancer) Failover(failed *health.Instance) (*health.Instance, error) {
	lb.checker.MarkUnhealthy(failed.ID)
	return lb.Pick()
}
