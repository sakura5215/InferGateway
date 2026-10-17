// Package infergateway 是推理服务编排层的统一入口（增量模块 #2 的编排单例）。
//
// 负责管理 vLLM/SGLang 推理实例池：启动时从环境变量加载实例列表，
// 启动 HealthChecker 后台检查，暴露全局 LB 供 gRPC handler 调用选实例。
//
// 实例配置格式（环境变量 INFERGATEWAY_INSTANCES）：
//
//	INFERGATEWAY_INSTANCES="http://localhost:8000,1;http://localhost:8001,1"
//
// 每项用分号分隔，每项为 url,weight。weight 越大被选中概率越高。
package infergateway

import (
	"os"
	"strconv"
	"strings"

	"github.com/sakura5215/InferGateway/balancer"
	"github.com/sakura5215/InferGateway/common/logger"
	"github.com/sakura5215/InferGateway/health"
)

// Checker 全局健康检查器，main.go 启动时初始化。
var Checker *health.HealthChecker

// LB 全局负载均衡器，gRPC handler 通过它选实例。
var LB *balancer.LoadBalancer

// InitOrchestrator 初始化编排层：加载实例、启动健康检查、创建负载均衡器。
// 在 main.go 里 InitDB/Redis 之后调用。
func InitOrchestrator() {
	Checker = health.NewHealthChecker()

	// 从环境变量加载实例列表，默认两个本地 vLLM 实例
	instancesStr := os.Getenv("INFERGATEWAY_INSTANCES")
	if instancesStr == "" {
		instancesStr = "http://localhost:8000,1;http://localhost:8001,1"
	}

	id := 1
	for _, entry := range strings.Split(instancesStr, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ",")
		url := strings.TrimSpace(parts[0])
		weight := 1
		if len(parts) > 1 {
			if w, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && w > 0 {
				weight = w
			}
		}
		Checker.AddInstance(id, url, weight)
		logger.SysLog("registered inference instance #" + strconv.Itoa(id) + ": " + url + " weight=" + strconv.Itoa(weight))
		id++
	}

	Checker.Start()
	LB = balancer.NewLoadBalancer(Checker)
	logger.SysLog("orchestrator initialized")
}
