package middleware

// middleware/auth.go — 鉴权 middleware 集合
//
// 五个鉴权函数，严格度递增：
//   UserAuth   — 普通用户（已登录，cookie session 或 access token）
//   AdminAuth  — 管理员
//   RootAuth   — root 账户（环境变量初始化的唯一 root）
//   TokenAuth  — API token 鉴权（sk-xxx，用于 /v1/* 推理接口和 /dashboard 计费）
//
// 区别：
//   UserAuth/AdminAuth/RootAuth 用 cookie session（浏览器登录场景）
//   TokenAuth 用 sk-xxx token（API 调用场景，无 cookie）
//
// TokenAuth 是 InferGateway 核心链路（/v1/* 路由组都挂）：
//   1. 解析 Authorization: Bearer sk-xxx
//   2. ValidateUserToken 校验 token 有效性 + 提取用户信息
//   3. 校验 IP 网段限制（token.Subnet 可配置只允许某 IP 段使用）
//   4. 校验用户是否被封禁（blacklist）
//   5. 提取 requestModel（路径参数或 body 解析）
//   6. 校验 token 是否有权用该 model（token.Models 可限制）
//   7. 塞 ctxkey: Id/TokenId/TokenName/RequestModel/AvailableModels
//   8. 支持 SpecificChannelId：admin 可以指定渠道，普通用户不行
//
// gRPC 层（grpc/server.go）复刻了 TokenAuth 的塞 ctxkey 逻辑：
//   鉴权后调 RelayTextHelper 走完整 relay 链。

import (
	"fmt"
	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"github.com/sakura5215/InferGateway/common/blacklist"
	"github.com/sakura5215/InferGateway/common/ctxkey"
	"github.com/sakura5215/InferGateway/common/network"
	"github.com/sakura5215/InferGateway/model"
	"net/http"
	"strings"
)

// authHelper cookie session 鉴权的核心实现
//   优先级：cookie session > access token（Authorization header）
//   1. 从 session 取 username/role/id/status
//   2. session 为空则取 Authorization header 调 model.ValidateAccessToken
//   3. 校验用户是否被禁用（status=disabled）或被 blacklist 封禁
//   4. 校验角色 >= minRole（普通/管理员/root 三级）
//   5. 通过则塞 c.Set("username"/"role"/"id")，否则 Abort 返 JSON 错误
func authHelper(c *gin.Context, minRole int) {
	session := sessions.Default(c)
	username := session.Get("username")
	role := session.Get("role")
	id := session.Get("id")
	status := session.Get("status")
	if username == nil {
		// session 没登录信息，尝试 access token
		accessToken := c.Request.Header.Get("Authorization")
		if accessToken == "" {
			c.JSON(http.StatusUnauthorized, gin.H{
				"success": false,
				"message": "无权进行此操作，未登录且未提供 access token",
			})
			c.Abort()
			return
		}
		user := model.ValidateAccessToken(accessToken)
		if user != nil && user.Username != "" {
			// access token 有效，提取用户信息
			username = user.Username
			role = user.Role
			id = user.Id
			status = user.Status
		} else {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "无权进行此操作，access token 无效",
			})
			c.Abort()
			return
		}
	}
	// 校验用户是否被禁用（DB 里 status=disabled 或 blacklist 内存黑名单）
	if status.(int) == model.UserStatusDisabled || blacklist.IsUserBanned(id.(int)) {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "用户已被封禁",
		})
		session := sessions.Default(c)
		session.Clear() // 清 session 强制用户重新登录
		_ = session.Save()
		c.Abort()
		return
	}
	// 校验角色权限（普通用户 RoleCommonUser / 管理员 RoleAdminUser / root RoleRootUser）
	if role.(int) < minRole {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "无权进行此操作，权限不足",
		})
		c.Abort()
		return
	}
	c.Set("username", username)
	c.Set("role", role)
	c.Set("id", id)
	c.Next()
}

// UserAuth 普通用户鉴权（minRole=RoleCommonUser，最低门槛）
func UserAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, model.RoleCommonUser)
	}
}

// AdminAuth 管理员鉴权（minRole=RoleAdminUser）
func AdminAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, model.RoleAdminUser)
	}
}

// RootAuth root 账户鉴权（minRole=RoleRootUser，最高权限）
//   只有一个 root 账户能用，环境变量初始化（默认 root/123456）
func RootAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		authHelper(c, model.RoleRootUser)
	}
}

