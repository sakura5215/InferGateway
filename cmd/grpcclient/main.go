// InferGateway gRPC client 测试程序。
// 调用 ListChannels 查看实例健康状态，调用 ChatCompletion 验证推理转发。
//
// 启动：
//   go run ./cmd/grpcclient -addr localhost:50051 -token <your-one-api-token>
package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	pb "github.com/sakura5215/InferGateway/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	addr := flag.String("addr", "localhost:50051", "gRPC server address")
	token := flag.String("token", "", "one-api token (留空则跳过 ChatCompletion)")
	model := flag.String("model", "qwen2.5-1.5b-instruct", "model name")
	prompt := flag.String("prompt", "你好，简单介绍下你自己", "user prompt")
	flag.Parse()

	conn, err := grpc.Dial(*addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Println("dial error:", err)
		return
	}
	defer conn.Close()
	c := pb.NewInferGatewayServiceClient(conn)

	// 1. ListChannels：查看推理编排层的实例及健康状态
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	list, err := c.ListChannels(ctx, &pb.ListChannelsRequest{})
	if err != nil {
		fmt.Println("ListChannels error:", err)
	} else {
		fmt.Println("=== 推理实例列表 ===")
		for _, ch := range list.Channels {
			status := "healthy"
			if !ch.Healthy {
				status = "DOWN"
			}
			fmt.Printf("  #%d  %s  type=%s  %s\n", ch.Id, ch.Name, ch.Type, status)
		}
	}

	// 2. ChatCompletion：验证推理转发链路
	if *token == "" {
		fmt.Println("\n(skip ChatCompletion: 未提供 -token)")
		return
	}
	resp, err := c.ChatCompletion(ctx, &pb.ChatRequest{
		Token:       *token,
		Model:       *model,
		Messages:    fmt.Sprintf(`[{"role":"user","content":%q}]`, *prompt),
		Temperature: 0.7,
		MaxTokens:   100,
	})
	if err != nil {
		fmt.Println("\nChatCompletion error:", err)
		return
	}
	fmt.Printf("\n=== ChatCompletion ===\ncode=%d\n", resp.Code)
	if resp.Code == 0 {
		fmt.Println("response:", resp.Data)
	} else {
		fmt.Println("error:", resp.Message)
	}
}
