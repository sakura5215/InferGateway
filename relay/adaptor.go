package relay

// relay/adaptor.go — GetAdaptor 工厂函数（InferGateway 核心路由分发）
//
// 职责：
//   按 channel 类型返回对应的 adaptor 实例。
//   每个 channel 在 DB 里配 type（int），relay 链调 GetAdaptor(type) 拿 adaptor。
//   relay/controller/text.go 的 RelayTextHelper 会用这个工厂。
//
// 设计模式：工厂模式 + 策略模式
//   - 工厂：GetAdaptor 按 apiType 返回不同 adaptor 实例
//   - 策略：每个 adaptor 实现相同 Adaptor 接口，但内部行为不同
//
// 厂商列表（40+ 个 adaptor，覆盖主流 LLM 服务）：
//   OpenAI 系：openai.Adaptor（vLLM 也走这个，兼容 OpenAI 格式）
//   云厂商：Ali(百炼)/Baidu(千帆)/Tencent(混元)/Zhipu(智谱)/Doubao/Xunfei(星火)
//   海外：Anthropic(Claude)/Gemini(Google)/PaLM(老 Google AI)/VertexAI(GCP)
//   自建：Ollama(本地)
//   特殊：AIProxyLibrary(proxy pool)/Cohere/Replicate/DeepL/Coze/Cloudflare/AWS Claude
//   代理：proxy.Adaptor（私有 /oneapi/proxy 走原始路径转发）
//
// vLLM 在 InferGateway 里：
//   vLLM 启动时 --served-model-name qwen3-4b-instruct，
//   channel type 配 OpenAI（type=1），base_url=http://localhost:8000，
//   GetAdaptor(1) 返回 openai.Adaptor，转发请求格式零修改。

import (
	"github.com/sakura5215/InferGateway/relay/adaptor"
	"github.com/sakura5215/InferGateway/relay/adaptor/aiproxy"
	"github.com/sakura5215/InferGateway/relay/adaptor/ali"
	"github.com/sakura5215/InferGateway/relay/adaptor/anthropic"
	"github.com/sakura5215/InferGateway/relay/adaptor/aws"
	"github.com/sakura5215/InferGateway/relay/adaptor/baidu"
	"github.com/sakura5215/InferGateway/relay/adaptor/cloudflare"
	"github.com/sakura5215/InferGateway/relay/adaptor/cohere"
	"github.com/sakura5215/InferGateway/relay/adaptor/coze"
	"github.com/sakura5215/InferGateway/relay/adaptor/deepl"
	"github.com/sakura5215/InferGateway/relay/adaptor/gemini"
	"github.com/sakura5215/InferGateway/relay/adaptor/ollama"
	"github.com/sakura5215/InferGateway/relay/adaptor/openai"
	"github.com/sakura5215/InferGateway/relay/adaptor/palm"
	"github.com/sakura5215/InferGateway/relay/adaptor/proxy"
	"github.com/sakura5215/InferGateway/relay/adaptor/replicate"
	"github.com/sakura5215/InferGateway/relay/adaptor/tencent"
	"github.com/sakura5215/InferGateway/relay/adaptor/vertexai"
	"github.com/sakura5215/InferGateway/relay/adaptor/xunfei"
	"github.com/sakura5215/InferGateway/relay/adaptor/zhipu"
	"github.com/sakura5215/InferGateway/relay/apitype"
)

// GetAdaptor 工厂函数：按 apiType 返回对应 adaptor 实例
//   apiType 来自 channel.Type（DB 字段），常量定义在 relay/apitype/apitype.go
//   默认兜底：openai.Adaptor（兼容 OpenAI 格式的厂商都走这个）
func GetAdaptor(apiType int) adaptor.Adaptor {
	switch apiType {
	case apitype.AIProxyLibrary:
		return &aiproxy.Adaptor{}
	case apitype.Ali:
		return &ali.Adaptor{}
	case apitype.Anthropic:
		return &anthropic.Adaptor{}
	case apitype.AwsClaude:
		return &aws.Adaptor{}
	case apitype.Baidu:
		return &baidu.Adaptor{}
	case apitype.Gemini:
		return &gemini.Adaptor{}
	case apitype.OpenAI:
		return &openai.Adaptor{} // ★ vLLM 也走这个（兼容 OpenAI 格式）
	case apitype.PaLM:
		return &palm.Adaptor{}
	case apitype.Tencent:
		return &tencent.Adaptor{}
	case apitype.Xunfei:
		return &xunfei.Adaptor{}
	case apitype.Zhipu:
		return &zhipu.Adaptor{}
	case apitype.Ollama:
		return &ollama.Adaptor{}
	case apitype.Coze:
		return &coze.Adaptor{}
	case apitype.Cohere:
		return &cohere.Adaptor{}
	case apitype.Cloudflare:
		return &cloudflare.Adaptor{}
	case apitype.DeepL:
		return &deepl.Adaptor{}
	case apitype.VertexAI:
		return &vertexai.Adaptor{}
	case apitype.Proxy:
		return &proxy.Adaptor{}
	case apitype.Replicate:
		return &replicate.Adaptor{}
	}
	return nil
}
