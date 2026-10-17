// Package health 实现推理服务编排层的健康检查模块（增量模块 #2 之一）。
//
// 定期 ping 下游 vLLM/SGLang 实例的 /health 端点，维护实例健康状态。
// 设计要点：
//   - 每个实例一个检查周期内并发 ping，避免串行阻塞
//   - 连续失败 N 次（默认 3）才标记不健康，避免单次抖动误判
//   - 不健康实例继续 ping，恢复后自动标回健康
//   - 状态用 RWMutex 保护，供 LoadBalancer 并发读取
package health

import (
	"net/http"
	"sync"
	"time"

	"github.com/sakura5215/InferGateway/common/logger"
)

// Instance 表示一个下游推理服务实例（如 vLLM）。
type Instance struct {
	ID     int
	URL    string // 如 http://localhost:8000
	Weight int    // 负载权重，越大被选中概率越高

	healthy   bool
	LastCheck time.Time
	FailCount int // 连续失败次数
	mu        sync.RWMutex
}

// IsHealthy 并发安全地返回实例健康状态。
func (i *Instance) IsHealthy() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.healthy
}

// HealthChecker 定期检查所有实例的 /health 端点。
type HealthChecker struct {
	instances map[int]*Instance
	interval  time.Duration // 检查间隔，默认 10s
	threshold int           // 连续失败 N 次标记不健康，默认 3
	client    *http.Client
	stop      chan struct{}
}

// NewHealthChecker 创建检查器，使用默认参数（10s 间隔，3 次阈值，5s 超时）。
func NewHealthChecker() *HealthChecker {
	return &HealthChecker{
		instances: make(map[int]*Instance),
		interval:  10 * time.Second,
		threshold: 3,
		client:    &http.Client{Timeout: 5 * time.Second},
		stop:      make(chan struct{}),
	}
}

// AddInstance 注册一个推理实例。初始状态为健康。
func (hc *HealthChecker) AddInstance(id int, url string, weight int) {
	hc.instances[id] = &Instance{
		ID:      id,
		URL:     url,
		Weight:  weight,
		healthy: true,
	}
}

// Start 启动后台健康检查 goroutine，周期性 ping 所有实例。
func (hc *HealthChecker) Start() {
	go func() {
		ticker := time.NewTicker(hc.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				hc.checkAll()
			case <-hc.stop:
				return
			}
		}
	}()
	logger.SysLog("health checker started")
}

// Stop 停止健康检查 goroutine。
func (hc *HealthChecker) Stop() {
	close(hc.stop)
}

// checkAll 并发检查所有实例。
func (hc *HealthChecker) checkAll() {
	var wg sync.WaitGroup
	for _, inst := range hc.instances {
		wg.Add(1)
		go func(i *Instance) {
			defer wg.Done()
			hc.check(i)
		}(inst)
	}
	wg.Wait()
}

// check ping 单个实例的 /health 端点。
// vLLM 的 /health：HTTP 200=就绪，503=模型加载中，连接失败=宕机。
func (hc *HealthChecker) check(inst *Instance) {
	resp, err := hc.client.Get(inst.URL + "/health")
	if err != nil {
		inst.mu.Lock()
		inst.FailCount++
		if inst.FailCount >= hc.threshold {
			inst.healthy = false
		}
		inst.LastCheck = time.Now()
		inst.mu.Unlock()
		logger.SysError("health check failed for " + inst.URL + ": " + err.Error())
		return
	}
	defer resp.Body.Close()

	inst.mu.Lock()
	defer inst.mu.Unlock()
	inst.LastCheck = time.Now()
	if resp.StatusCode == http.StatusOK {
		inst.FailCount = 0
		inst.healthy = true
	} else {
		inst.FailCount++
		if inst.FailCount >= hc.threshold {
			inst.healthy = false
		}
	}
}

// Healthy 返回所有当前健康的实例，供 LoadBalancer 选实例用。
func (hc *HealthChecker) Healthy() []*Instance {
	result := make([]*Instance, 0)
	for _, inst := range hc.instances {
		if inst.IsHealthy() {
			result = append(result, inst)
		}
	}
	return result
}

// MarkUnhealthy 手动标记实例不健康。Failover 时调用，加速剔除故障实例。
func (hc *HealthChecker) MarkUnhealthy(id int) {
	if inst, ok := hc.instances[id]; ok {
		inst.mu.Lock()
		inst.healthy = false
		inst.FailCount = hc.threshold
		inst.mu.Unlock()
	}
}

// All 返回所有实例（含不健康的），供 ListChannels 展示状态。
func (hc *HealthChecker) All() []*Instance {
	result := make([]*Instance, 0, len(hc.instances))
	for _, inst := range hc.instances {
		result = append(result, inst)
	}
	return result
}

// Get 返回指定 ID 的实例。
func (hc *HealthChecker) Get(id int) *Instance {
	return hc.instances[id]
}
