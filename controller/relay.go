package controller

// controller/relay.go — HTTP 推理转发入口（InferGateway 核心链路 controller 层）
//
// 职责：
//   /v1/* 路由组所有请求的入口函数。请求经过 router middleware 后落到这里。
//   根据 URL path 判断 relayMode（chat/completions/images/audio/proxy），
//   调用 relay/controller 包对应 helper 函数完成转发，并处理失败重试。
//
// 重试机制（核心特性）：
//   1. 第一次失败后，根据 shouldRetry 判断是否重试（429/5xx 重试，4xx 不重试）
//   2. 重试次数来自 config.RetryTimes（默认 5）
//   3. 每次重试 CacheGetRandomSatisfiedChannel 换一个 channel（跳过上次失败的）
//   4. SpecificChannelId 模式不重试（admin 指定渠道失败就失败）
//
// 失败后处理：
//   processChannelRelayError — 异步 goroutine 记录错误日志
//   monitor.ShouldDisableChannel — 判断是否要禁用该 channel（连续失败次数达阈值）
//   monitor.Emit — 上报 channel 健康状态（前端 dashboard 显示）
//
// gRPC 入口（grpc/server.go ChatCompletion）：
//   不走 controller.Relay，直接调 relay/controller.RelayTextHelper。
//   绕过的原因：gRPC 没有 gin.Context 路由，路径无法判断 relayMode；
//   gRPC 只支持 chat/completions 一种模式，直接调 RelayTextHelper 即可。

// https://platform.openai.com/docs/api-reference/chat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/sakura5215/InferGateway/common"
	"github.com/sakura5215/InferGateway/common/config"
	"github.com/sakura5215/InferGateway/common/ctxkey"
	"github.com/sakura5215/InferGateway/common/helper"
	"github.com/sakura5215/InferGateway/common/logger"
	"github.com/sakura5215/InferGateway/middleware"
	dbmodel "github.com/sakura5215/InferGateway/model"
	"github.com/sakura5215/InferGateway/monitor"
	"github.com/sakura5215/InferGateway/relay/controller"
	"github.com/sakura5215/InferGateway/relay/model"
	"github.com/sakura5215/InferGateway/relay/relaymode"
)

// https://platform.openai.com/docs/api-reference/chat

// relayHelper 按 relayMode 分发到对应的 helper
//   ImagesGenerations → RelayImageHelper
//   AudioSpeech/Translation/Transcription → RelayAudioHelper
//   Proxy → RelayProxyHelper
//   其他（ChatCompletion/Completions/Embeddings/Moderations/Edits）→ RelayTextHelper
//   InferGateway gRPC 层只调用 RelayTextHelper（chat 模式）
func relayHelper(c *gin.Context, relayMode int) *model.ErrorWithStatusCode {
	var err *model.ErrorWithStatusCode
	switch relayMode {
	case relaymode.ImagesGenerations:
		err = controller.RelayImageHelper(c, relayMode)
	case relaymode.AudioSpeech:
		fallthrough
	case relaymode.AudioTranslation:
		fallthrough
	case relaymode.AudioTranscription:
		err = controller.RelayAudioHelper(c, relayMode)
	case relaymode.Proxy:
		err = controller.RelayProxyHelper(c, relayMode)
	default:
		err = controller.RelayTextHelper(c)
	}
	return err
}

