package adaptor

// relay/adaptor/interface.go — Adaptor 接口定义（InferGateway 核心抽象）
//
// 职责：
//   定义"如何把统一 OpenAI 格式请求转成厂商格式"的统一接口。
//   每个下游厂商（OpenAI/Claude/Gemini/vLLM...）实现这个接口，注册到 GetAdaptor 工厂。
//   relay/controller/text.go 的 RelayTextHelper 通过这个接口转发请求。
//
// 接口方法（按 relay 链调用顺序）：
//   1. Init(meta)                          — 初始化 adaptor（保存在 meta）
//   2. GetRequestURL(meta)                 — 拼下游 URL（vLLM 是 http://localhost:8000/v1/chat/completions）
//   3. ConvertRequest(c, relayMode, req)   — ★ 把统一 OpenAI 格式转成厂商格式
//                                            （Claude 要套 messages+anthropic-version 头）
//                                            （vLLM 兼容 OpenAI 格式，几乎零修改）
//   4. SetupRequestHeader(c, req, meta)    — 设置请求头（Authorization、Content-Type、厂商特有头）
//   5. DoRequest(c, meta, body)            — ★ 真正发 HTTP 到下游
//   6. DoResponse(c, resp, meta)           — ★ 解析下游返回，计算 usage（计费依据）
//
// 辅助方法：
//   ConvertImageRequest(req) — 图片请求转换（DALL·E 文生图用）
//   GetModelList()           — 返回该 adaptor 支持的模型列表
//   GetChannelName()         — 返回 adaptor 名称（日志用）
//
// InferGateway gRPC 层视角：
//   grpc/server.go 的 ChatCompletion 调 RelayTextHelper，
//   RelayTextHelper 内部调 GetAdaptor().Init/ConvertRequest/DoRequest/DoResponse，
//   即 gRPC 入口完全复用 adaptor 链，零修改。
//   vLLM 兼容 OpenAI 格式，所以用 openai.Adaptor 实现（不写新 adaptor）。

import (
	"github.com/gin-gonic/gin"
	"github.com/sakura5215/InferGateway/relay/meta"
	"github.com/sakura5215/InferGateway/relay/model"
	"io"
	"net/http"
)

// Adaptor 厂商适配器接口（每个下游厂商一个实现）
// 40+ 厂商实现：openai/anthropic/gemini/ali/baidu/tencent/zhipu/deepseek/doubao/ollama/...
// vLLM 直接复用 openai.Adaptor（兼容 OpenAI 格式）
type Adaptor interface {
	// 1. 初始化 adaptor（保存 meta 信息）
	Init(meta *meta.Meta)

	// 2. 拼下游请求 URL（按 meta 的 BaseURL + relayMode 决定路径）
	//    例如 OpenAI 是 https://api.openai.com/v1/chat/completions
	//         vLLM   是 http://localhost:8000/v1/chat/completions
	GetRequestURL(meta *meta.Meta) (string, error)

	// 3. 设置 HTTP 请求头（Authorization、Content-Type、厂商特有头如 anthropic-version）
	SetupRequestHeader(c *gin.Context, req *http.Request, meta *meta.Meta) error

	// 4. ★ 把统一 OpenAI 格式请求转成厂商格式
	//    OpenAI/vLLM 兼容 OpenAI 格式，几乎零修改
	//    Claude 要套 messages 数组 + anthropic-version 头
	//    Gemini 要转 contents 数组 + generationConfig
	ConvertRequest(c *gin.Context, relayMode int, request *model.GeneralOpenAIRequest) (any, error)

	// 5. 图片请求转换（DALL·E 文生图用，relayMode=ImagesGenerations 时走这个不是 ConvertRequest）
	ConvertImageRequest(request *model.ImageRequest) (any, error)

	// 6. ★ 真正发 HTTP 请求到下游
	//    用 http.Client 发，返回 *http.Response
	DoRequest(c *gin.Context, meta *meta.Meta, requestBody io.Reader) (*http.Response, error)

	// 7. ★ 解析下游响应，计算 usage（prompt_tokens/completion_tokens，计费依据）
	//    非 chat 模式（embeddings/audio）实现自己的 DoResponse
	//    失败时返 ErrorWithStatusCode（relay 链会判断是否重试）
	DoResponse(c *gin.Context, resp *http.Response, meta *meta.Meta) (usage *model.Usage, err *model.ErrorWithStatusCode)

	// 8. 返回该 adaptor 支持的模型列表（用于 /v1/models 接口展示）
	GetModelList() []string

	// 9. 返回 adaptor 名称（日志和错误信息用）
	GetChannelName() string
}
