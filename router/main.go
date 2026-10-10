package router

// router/main.go — 路由总装配入口
//
// 职责：
//   SetRouter 是 main.go 启动 Gin engine 后调用的总装配函数。
//   它按顺序挂载四类路由组：
//     1. /api/*              — 管理面 API（用户/渠道/令牌/日志/兑换码/分组/option）
//     2. /dashboard/*       — 计费仪表盘 API（OpenAI 兼容的 billing/subscription/usage）
//     3. /v1/*              — LLM 推理转发 API（OpenAI 兼容的 chat/completions、embeddings、images 等）
//     4. /                  — 前端静态资源（embed 嵌入 web/build/ 或重定向到 FRONTEND_BASE_URL）
//
//   顺序敏感：/v1/* 必须在 / 之前注册，否则前端静态会优先匹配 /v1/xxx。
//
// 关键决策：
//   - FRONTEND_BASE_URL 不为空时，前端走外链重定向（不影响 /api /v1 路由匹配）
//   - IsMasterNode：集群部署时主节点才挂前端静态，从节点直接 301 重定向到主节点
//   - buildFS embed.FS：前端构建产物编译进二进制（web/build/*），启动不需要外部静态目录

import (
	"embed"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/sakura5215/InferGateway/common/config"
	"github.com/sakura5215/InferGateway/common/logger"
	"net/http"
	"os"
	"strings"
)

// SetRouter 总装配函数：挂载 /api、/dashboard、/v1、/ 四类路由组
//   参数：
//     router   — gin.Engine 实例（main.go 里 gin.New() 创建）
//     buildFS  — //go:embed web/build/* 嵌入的前端资源（main.go 传入）
func SetRouter(router *gin.Engine, buildFS embed.FS) {
	SetApiRouter(router)       // 1. 管理面 API（鉴权: GlobalAPIRateLimit + UserAuth/AdminAuth）
	SetDashboardRouter(router) // 2. OpenAI 兼容的 billing 接口（鉴权: TokenAuth）
	SetRelayRouter(router)     // 3. LLM 转发 API（鉴权: TokenAuth + Distribute 选渠道）— InferGateway 核心链路
	// 前端静态资源路由（可选，只在 IsMasterNode 且没有外链时挂）
	frontendBaseUrl := os.Getenv("FRONTEND_BASE_URL")
	if config.IsMasterNode && frontendBaseUrl != "" {
		// 主节点强制嵌入静态资源（不让用户配置 FRONTEND_BASE_URL 走外链）
		frontendBaseUrl = ""
		logger.SysLog("FRONTEND_BASE_URL is ignored on master node")
	}
	if frontendBaseUrl == "" {
		// 走 embed 嵌入：把 web/build/ 当静态目录服务
		SetWebRouter(router, buildFS)
	} else {
		// 走外链重定向：所有未匹配的路径 301 到 FRONTEND_BASE_URL + 原路径
		// 用途：前端部署在独立 CDN/Nginx 上，后端只管 API
		frontendBaseUrl = strings.TrimSuffix(frontendBaseUrl, "/")
		router.NoRoute(func(c *gin.Context) {
			c.Redirect(http.StatusMovedPermanently, fmt.Sprintf("%s%s", frontendBaseUrl, c.Request.RequestURI))
		})
	}
}
