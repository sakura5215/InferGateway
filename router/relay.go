package router

// router/relay.go — LLM 推理转发路由组（InferGateway 核心链路）
//
// 职责：
//   注册 OpenAI 兼容的 /v1/* 路由组，所有 LLM 推理请求走这里。
//   请求经过三个 middleware 后落到 controller.Relay：
//     1. RelayPanicRecover — panic 兜底，防止单个请求崩整个进程
//     2. TokenAuth         — 解析 Authorization: Bearer sk-xxx，校验 token 有效性
//                            塞 ctxkey: Id/TokenId/TokenName/RequestModel 等
//     3. Distribute         — 按 token 的 group 选一个健康渠道（channel）
//                            塞 ctxkey: Channel/SpecificChannelId/ChannelType 等
//                            并覆写 Authorization 为渠道的 key（不是用户 token）
//
// 路由组结构：
//   /v1/models                — ListModels/RetrieveModel，只挂 TokenAuth（不选渠道）
//   /v1/completions           — 老 OpenAI completion（已被 chat/completions 取代但保留）
//   /v1/chat/completions      — ★ 核心入口，ChatCompletion
//   /v1/edits                 — OpenAI edits API（gpt-3.5 文本编辑）
//   /v1/images/generations    — DALL·E 文生图
//   /v1/embeddings            — 文本向量化
//   /v1/audio/transcriptions  — Whisper 语音转文字
//   /v1/audio/translations    — Whisper 翻译
//   /v1/audio/speech         — TTS 语音合成
//   /v1/moderations          — 内容审核
//   /v1/oneapi/proxy/:id/*   — one-api 私有代理接口（直连指定 channel，跳过 Distribute）
//
//   /v1/files /v1/fine_tuning /v1/assistants /v1/threads — 注册但都返 RelayNotImplemented
//   这些是 OpenAI 后续推的 Assistant API，one-api 不实现，留着兼容客户端调用路径
//
// 全局 middleware（router.Use）：
//   - CORS                    — 允许跨域（前端独立部署在 CDN 时必需）
//   - GzipDecodeMiddleware    — 解压 Content-Encoding: gzip 的请求体（vLLM 客户端偶尔用）

import (
	"github.com/sakura5215/InferGateway/controller"
	"github.com/sakura5215/InferGateway/middleware"

	"github.com/gin-gonic/gin"
)