// Relay HTTP 推理转发的入口函数
//   注册在 router/relay.go 的 /v1/* 路由组（chat/completions/embeddings/images/audio 等）
//   流程：
//     1. relaymode.GetByPath 根据 URL path 判断 relayMode
//     2. 调 relayHelper 转发（失败返 ErrorWithStatusCode）
//     3. 成功 → monitor.Emit 上报成功状态，return
//     4. 失败 → 异步 processChannelRelayError 记录 + 判断是否禁用 channel
//     5. shouldRetry 判断是否重试（429/5xx 重试，4xx 不重试）
//     6. 重试循环：每次换一个 channel（CacheGetRandomSatisfiedChannel），
//        跳过上次失败的 lastFailedChannelId
//     7. 全部重试失败 → 返 JSON 错误给客户端
func Relay(c *gin.Context) {
	ctx := c.Request.Context()
	relayMode := relaymode.GetByPath(c.Request.URL.Path)
	if config.DebugEnabled {
		requestBody, _ := common.GetRequestBody(c)
		logger.Debugf(ctx, "request body: %s", string(requestBody))
	}
	channelId := c.GetInt(ctxkey.ChannelId)
	userId := c.GetInt(ctxkey.Id)
	bizErr := relayHelper(c, relayMode)
	if bizErr == nil {
		monitor.Emit(channelId, true)
		return
	}
	lastFailedChannelId := channelId
	channelName := c.GetString(ctxkey.ChannelName)
	group := c.GetString(ctxkey.Group)
	originalModel := c.GetString(ctxkey.OriginalModel)
	go processChannelRelayError(ctx, userId, channelId, channelName, *bizErr)
	requestId := c.GetString(helper.RequestIdKey)
	retryTimes := config.RetryTimes
	if !shouldRetry(c, bizErr.StatusCode) {
		logger.Errorf(ctx, "relay error happen, status code is %d, won't retry in this case", bizErr.StatusCode)
		retryTimes = 0
	}
	for i := retryTimes; i > 0; i-- {
		channel, err := dbmodel.CacheGetRandomSatisfiedChannel(group, originalModel, i != retryTimes)
		if err != nil {
			logger.Errorf(ctx, "CacheGetRandomSatisfiedChannel failed: %+v", err)
			break
		}
		logger.Infof(ctx, "using channel #%d to retry (remain times %d)", channel.Id, i)
		if channel.Id == lastFailedChannelId {
			continue
		}
		middleware.SetupContextForSelectedChannel(c, channel, originalModel)
		requestBody, err := common.GetRequestBody(c)
		c.Request.Body = io.NopCloser(bytes.NewBuffer(requestBody))
		bizErr = relayHelper(c, relayMode)
		if bizErr == nil {
			return
		}
		channelId := c.GetInt(ctxkey.ChannelId)
		lastFailedChannelId = channelId
		channelName := c.GetString(ctxkey.ChannelName)
		go processChannelRelayError(ctx, userId, channelId, channelName, *bizErr)
	}
	if bizErr != nil {
		if bizErr.StatusCode == http.StatusTooManyRequests {
			bizErr.Error.Message = "当前分组上游负载已饱和，请稍后再试"
		}

		// BUG: bizErr is in race condition
		bizErr.Error.Message = helper.MessageWithRequestId(bizErr.Error.Message, requestId)
		c.JSON(bizErr.StatusCode, gin.H{
			"error": bizErr.Error,
		})
	}
}

// shouldRetry 判断是否应该重试
//   - SpecificChannelId 模式不重试（admin 指定渠道失败就失败）
//   - 429（限流）→ 重试
//   - 5xx（服务端错误）→ 重试
//   - 4xx（客户端错误，如 400 bad request）→ 不重试（重试也是同样错误）
//   - 2xx（成功）→ 不需要重试（理论上不会走到这里）
func shouldRetry(c *gin.Context, statusCode int) bool {
	if _, ok := c.Get(ctxkey.SpecificChannelId); ok {
		return false
	}
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	if statusCode/100 == 5 {
		return true
	}
	if statusCode == http.StatusBadRequest {
		return false
	}
	if statusCode/100 == 2 {
		return false
	}
	return true
}

// processChannelRelayError 异步处理转发失败
//   - 记录错误日志
//   - monitor.ShouldDisableChannel 判断是否要禁用该 channel（连续失败次数达阈值）
//   - 禁用：monitor.DisableChannel 把 channel 标记为禁用，下次 Distribute 不会选中
//   - 未达禁用阈值：monitor.Emit 上报失败状态
func processChannelRelayError(ctx context.Context, userId int, channelId int, channelName string, err model.ErrorWithStatusCode) {
	logger.Errorf(ctx, "relay error (channel id %d, user id: %d): %s", channelId, userId, err.Message)
	// https://platform.openai.com/docs/guides/error-codes/api-errors
	if monitor.ShouldDisableChannel(&err.Error, err.StatusCode) {
		monitor.DisableChannel(channelId, channelName, err.Message)
	} else {
		monitor.Emit(channelId, false)
	}
}

// RelayNotImplemented 未实现的 OpenAI API 兜底
//   router/relay.go 里所有未实现的接口（files/fine_tuning/assistants/threads）走这里
//   返 501 Not Implemented + JSON 错误
func RelayNotImplemented(c *gin.Context) {
	err := model.Error{
		Message: "API not implemented",
		Type:    "infergateway_error",
		Param:   "",
		Code:    "api_not_implemented",
	}
	c.JSON(http.StatusNotImplemented, gin.H{
		"error": err,
	})
}

// RelayNotFound 路由未匹配兜底
//   router/web.go 的 NoRoute handler 调用，对 /v1 和 /api 路径返 JSON 404
//   （否则会返 index.html 让客户端 JSON 解析失败）
func RelayNotFound(c *gin.Context) {
	err := model.Error{
		Message: fmt.Sprintf("Invalid URL (%s %s)", c.Request.Method, c.Request.URL.Path),
		Type:    "invalid_request_error",
		Param:   "",
		Code:    "",
	}
	c.JSON(http.StatusNotFound, gin.H{
		"error": err,
	})
}
