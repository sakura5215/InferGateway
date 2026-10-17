// Package grpc 实现 InferGateway 的 gRPC 接口层（增量模块 #1）。
//
// 设计要点：gRPC 入口复用 one-api 的 relay/adaptor 转发链。
// 通过构造 mock *gin.Context（用 httptest.NewRecorder 作 Writer），
// 让 RelayTextHelper 以为是一次正常 HTTP 请求，
// 从而零修改复用鉴权、adaptor 转发、计费全流程。
//
// 选实例不走 one-api 的 Distribute，而是走本项目的推理编排层 LB，
// 这样推理服务的健康检查和故障切换独立于渠道分发，是核心增量。
package grpc

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/sakura5215/InferGateway/common/ctxkey"
	"github.com/sakura5215/InferGateway/common/logger"
	"github.com/sakura5215/InferGateway/infergateway"
	"github.com/sakura5215/InferGateway/middleware"
	dbmodel "github.com/sakura5215/InferGateway/model"
	pb "github.com/sakura5215/InferGateway/proto"
	"github.com/sakura5215/InferGateway/relay/channeltype"
	relaycontroller "github.com/sakura5215/InferGateway/relay/controller"
	relaymodel "github.com/sakura5215/InferGateway/relay/model"
	grpccore "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server 实现 InferGatewayServiceServer。
type Server struct {
	pb.UnimplementedInferGatewayServiceServer
}

// StartServer 启动 gRPC 服务监听 addr（如 :50051）。
// 在 main.go 里以 goroutine 调用，与 HTTP server 并存，实现双协议入口。
func StartServer(addr string) {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		logger.FatalLog("gRPC listen failed: " + err.Error())
		return
	}
	s := grpccore.NewServer()
	pb.RegisterInferGatewayServiceServer(s, &Server{})
	logger.SysLog("gRPC server listening on " + addr)
	if err := s.Serve(lis); err != nil {
		logger.FatalLog("gRPC serve failed: " + err.Error())
	}
}

// ChatCompletion 同步聊天补全：复用 relay 转发链 + 推理编排层选实例。
//
// 流程：鉴权 token → 取用户分组 → 编排层 LB 选健康实例 →
// 构造临时 channel(base_url=实例URL) + mock gin.Context → 调 RelayTextHelper →
// 失败(5xx/超时)则 Failover 切下一个实例重试。
func (s *Server) ChatCompletion(ctx context.Context, req *pb.ChatRequest) (*pb.ChatResponse, error) {
	// 1. 鉴权：复用 one-api 的 token 校验
	//    客户端可能带 sk- 前缀（OpenAI 惯例），ValidateUserToken 期望纯 key，需去掉前缀
	tokenKey := strings.TrimPrefix(req.Token, "sk-")
	token, err := dbmodel.ValidateUserToken(tokenKey)
	if err != nil {
		return &pb.ChatResponse{Code: 401, Message: "invalid token: " + err.Error()}, nil
	}

	// 2. 组装 OpenAI 格式请求体，messages 原样透传给 adaptor
	reqBody := map[string]any{
		"model":    req.Model,
		"messages": json.RawMessage(req.Messages),
		"stream":   req.Stream,
	}
	if req.Temperature > 0 {
		reqBody["temperature"] = req.Temperature
	}
	if req.MaxTokens > 0 {
		reqBody["max_tokens"] = req.MaxTokens
	}
	bodyBytes, _ := json.Marshal(reqBody)

	// 3. 取用户分组（计费要用）
	userGroup, err := dbmodel.CacheGetUserGroup(token.UserId)
	if err != nil {
		return &pb.ChatResponse{Code: 500, Message: "get user group failed: " + err.Error()}, nil
	}

	// 4. 推理编排层：选实例 + failover 重试
	//    不走 one-api 的 Distribute（按 group 选 channel），而是走编排层 LB 选 vLLM 实例。
	//    这样推理服务的健康检查和故障切换独立于渠道分发，是本项目核心增量。
	maxRetries := 3
	var bizErr *relaymodel.ErrorWithStatusCode
	for attempt := 0; attempt < maxRetries; attempt++ {
		inst, err := infergateway.LB.Pick()
		if err != nil {
			return &pb.ChatResponse{Code: 503, Message: "no healthy inference instance: " + err.Error()}, nil
		}

		// 4.1 构造 mock gin.Context
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(bodyBytes))
		c.Request.Header.Set("Authorization", "Bearer "+req.Token)
		c.Request.Header.Set("Content-Type", "application/json")

		// 4.2 鉴权上下文
		c.Set(ctxkey.Id, token.UserId)
		c.Set(ctxkey.TokenId, token.Id)
		c.Set(ctxkey.TokenName, token.Name)
		c.Set(ctxkey.RequestModel, req.Model)
		c.Set(ctxkey.Group, userGroup)

		// 4.3 用编排层选中的实例构造临时 channel，base_url=实例 URL
		//     vLLM 兼容 OpenAI 格式，channel.Type=OpenAI 复用 openai.Adaptor 转发
		instURL := inst.URL
		channel := &dbmodel.Channel{
			Id:      inst.ID,
			Type:    channeltype.OpenAI,
			Name:    "vllm-" + strconv.Itoa(inst.ID),
			Key:     "EMPTY",
			Status:  dbmodel.ChannelStatusEnabled,
			BaseURL: &instURL,
			Group:   "default",
			Models:  req.Model,
		}
		middleware.SetupContextForSelectedChannel(c, channel, req.Model)

		// 4.4 调 relay 转发链（复用 RelayTextHelper：解析→计费→adaptor→转发→响应）
		bizErr = relaycontroller.RelayTextHelper(c)
		if bizErr == nil {
			return &pb.ChatResponse{Code: 0, Data: recorder.Body.String()}, nil
		}

		// 4.5 失败判断：仅 5xx 或超时才 failover，4xx 是请求本身有问题不重试
		if bizErr.StatusCode < 500 && bizErr.StatusCode != http.StatusRequestTimeout {
			break
		}
		if attempt < maxRetries-1 {
			infergateway.LB.Failover(inst)
		}
	}

	if bizErr != nil {
		return &pb.ChatResponse{Code: int32(bizErr.StatusCode), Message: bizErr.Error.Message}, nil
	}
	return &pb.ChatResponse{Code: 500, Message: "all retries failed"}, nil
}

// ChatCompletionStream 流式聊天补全。
// 当前返回 Unimplemented，后续需用支持 flush 的 ResponseWriter 替换 httptest.Recorder。
func (s *Server) ChatCompletionStream(req *pb.ChatRequest, stream pb.InferGatewayService_ChatCompletionStreamServer) error {
	return status.Errorf(codes.Unimplemented, "stream mode not implemented yet, use ChatCompletion instead")
}

// ListChannels 列出推理编排层管理的实例及健康状态。
func (s *Server) ListChannels(ctx context.Context, req *pb.ListChannelsRequest) (*pb.ListChannelsResponse, error) {
	instances := infergateway.Checker.All()
	list := make([]*pb.ChannelInfo, 0, len(instances))
	for _, inst := range instances {
		list = append(list, &pb.ChannelInfo{
			Id:      int32(inst.ID),
			Name:    inst.URL,
			Type:    "vllm",
			Healthy: inst.IsHealthy(),
		})
	}
	return &pb.ListChannelsResponse{Channels: list}, nil
}
