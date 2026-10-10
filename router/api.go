package router

// router/api.go — 管理面 API 路由组（/api/*）
//
// 职责：
//   注册所有后台管理 + 用户自服务 API：
//   - 状态/公告/about（公开）
//   - 用户注册/登录/登出/OAuth（Github/微信/飞书/Email/OIDC）
//   - 自服务（dashboard/令牌/可用模型/充值/邀请码）
//   - 管理员管理用户（CRUD + 搜索 + 管理）
//   - 渠道管理（CRUD + 测试 + 余额查询）
//   - 令牌管理（CRUD + 搜索）
//   - 兑换码管理（CRUD + 搜索）
//   - 日志（admin 看全量 + user 看自己）
//   - 选项配置（仅 root）
//   - 分组（admin）
//
// 鉴权分层：
//   GlobalAPIRateLimit — 全局 IP 维度限流（防爆破）
//   UserAuth           — 已登录用户（基于 cookie session）
//   AdminAuth          — 管理员
//   RootAuth           — root 账户（只有一个，环境变量初始化的 root/123456）
//
// 速率限制：
//   CriticalRateLimit — 注册/登录/重置密码/OAuth 等敏感操作额外限流

import (
	"github.com/sakura5215/InferGateway/controller"
	"github.com/sakura5215/InferGateway/controller/auth"
	"github.com/sakura5215/InferGateway/middleware"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

// SetApiRouter 注册 /api/* 管理面路由组
//   全局 middleware：Gzip 压缩 + GlobalAPIRateLimit（IP 维度限流）
func SetApiRouter(router *gin.Engine) {
	apiRouter := router.Group("/api")
	apiRouter.Use(gzip.Gzip(gzip.DefaultCompression))
	apiRouter.Use(middleware.GlobalAPIRateLimit())
	{
		// === 公开接口（无需登录） ===
		apiRouter.GET("/status", controller.GetStatus)                  // 服务状态（健康检查 + 版本号）
		apiRouter.GET("/models", middleware.UserAuth(), controller.DashboardListModels) // 模型列表（需登录）
		apiRouter.GET("/notice", controller.GetNotice)                 // 公告
		apiRouter.GET("/about", controller.GetAbout)                    // about 页内容
		apiRouter.GET("/home_page_content", controller.GetHomePageContent) // 首页内容
		apiRouter.GET("/verification", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), controller.SendEmailVerification) // 发送邮箱验证码
		apiRouter.GET("/reset_password", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), controller.SendPasswordResetEmail) // 发送重置密码邮件
		apiRouter.POST("/user/reset", middleware.CriticalRateLimit(), controller.ResetPassword) // 重置密码
		// OAuth 第三方登录（都加 CriticalRateLimit 防爆破）
		apiRouter.GET("/oauth/github", middleware.CriticalRateLimit(), auth.GitHubOAuth)
		apiRouter.GET("/oauth/oidc", middleware.CriticalRateLimit(), auth.OidcAuth)
		apiRouter.GET("/oauth/lark", middleware.CriticalRateLimit(), auth.LarkOAuth)
		apiRouter.GET("/oauth/state", middleware.CriticalRateLimit(), auth.GenerateOAuthCode) // OAuth state 防 CSRF
		apiRouter.GET("/oauth/wechat", middleware.CriticalRateLimit(), auth.WeChatAuth)
		apiRouter.GET("/oauth/wechat/bind", middleware.CriticalRateLimit(), middleware.UserAuth(), auth.WeChatBind) // 已登录用户绑定微信
		apiRouter.GET("/oauth/email/bind", middleware.CriticalRateLimit(), middleware.UserAuth(), controller.EmailBind) // 已登录用户绑定邮箱
		apiRouter.POST("/topup", middleware.AdminAuth(), controller.AdminTopUp) // 管理员充值

		// === 用户自服务（/api/user） ===
		userRoute := apiRouter.Group("/user")
		{
			userRoute.POST("/register", middleware.CriticalRateLimit(), middleware.TurnstileCheck(), controller.Register) // 注册
			userRoute.POST("/login", middleware.CriticalRateLimit(), controller.Login) // 登录（返 cookie session）
			userRoute.GET("/logout", controller.Logout) // 登出

			// 自服务（需 UserAuth）：只能操作自己的资源
			selfRoute := userRoute.Group("/")
			selfRoute.Use(middleware.UserAuth())
			{
				selfRoute.GET("/dashboard", controller.GetUserDashboard)       // 个人仪表盘
				selfRoute.GET("/self", controller.GetSelf)                      // 获取自己信息
				selfRoute.PUT("/self", controller.UpdateSelf)                   // 修改自己信息
				selfRoute.DELETE("/self", controller.DeleteSelf)                // 注销账号
				selfRoute.GET("/token", controller.GenerateAccessToken)         // 生成 access_token（cookie 鉴权 → API token）
				selfRoute.GET("/aff", controller.GetAffCode)                    // 获取邀请码
				selfRoute.POST("/topup", controller.TopUp)                      // 充值（兑换码 or 支付）
				selfRoute.GET("/available_models", controller.GetUserAvailableModels) // 自己可用的模型列表（按分组过滤）
			}

			// 用户管理（需 AdminAuth）：管理员操作所有用户
			adminRoute := userRoute.Group("/")
			adminRoute.Use(middleware.AdminAuth())
			{
				adminRoute.GET("/", controller.GetAllUsers)     // 列所有用户
				adminRoute.GET("/search", controller.SearchUsers) // 搜索用户
				adminRoute.GET("/:id", controller.GetUser)        // 查单个用户
				adminRoute.POST("/", controller.CreateUser)       // 创建用户
				adminRoute.POST("/manage", controller.ManageUser) // 管理操作（封禁/解封/重置密码）
				adminRoute.PUT("/", controller.UpdateUser)         // 修改用户
				adminRoute.DELETE("/:id", controller.DeleteUser)   // 删除用户
			}
		}
		// === 选项配置（仅 root）===
		optionRoute := apiRouter.Group("/option")
		optionRoute.Use(middleware.RootAuth())
		{
			optionRoute.GET("/", controller.GetOptions)    // 获取所有系统配置
			optionRoute.PUT("/", controller.UpdateOption)  // 修改系统配置（model ratio 等）
		}
		// === 渠道管理（需 AdminAuth）===
		// channel = 下游 LLM 服务（OpenAI/Claude/vLLM 等），管理员配置 base_url/key/models
		channelRoute := apiRouter.Group("/channel")
		channelRoute.Use(middleware.AdminAuth())
		{
			channelRoute.GET("/", controller.GetAllChannels)                          // 列所有渠道
			channelRoute.GET("/search", controller.SearchChannels)                   // 搜索渠道
			channelRoute.GET("/models", controller.ListAllModels)                   // 列所有渠道的模型
			channelRoute.GET("/:id", controller.GetChannel)                          // 查单个渠道
			channelRoute.GET("/test", controller.TestChannels)                      // 测试所有渠道连通性
			channelRoute.GET("/test/:id", controller.TestChannel)                    // 测试单个渠道
			channelRoute.GET("/update_balance", controller.UpdateAllChannelsBalance)  // 刷新所有渠道余额
			channelRoute.GET("/update_balance/:id", controller.UpdateChannelBalance) // 刷新单个渠道余额
			channelRoute.POST("/", controller.AddChannel)                            // 新增渠道
			channelRoute.PUT("/", controller.UpdateChannel)                          // 修改渠道
			channelRoute.DELETE("/disabled", controller.DeleteDisabledChannel)      // 删除所有禁用渠道
			channelRoute.DELETE("/:id", controller.DeleteChannel)                   // 删除单个渠道
		}
		// === 令牌管理（需 UserAuth）===
		// token = 用户发放给客户端的访问凭证（sk-xxx），按 quota 计费
		tokenRoute := apiRouter.Group("/token")
		tokenRoute.Use(middleware.UserAuth())
		{
			tokenRoute.GET("/", controller.GetAllTokens)      // 列自己所有令牌
			tokenRoute.GET("/search", controller.SearchTokens) // 搜索令牌
			tokenRoute.GET("/:id", controller.GetToken)         // 查单个令牌
			tokenRoute.POST("/", controller.AddToken)           // 新建令牌（返 sk-xxx）
			tokenRoute.PUT("/", controller.UpdateToken)          // 修改令牌
			tokenRoute.DELETE("/:id", controller.DeleteToken)   // 删除令牌
		}
		// === 兑换码管理（需 AdminAuth）===
		// redemption = 兑换码，用户输入后增加 quota
		redemptionRoute := apiRouter.Group("/redemption")
		redemptionRoute.Use(middleware.AdminAuth())
		{
			redemptionRoute.GET("/", controller.GetAllRedemptions)
			redemptionRoute.GET("/search", controller.SearchRedemptions)
			redemptionRoute.GET("/:id", controller.GetRedemption)
			redemptionRoute.POST("/", controller.AddRedemption)
			redemptionRoute.PUT("/", controller.UpdateRedemption)
			redemptionRoute.DELETE("/:id", controller.DeleteRedemption)
		}
		// === 日志（admin 看全量，user 看自己）===
		// log = 每次 LLM 请求的计费日志（token 用量、模型、渠道、用户、时间）
		logRoute := apiRouter.Group("/log")
		logRoute.GET("/", middleware.AdminAuth(), controller.GetAllLogs)              // admin: 列所有日志
		logRoute.DELETE("/", middleware.AdminAuth(), controller.DeleteHistoryLogs)   // admin: 删除历史日志
		logRoute.GET("/stat", middleware.AdminAuth(), controller.GetLogsStat)        // admin: 统计（按日 token 用量）
		logRoute.GET("/self/stat", middleware.UserAuth(), controller.GetLogsSelfStat) // user: 自己的统计
		logRoute.GET("/search", middleware.AdminAuth(), controller.SearchAllLogs)     // admin: 搜索全量日志
		logRoute.GET("/self", middleware.UserAuth(), controller.GetUserLogs)         // user: 列自己日志
		logRoute.GET("/self/search", middleware.UserAuth(), controller.SearchUserLogs) // user: 搜索自己日志
		// === 分组（需 AdminAuth）===
		// group = 用户分组，不同分组可用不同渠道（VIP 用户分组走高质量渠道）
		groupRoute := apiRouter.Group("/group")
		groupRoute.Use(middleware.AdminAuth())
		{
			groupRoute.GET("/", controller.GetGroups) // 列所有分组
		}
	}
}
