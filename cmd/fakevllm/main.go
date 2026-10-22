// fake vllm server：模拟 vLLM 的 /health 和 /v1/chat/completions 端点，
// 作为故障切换演示的备用推理实例（真 vLLM 实例挂掉时请求切到这里）。
//
// 启动：go run ./cmd/fakevllm -port 8001
package main

import (
	"flag"
	"fmt"
	"net/http"
	"time"
)

func main() {
	port := flag.Int("port", 8001, "listen port")
	flag.Parse()

	mux := http.NewServeMux()

	// vLLM 的健康检查端点：200=就绪
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// vLLM 的 OpenAI 兼容聊天端点：返回最小 OpenAI 格式响应
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 响应里带上端口，方便演示时看出请求被切到了哪个实例
		resp := fmt.Sprintf(`{"id":"chatcmpl-fake-%d","object":"chat.completion","created":%d,"model":"fake-vllm","choices":[{"index":0,"message":{"role":"assistant","content":"[fake vllm on :%d] this is a mock response for failover demo"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":15,"total_tokens":25}}`,
			*port, time.Now().Unix(), *port)
		fmt.Fprint(w, resp)
	})

	// vLLM 的模型列表端点
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"qwen2.5-1.5b","object":"model"}]}`)
	})

	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("fake vllm server listening on %s\n", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Println("listen error:", err)
	}
}