// SetRelayRouter 注册 OpenAI 兼容的 /v1/* 转发路由组
//   被路由顺序：RelayPanicRecover → TokenAuth → Distribute → controller.Relay
//   所有未实现的 OpenAI 接口（files/fine_tuning/assistants/threads）返 RelayNotImplemented
func SetRelayRouter(router *gin.Engine) {
	// 全局 middleware：CORS + Gzip 解码
	router.Use(middleware.CORS())
	router.Use(middleware.GzipDecodeMiddleware())
	// https://platform.openai.com/docs/api-reference/introduction
	// /v1/models 路由组：只挂 TokenAuth（看模型列表不需要选渠道，也不计费）
	modelsRouter := router.Group("/v1/models")
	modelsRouter.Use(middleware.TokenAuth())
	{
		modelsRouter.GET("", controller.ListModels)         // GET /v1/models — 返回所有可用模型列表
		modelsRouter.GET("/:model", controller.RetrieveModel) // GET /v1/models/{model} — 返回单个模型详情
	}
	// /v1/* 推理转发路由组：三个 middleware 全挂
	relayV1Router := router.Group("/v1")
	relayV1Router.Use(middleware.RelayPanicRecover(), middleware.TokenAuth(), middleware.Distribute())
	{
		// ★ 核心 LLM 推理接口（生产 99% 流量走这里）
		relayV1Router.Any("/oneapi/proxy/:channelid/*target", controller.Relay) // one-api 私有代理：跳过渠道分发，直连指定 channel
		relayV1Router.POST("/completions", controller.Relay)                     // 老 completion API（保留兼容）
		relayV1Router.POST("/chat/completions", controller.Relay)                // ★ ChatCompletion — 主入口，InferGateway gRPC 层也复用这条链路
		relayV1Router.POST("/edits", controller.Relay)                           // OpenAI edits API（gpt-3.5 文本编辑）
		relayV1Router.POST("/images/generations", controller.Relay)              // DALL·E 文生图
		relayV1Router.POST("/images/edits", controller.RelayNotImplemented)     // 图片编辑（未实现）
		relayV1Router.POST("/images/variations", controller.RelayNotImplemented) // 图片变体（未实现）
		relayV1Router.POST("/embeddings", controller.Relay)                     // 文本向量化
		relayV1Router.POST("/engines/:model/embeddings", controller.Relay)       // 老 embeddings 路径（兼容 Azure 风格）
		relayV1Router.POST("/audio/transcriptions", controller.Relay)           // Whisper 语音转文字
		relayV1Router.POST("/audio/translations", controller.Relay)             // Whisper 翻译
		relayV1Router.POST("/audio/speech", controller.Relay)                    // TTS 语音合成
		// 以下都是 OpenAI 后续推的 API，one-api 注册路由但不实现（返 501）
		relayV1Router.GET("/files", controller.RelayNotImplemented)
		relayV1Router.POST("/files", controller.RelayNotImplemented)
		relayV1Router.DELETE("/files/:id", controller.RelayNotImplemented)
		relayV1Router.GET("/files/:id", controller.RelayNotImplemented)
		relayV1Router.GET("/files/:id/content", controller.RelayNotImplemented)
		relayV1Router.POST("/fine_tuning/jobs", controller.RelayNotImplemented)
		relayV1Router.GET("/fine_tuning/jobs", controller.RelayNotImplemented)
		relayV1Router.GET("/fine_tuning/jobs/:id", controller.RelayNotImplemented)
		relayV1Router.POST("/fine_tuning/jobs/:id/cancel", controller.RelayNotImplemented)
		relayV1Router.GET("/fine_tuning/jobs/:id/events", controller.RelayNotImplemented)
		relayV1Router.DELETE("/models/:model", controller.RelayNotImplemented)
		relayV1Router.POST("/moderations", controller.Relay)
		// Assistants API（OpenAI 2023 推出）— 全部未实现
		relayV1Router.POST("/assistants", controller.RelayNotImplemented)
		relayV1Router.GET("/assistants/:id", controller.RelayNotImplemented)
		relayV1Router.POST("/assistants/:id", controller.RelayNotImplemented)
		relayV1Router.DELETE("/assistants/:id", controller.RelayNotImplemented)
		relayV1Router.GET("/assistants", controller.RelayNotImplemented)
		relayV1Router.POST("/assistants/:id/files", controller.RelayNotImplemented)
		relayV1Router.GET("/assistants/:id/files/:fileId", controller.RelayNotImplemented)
		relayV1Router.DELETE("/assistants/:id/files/:fileId", controller.RelayNotImplemented)
		relayV1Router.GET("/assistants/:id/files", controller.RelayNotImplemented)
		// Threads API（Assistants 的执行容器）— 全部未实现
		relayV1Router.POST("/threads", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id", controller.RelayNotImplemented)
		relayV1Router.DELETE("/threads/:id", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/messages", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/messages/:messageId", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/messages/:messageId", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/messages/:messageId/files/:filesId", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/messages/:messageId/files", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/runs", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/runs/:runsId", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/runs/:runsId", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/runs", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/runs/:runsId/submit_tool_outputs", controller.RelayNotImplemented)
		relayV1Router.POST("/threads/:id/runs/:runsId/cancel", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/runs/:runsId/steps/:stepId", controller.RelayNotImplemented)
		relayV1Router.GET("/threads/:id/runs/:runsId/steps", controller.RelayNotImplemented)
	}
}
