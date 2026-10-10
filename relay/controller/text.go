package controller

// relay/controller/text.go — RelayTextHelper 主流程（InferGateway 真正的核心）
//
// 职责：
//   把鉴权+选渠道后的请求转发到下游，并计费。
//   这是整个项目最核心的函数，HTTP 入口和 gRPC 入口都走这里。
//
// 主流程：解析请求 → 预扣 quota → 选 adaptor → 转发下游 → 解析返回 → 后扣 quota。
//
// InferGateway gRPC 复用：
//   grpc/server.go 的 ChatCompletion 用 mock gin.Context + httptest.NewRecorder
//   包装一个伪造的 gin.Context 喂给 RelayTextHelper。
//   relay 链完全不知道是 HTTP 还是 gRPC，零修改复用。
//   mock gin.Context 的关键：用 gin.CreateTestContext(recorder) 创建，
//   recorder 是 httptest.NewRecorder()，从 recorder.Body 取响应 JSON。
//
// 失败处理：
//   preConsumeQuota 失败 → 直接返 429（不打下游）
//   adaptor.DoRequest 失败 → 返 ErrorWithStatusCode，controller.Relay 会判断重试
//   adaptor.DoResponse 失败 → 返 ErrorWithStatusCode（计费已扣，需要 refundQuota）

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/sakura5215/InferGateway/common/config"
	"github.com/sakura5215/InferGateway/common/logger"
	"github.com/sakura5215/InferGateway/relay"
	"github.com/sakura5215/InferGateway/relay/adaptor"
	"github.com/sakura5215/InferGateway/relay/adaptor/openai"
	"github.com/sakura5215/InferGateway/relay/apitype"
	"github.com/sakura5215/InferGateway/relay/billing"
	billingratio "github.com/sakura5215/InferGateway/relay/billing/ratio"
	"github.com/sakura5215/InferGateway/relay/channeltype"
	"github.com/sakura5215/InferGateway/relay/meta"
	"github.com/sakura5215/InferGateway/relay/model"
)

// RelayTextHelper 推理转发主流程（HTTP 和 gRPC 共用入口）。
// 主流程：meta 提取 → 解析请求 → 预扣 quota → GetAdaptor 选 adaptor →
// DoRequest 转发下游 → DoResponse 解析返回算 usage → postConsumeQuota 后扣。
// 失败处理：preConsumeQuota 失败返 429 不打下游；DoResponse 失败需 refundQuota。
func RelayTextHelper(c *gin.Context) *model.ErrorWithStatusCode {
	ctx := c.Request.Context()
	// 1. 从 gin.Context 提取 ctxkey 包装成 meta
	meta := meta.GetByContext(c)
	// 2. 解析请求 body 成 GeneralOpenAIRequest
	// get & validate textRequest
	textRequest, err := getAndValidateTextRequest(c, meta.Mode)
	if err != nil {
		logger.Errorf(ctx, "getAndValidateTextRequest failed: %s", err.Error())
		return openai.ErrorWrapper(err, "invalid_text_request", http.StatusBadRequest)
	}
	meta.IsStream = textRequest.Stream

	// 3. 模型名映射（channel.ModelMapping，如 gpt-4 → gpt-4-0613）
	// map model name
	meta.OriginModelName = textRequest.Model
	textRequest.Model, _ = getMappedModelName(textRequest.Model, meta.ModelMapping)
	meta.ActualModelName = textRequest.Model
	// 4. 注入渠道级 system prompt（管理员在渠道配置里设）
	// set system prompt if not empty
	systemPromptReset := setSystemPrompt(ctx, textRequest, meta.ForcedSystemPrompt)
	// 5. 计算计费倍率（model_ratio × group_ratio）
	// get model ratio & group ratio
	modelRatio := billingratio.GetModelRatio(textRequest.Model, meta.ChannelType)
	groupRatio := billingratio.GetGroupRatio(meta.Group)
	ratio := modelRatio * groupRatio
	// 6. ★ 预扣 quota（防用户跑完不付钱，prompt_tokens × ratio）
	// pre-consume quota
	promptTokens := getPromptTokens(textRequest, meta.Mode)
	meta.PromptTokens = promptTokens
	preConsumedQuota, bizErr := preConsumeQuota(ctx, textRequest, promptTokens, ratio, meta)
	if bizErr != nil {
		logger.Warnf(ctx, "preConsumeQuota failed: %+v", *bizErr)
		return bizErr
	}

	// 7. ★ 工厂选 adaptor（vLLM 走 openai.Adaptor，因为兼容 OpenAI 格式）
	adaptor := relay.GetAdaptor(meta.APIType)
	if adaptor == nil {
		return openai.ErrorWrapper(fmt.Errorf("invalid api type: %d", meta.APIType), "invalid_api_type", http.StatusBadRequest)
	}
	adaptor.Init(meta)

	// get request body
	requestBody, err := getRequestBody(c, meta, textRequest, adaptor)
	if err != nil {
		return openai.ErrorWrapper(err, "convert_request_failed", http.StatusInternalServerError)
	}

	// do request
	resp, err := adaptor.DoRequest(c, meta, requestBody)
	if err != nil {
		logger.Errorf(ctx, "DoRequest failed: %s", err.Error())
		return openai.ErrorWrapper(err, "do_request_failed", http.StatusInternalServerError)
	}
	if isErrorHappened(meta, resp) {
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return RelayErrorHandler(resp)
	}

	// do response
	usage, respErr := adaptor.DoResponse(c, resp, meta)
	if respErr != nil {
		logger.Errorf(ctx, "respErr is not nil: %+v", respErr)
		billing.ReturnPreConsumedQuota(ctx, preConsumedQuota, meta.TokenId)
		return respErr
	}
	// post-consume quota
	go postConsumeQuota(ctx, usage, meta, textRequest, ratio, preConsumedQuota, modelRatio, groupRatio, systemPromptReset)
	return nil
}

func getRequestBody(c *gin.Context, meta *meta.Meta, textRequest *model.GeneralOpenAIRequest, adaptor adaptor.Adaptor) (io.Reader, error) {
	if !config.EnforceIncludeUsage &&
		meta.APIType == apitype.OpenAI &&
		meta.OriginModelName == meta.ActualModelName &&
		meta.ChannelType != channeltype.Baichuan &&
		meta.ForcedSystemPrompt == "" {
		// no need to convert request for openai
		return c.Request.Body, nil
	}

	// get request body
	var requestBody io.Reader
	convertedRequest, err := adaptor.ConvertRequest(c, meta.Mode, textRequest)
	if err != nil {
		logger.Debugf(c.Request.Context(), "converted request failed: %s\n", err.Error())
		return nil, err
	}
	jsonData, err := json.Marshal(convertedRequest)
	if err != nil {
		logger.Debugf(c.Request.Context(), "converted request json_marshal_failed: %s\n", err.Error())
		return nil, err
	}
	logger.Debugf(c.Request.Context(), "converted request: \n%s", string(jsonData))
	requestBody = bytes.NewBuffer(jsonData)
	return requestBody, nil
}
