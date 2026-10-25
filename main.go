package main

// main.go — InferGateway 程序总入口（HTTP :3000 + gRPC :50051 双协议）
//
// 作用：启动 DB/Redis/缓存/编排层/gRPC/HTTP，双协议入口并存。
// 流程：配置初始化 → DB/Redis/Option/Channel 缓存 → 编排层 → 路由装配 → gRPC → HTTP 阻塞。
// 详见 main() 函数内步骤注释。
//
// 关键环境变量：
//   PORT                          — HTTP 端口（默认 3000）
//   SQL_DSN                       — MySQL DSN（必填）
//   REDIS_CONN_STRING             — Redis 连接串（可选）
//   INFERGATEWAY_INSTANCES        — ★ vLLM 实例列表，格式 "url1,weight1;url2,weight2"
//   GIN_MODE                      — gin 模式（debug/release）

import (
	"embed"
	"fmt"
	"os"
	"strconv"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	_ "github.com/joho/godotenv/autoload"

	"github.com/sakura5215/InferGateway/common"
	"github.com/sakura5215/InferGateway/common/client"
	"github.com/sakura5215/InferGateway/common/config"
	"github.com/sakura5215/InferGateway/common/i18n"
	"github.com/sakura5215/InferGateway/common/logger"
	"github.com/sakura5215/InferGateway/controller"
	"github.com/sakura5215/InferGateway/grpc"
	"github.com/sakura5215/InferGateway/infergateway"
	"github.com/sakura5215/InferGateway/middleware"
	"github.com/sakura5215/InferGateway/model"
	"github.com/sakura5215/InferGateway/relay/adaptor/openai"
	"github.com/sakura5215/InferGateway/router"
)

// go:embed web/build/* — 把前端构建产物编译进二进制（启动不需要外部静态目录）
// 注意：上面这行是 Go 编译指令，不能加 // 前缀变成普通注释，否则 pattern 会失效
var buildFS embed.FS

// main 程序入口，详见文件头注释
func main() {
	// 1. 初始化通用配置 + 日志
	common.Init()
	logger.SetupLogger()
	logger.SysLogf("InferGateway %s started", common.Version)

	if os.Getenv("GIN_MODE") != gin.DebugMode {
		gin.SetMode(gin.ReleaseMode)
	}
	if config.DebugEnabled {
		logger.SysLog("running in debug mode")
	}

	// 4. 初始化 MySQL 数据库（建表 + 索引 + root 账户）
	// Initialize SQL Database
	model.InitDB()
	model.InitLogDB()

	var err error
	err = model.CreateRootAccountIfNeed()
	if err != nil {
		logger.FatalLog("database init error: " + err.Error())
	}
	defer func() {
		err := model.CloseDB()
		if err != nil {
			logger.FatalLog("failed to close database: " + err.Error())
		}
	}()

	// 5. 初始化 Redis（可选）
	// Initialize Redis
	err = common.InitRedisClient()
	if err != nil {
		logger.FatalLog("failed to initialize Redis: " + err.Error())
	}

	// 6. 加载系统选项 + channel 缓存（Distribute middleware 用）
	// Initialize options
	model.InitOptionMap()
	logger.SysLog(fmt.Sprintf("using theme %s", config.Theme))
	if common.RedisEnabled {
		// for compatibility with old versions
		config.MemoryCacheEnabled = true
	}
	if config.MemoryCacheEnabled {
		logger.SysLog("memory cache enabled")
		logger.SysLog(fmt.Sprintf("sync frequency: %d seconds", config.SyncFrequency))
		model.InitChannelCache()
	}
	if config.MemoryCacheEnabled {
		// 定时同步 DB → 内存（避免每次请求都查 DB）
		go model.SyncOptions(config.SyncFrequency)
		go model.SyncChannelCache(config.SyncFrequency)
	}
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		// 定时测试 channel 连通性（管理员配置）
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY"))
		if err != nil {
			logger.FatalLog("failed to parse CHANNEL_TEST_FREQUENCY: " + err.Error())
		}
		go controller.AutomaticallyTestChannels(frequency)
	}
	if os.Getenv("BATCH_UPDATE_ENABLED") == "true" {
		// 批量更新模式（quota/log 累积批量写 DB，提高性能）
		config.BatchUpdateEnabled = true
		logger.SysLog("batch update enabled with interval " + strconv.Itoa(config.BatchUpdateInterval) + "s")
		model.InitBatchUpdater()
	}
	if config.EnableMetric {
		logger.SysLog("metric enabled, will disable channel if too much request failed")
	}
	// 7. 初始化 OpenAI tokenizer + HTTP client
	openai.InitTokenEncoders()
	client.Init()

	// === [InferGateway 增量] 初始化推理服务编排层 ===
	// 从环境变量 INFERGATEWAY_INSTANCES 加载 vLLM 实例：
	//   格式 "http://localhost:8000,1;http://localhost:8001,1"
	// 启动 health checker goroutine（每 10s ping 各实例 /health）
	// 注册实例到 balancer（加权轮询 + failover）
	infergateway.InitOrchestrator()

	// Initialize i18n
	if err := i18n.Init(); err != nil {
		logger.FatalLog("failed to initialize i18n: " + err.Error())
	}

	// 10. 创建 Gin engine + 挂全局 middleware
	// Initialize HTTP server
	server := gin.New()
	server.Use(gin.Recovery())
	// This will cause SSE not to work!!!
	//server.Use(gzip.Gzip(gzip.DefaultCompression)) // gzip 会破坏 SSE 流式响应
	server.Use(middleware.RequestId())   // 每个请求加 X-Request-Id（日志追踪）
	server.Use(middleware.Language())    // 解析 Accept-Language（多语言）
	middleware.SetUpLogger(server)        // 请求日志（access log）
	// Initialize session store — cookie-based session（登录态）
	store := cookie.NewStore([]byte(config.SessionSecret))
	server.Use(sessions.Sessions("session", store))

	// 10. 装配所有路由组（/api /v1 /dashboard /web）
	router.SetRouter(server, buildFS)
	var port = os.Getenv("PORT")
	if port == "" {
		port = strconv.Itoa(*common.Port)
	}
	// === [InferGateway 增量] 启动 gRPC server，双协议入口 ===
	// 与 HTTP server 并存，监听 :50051
	// ChatCompletion 复用 relay 链（mock gin.Context 喂 RelayTextHelper）
	// ListChannels 返回编排层所有实例健康状态
	go grpc.StartServer(":50051")
	logger.SysLogf("server started on http://localhost:%s", port)
	// 12. 启动 HTTP server（阻塞主 goroutine）
	err = server.Run(":" + port)
	if err != nil {
		logger.FatalLog("failed to start HTTP server: " + err.Error())
	}
}
