package router

// router/dashboard.go — OpenAI 计费仪表盘 API
//
// 职责：
//   兼容 OpenAI 官方的 billing 接口（dashboard.billing.subscription / usage），
//   让使用 OpenAI SDK 的客户端能查 quota 余额和用量，不用改代码。
//
//   OpenAI 早期有这个 API 后来废弃了，但很多老 SDK/第三方工具还在调，
//   实现一份兼容版，返用户的剩余 quota（按 $ 折算）。
//
// 鉴权：CORS + Gzip + GlobalAPIRateLimit + TokenAuth
//   注意：用 sk-xxx token 鉴权（不是 cookie session），因为客户端通常没登录态
//
// 路由：
//   GET /dashboard/billing/subscription       — 返 quota（按 1$ = 500000 quota 换算）
//   GET /v1/dashboard/billing/subscription   — 同上，加 /v1 前缀兼容
//   GET /dashboard/billing/usage             — 返最近用量
//   GET /v1/dashboard/billing/usage           — 同上

import (
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/sakura5215/InferGateway/controller"
	"github.com/sakura5215/InferGateway/middleware"
)

// SetDashboardRouter 注册 /dashboard 和 /v1/dashboard 路由组
//   全部走 TokenAuth（sk-xxx 鉴权），不需要 Distribute（不涉及推理转发）
func SetDashboardRouter(router *gin.Engine) {
	apiRouter := router.Group("/")
	apiRouter.Use(middleware.CORS())
	apiRouter.Use(gzip.Gzip(gzip.DefaultCompression))
	apiRouter.Use(middleware.GlobalAPIRateLimit())
	apiRouter.Use(middleware.TokenAuth())
	{
		// billing/subscription — OpenAI 老版本用来查订阅状态
		// 把用户的剩余 quota 折算成 $ 返回（hardcode $20/月）
		apiRouter.GET("/dashboard/billing/subscription", controller.GetSubscription)
		apiRouter.GET("/v1/dashboard/billing/subscription", controller.GetSubscription) // /v1 前缀兼容
		// billing/usage — OpenAI 老版本用来查每日用量
		// 返最近 100 天的 token 用量
		apiRouter.GET("/dashboard/billing/usage", controller.GetUsage)
		apiRouter.GET("/v1/dashboard/billing/usage", controller.GetUsage) // /v1 前缀兼容
	}
}