// TokenAuth API token 鉴权（sk-xxx，InferGateway /v1/* 路由组核心 middleware）
//   完整流程（详见文件头注释）：
//   1. 解析 Authorization: Bearer sk-xxx → 纯 key
//   2. ValidateUserToken 校验 + 返 token 对象
//   3. IP 网段限制检查（token.Subnet）
//   4. 用户封禁检查（blacklist + CacheIsUserEnabled）
//   5. 解析 requestModel（路径参数 :model 或 body 里的 model 字段）
//   6. token.Models 白名单检查
//   7. 塞 ctxkey: Id/TokenId/TokenName/RequestModel/AvailableModels
//   8. SpecificChannelId 支持（admin 可指定渠道，普通用户禁止）
//   9. 支持 :channelid 路径参数（/v1/oneapi/proxy/:channelid/*）
func TokenAuth() func(c *gin.Context) {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		// 1. 解析 Authorization header
		key := c.Request.Header.Get("Authorization")
		key = strings.TrimPrefix(key, "Bearer ") // 去掉 "Bearer " 前缀
		key = strings.TrimPrefix(key, "sk-")     // 去掉 "sk-" 前缀
		// 支持指定渠道：sk-xxx-channelId 格式（admin 才能用）
		parts := strings.Split(key, "-")
		key = parts[0]
		// 2. 校验 token 有效性（DB 查询 + quota 检查）
		token, err := model.ValidateUserToken(key)
		if err != nil {
			abortWithMessage(c, http.StatusUnauthorized, err.Error())
			return
		}
		// 3. IP 网段限制（如果 token 配置了 Subnet，只允许指定 IP 段调用）
		if token.Subnet != nil && *token.Subnet != "" {
			if !network.IsIpInSubnets(ctx, c.ClientIP(), *token.Subnet) {
				abortWithMessage(c, http.StatusForbidden, fmt.Sprintf("该令牌只能在指定网段使用：%s，当前 ip：%s", *token.Subnet, c.ClientIP()))
				return
			}
		}
		// 4. 用户封禁检查（双重：DB status + 内存 blacklist）
		userEnabled, err := model.CacheIsUserEnabled(token.UserId)
		if err != nil {
			abortWithMessage(c, http.StatusInternalServerError, err.Error())
			return
		}
		if !userEnabled || blacklist.IsUserBanned(token.UserId) {
			abortWithMessage(c, http.StatusForbidden, "用户已被封禁")
			return
		}
		// 5. 解析请求模型名（从路径参数或 body）
		requestModel, err := getRequestModel(c)
		if err != nil && shouldCheckModel(c) {
			abortWithMessage(c, http.StatusBadRequest, err.Error())
			return
		}
		c.Set(ctxkey.RequestModel, requestModel)
		// 6. token 模型白名单（token.Models 配置只允许用某些 model）
		if token.Models != nil && *token.Models != "" {
			c.Set(ctxkey.AvailableModels, *token.Models)
			if requestModel != "" && !isModelInList(requestModel, *token.Models) {
				abortWithMessage(c, http.StatusForbidden, fmt.Sprintf("该令牌无权使用模型：%s", requestModel))
				return
			}
		}
		// 7. 塞用户身份信息（后面 relay 链要用：计费、日志、限制）
		c.Set(ctxkey.Id, token.UserId)
		c.Set(ctxkey.TokenId, token.Id)
		c.Set(ctxkey.TokenName, token.Name)
		// 8. 支持指定渠道：sk-xxx-{channelId} 格式（仅 admin 可用）
		if len(parts) > 1 {
			if model.IsAdmin(token.UserId) {
				c.Set(ctxkey.SpecificChannelId, parts[1])
			} else {
				abortWithMessage(c, http.StatusForbidden, "普通用户不支持指定渠道")
				return
			}
		}

		// 9. /v1/oneapi/proxy/:channelid/* 路径参数也支持指定渠道
		if channelId := c.Param("channelid"); channelId != "" {
			c.Set(ctxkey.SpecificChannelId, channelId)
		}

		c.Next()
	}
}

// shouldCheckModel 判断当前路径是否需要强制校验 requestModel
//   completions/chat/images/audio 这些必须带 model 字段，其他（如 models 列表）不需要
func shouldCheckModel(c *gin.Context) bool {
	if strings.HasPrefix(c.Request.URL.Path, "/v1/completions") {
		return true
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/chat/completions") {
		return true
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/images") {
		return true
	}
	if strings.HasPrefix(c.Request.URL.Path, "/v1/audio") {
		return true
	}
	return false
}
