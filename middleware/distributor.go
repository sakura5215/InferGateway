package middleware

// middleware/distributor.go — 渠道分发 middleware（InferGateway 核心链路之一）
//
// 职责：
//   在 TokenAuth 之后、controller.Relay 之前执行。
//   根据用户的 group + 请求的 model，从数据库挑一个健康可用的渠道（channel），
//   把渠道上下文塞到 gin.Context，让 relay 链能拿到下游地址和 key。
//
// 选渠道逻辑（两条路径）：
//   路径 A：用户指定了 SpecificChannelId（admin 用 sk-xxx-{channelId}）
//     → 直接查该 channel，校验启用状态
//   路径 B：未指定（普通用户走这里）
//     → CacheGetRandomSatisfiedChannel(group, model) 随机选一个匹配的 channel
//     匹配条件：channel.models 含 requestModel + channel.group 含 userGroup + 启用
//     缓存：从内存 channel ability 缓存查，不直接打 DB
//
// SetupContextForSelectedChannel 塞的关键 ctxkey（relay 链要用）：
//   Channel / ChannelId / ChannelName  — 渠道身份
//   ModelMapping                        — 模型名映射（如 gpt-4 → gpt-4-0613）
//   BaseURL                              — 下游地址（vLLM 是 http://localhost:8000）
//   Authorization                        — 覆写为渠道的 key（不是用户的 token）
//   SystemPrompt                         — 渠道系统提示词（可选）
//   Config                              — 渠道额外配置（API version、plugin 等）
//
// 注意（InferGateway 增量视角）：
//   InferGateway 编排层（infergateway/orchestrator.go）是另一套独立的"选实例"逻辑，
//   不走这里。Distribute 是存量的"选 channel"逻辑（用于 OpenAI/Claude 渠道）。
//   两者并行：渠道分发选 channel，编排层选 vLLM 实例。
//   grpc/server.go 在调 RelayTextHelper 前需要手动构造 ctxkey.Channel 等，
//   绕过 Distribute 这一步（因为 gRPC handler 是 mock gin.Context）。

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/sakura5215/InferGateway/common/ctxkey"
	"github.com/sakura5215/InferGateway/common/logger"
	"github.com/sakura5215/InferGateway/model"
	"github.com/sakura5215/InferGateway/relay/channeltype"
)

// ModelRequest 从请求 body 解析 model 字段（用于 TokenAuth 提取 requestModel）
type ModelRequest struct {
	Model string `json:"model" form:"model"`
}

// Distribute 渠道分发 middleware
//   位置：TokenAuth 之后、controller.Relay 之前
//   职责：选 channel + 塞 channel 上下文
//   被路由：router/relay.go 的 /v1/* 路由组都挂
func Distribute() func(c *gin.Context) {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		// 1. 取用户分组（user 表的 group 字段，决定能用哪些 channel）
		userId := c.GetInt(ctxkey.Id)
		userGroup, _ := model.CacheGetUserGroup(userId)
		c.Set(ctxkey.Group, userGroup)
		var requestModel string
		var channel *model.Channel
		// 2. 路径 A：admin 指定了 SpecificChannelId（sk-xxx-{channelId} 或 path 参数）
		channelId, ok := c.Get(ctxkey.SpecificChannelId)
		if ok {
			id, err := strconv.Atoi(channelId.(string))
			if err != nil {
				abortWithMessage(c, http.StatusBadRequest, "无效的渠道 Id")
				return
			}
			channel, err = model.GetChannelById(id, true)
			if err != nil {
				abortWithMessage(c, http.StatusBadRequest, "无效的渠道 Id")
				return
			}
			// 校验渠道是否启用
			if channel.Status != model.ChannelStatusEnabled {
				abortWithMessage(c, http.StatusForbidden, "该渠道已被禁用")
				return
			}
		} else {
			// 路径 B：未指定渠道，按 group + model 随机选一个匹配的
			// CacheGetRandomSatisfiedChannel：内存缓存查询，不打 DB
			// 匹配条件：channel.models 含 requestModel + channel.group 含 userGroup + enabled
			requestModel = c.GetString(ctxkey.RequestModel)
			var err error
			channel, err = model.CacheGetRandomSatisfiedChannel(userGroup, requestModel, false)
			if err != nil {
				message := fmt.Sprintf("当前分组 %s 下对于模型 %s 无可用渠道", userGroup, requestModel)
				if channel != nil {
					// channel 不为 nil 但 err 不为 nil 表示 DB 一致性破坏（罕见）
					logger.SysError(fmt.Sprintf("渠道不存在：%d", channel.Id))
					message = "数据库一致性已被破坏，请联系管理员"
				}
				abortWithMessage(c, http.StatusServiceUnavailable, message)
				return
			}
		}
		logger.Debugf(ctx, "user id %d, user group: %s, request model: %s, using channel #%d", userId, userGroup, requestModel, channel.Id)
		// 3. 塞 channel 上下文（下面 SetupContextForSelectedChannel）
		SetupContextForSelectedChannel(c, channel, requestModel)
		c.Next()
	}
}

// SetupContextForSelectedChannel 把选中 channel 的信息塞 gin.Context
//   relay 链（RelayTextHelper）从这里取：BaseURL、Channel、ModelMapping、Authorization 等
//   关键：覆写 Authorization 为 channel.Key（不是用户的 token，用户 token 只用于鉴权）
func SetupContextForSelectedChannel(c *gin.Context, channel *model.Channel, modelName string) {
	c.Set(ctxkey.Channel, channel.Type)
	c.Set(ctxkey.ChannelId, channel.Id)
	c.Set(ctxkey.ChannelName, channel.Name)
	// 渠道级 system prompt（管理员在渠道配置里设）
	if channel.SystemPrompt != nil && *channel.SystemPrompt != "" {
		c.Set(ctxkey.SystemPrompt, *channel.SystemPrompt)
	}
	// 模型名映射（如 gpt-4 → gpt-4-0613，channel 级配置）
	c.Set(ctxkey.ModelMapping, channel.GetModelMapping())
	c.Set(ctxkey.OriginalModel, modelName) // 保留原始 model 名，retry 时用
	// ★ 覆写 Authorization 为渠道 key（不是用户 token）
	// relay 链后面 DoRequest 时会用这个 header 转发到下游
	c.Request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", channel.Key))
	c.Set(ctxkey.BaseURL, channel.GetBaseURL())
	cfg, _ := channel.LoadConfig()
	// 向后兼容：旧版 channel 用 Other 字段存额外配置，新版用 Config（JSON）
	// 这里把 Other 转成 cfg 字段
	if channel.Other != nil {
		switch channel.Type {
		case channeltype.Azure:
			if cfg.APIVersion == "" {
				cfg.APIVersion = *channel.Other
			}
		case channeltype.Xunfei:
			if cfg.APIVersion == "" {
				cfg.APIVersion = *channel.Other
			}
		case channeltype.Gemini:
			if cfg.APIVersion == "" {
				cfg.APIVersion = *channel.Other
			}
		case channeltype.AIProxyLibrary:
			if cfg.LibraryID == "" {
				cfg.LibraryID = *channel.Other
			}
		case channeltype.Ali:
			if cfg.Plugin == "" {
				cfg.Plugin = *channel.Other
			}
		}
	}
	c.Set(ctxkey.Config, cfg)
}
