package router

// router/web.go — 前端静态资源路由
//
// 职责：
//   把 web/build/ 目录下的前端构建产物（React SPA）当静态资源服务。
//   所有未匹配的路径返 index.html（让 React Router 接管前端路由）。
//
//   特殊处理：/v1/* 和 /api/* 开头的路径返 404 JSON（不能返 HTML，
//   否则客户端 JSON 解析失败）。
//
// 全局 middleware：
//   - Gzip                 — 压缩响应体（HTML/CSS/JS）
//   - GlobalWebRateLimit   — Web 端限流（比 API 限流更宽松）
//   - Cache                — 静态资源缓存（index.html 不缓存，资源文件缓存 1 天）
//
// embed 嵌入机制：
//   main.go 用 //go:embed web/build/* 把前端打包进二进制，
//   SetWebRouter 用 common.EmbedFolder 包装成 http.FileSystem 给 gin-contrib/static。

import (
	"embed"
	"fmt"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/sakura5215/InferGateway/common"
	"github.com/sakura5215/InferGateway/common/config"
	"github.com/sakura5215/InferGateway/controller"
	"github.com/sakura5215/InferGateway/middleware"
	"net/http"
	"strings"
)

// SetWebRouter 注册前端静态资源路由
//   参数：
//     router   — gin.Engine
//     buildFS  — //go:embed web/build/* 嵌入的前端资源
//   行为：
//     - static.Serve("/")   — 服务 web/build/{theme}/ 下的静态文件
//     - NoRoute handler     — 未匹配的路径返 index.html（SPA 路由 fallback）
//     - 但 /v1 和 /api 开头返 JSON 404（不能返 HTML）
func SetWebRouter(router *gin.Engine, buildFS embed.FS) {
	// 读取主题对应的 index.html（默认 default 主题）
	indexPageData, _ := buildFS.ReadFile(fmt.Sprintf("web/build/%s/index.html", config.Theme))
	router.Use(gzip.Gzip(gzip.DefaultCompression))
	router.Use(middleware.GlobalWebRateLimit())
	router.Use(middleware.Cache())
	// gin-contrib/static 服务静态文件
	router.Use(static.Serve("/", common.EmbedFolder(buildFS, fmt.Sprintf("web/build/%s", config.Theme))))
	// 兜底路由：所有未匹配的请求走这里
	router.NoRoute(func(c *gin.Context) {
		// /v1 和 /api 路径必须返 JSON 404（客户端按 JSON 解析）
		if strings.HasPrefix(c.Request.RequestURI, "/v1") || strings.HasPrefix(c.Request.RequestURI, "/api") {
			controller.RelayNotFound(c)
			return
		}
		// 其他路径返 index.html（SPA 路由 fallback，让 React Router 接管）
		// index.html 不缓存（避免前端发版后浏览器用旧缓存）
		c.Header("Cache-Control", "no-cache")
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexPageData)
	})
}
