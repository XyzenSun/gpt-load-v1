# internal/middleware 模块约定

本目录提供 Gin HTTP 中间件, 只有 `middleware.go`, 全部以返回 `gin.HandlerFunc` 的工厂函数暴露, 由 `internal/router/router.go` 按固定顺序装配.

## 现有中间件

- `Recovery`: 用 `gin.CustomRecovery` 捕获 panic, 记错误日志并返回 `ErrInternalServer`.
- `ErrorHandler`: 在 `c.Next()` 后读取 `c.Errors`, 把 `*app_errors.APIError` 交给 `response.Error`, 其他错误统一按 500 处理.
- `Logger`: 请求结束后按状态码选级别, `/health` 低于 400 不打印, 并从 context 读取 `keyIndex`, `keyPreview`, `retryCount` 追加信息.
- `CORS`: 依据 `CORSConfig`, 未启用直接放行; OPTIONS 预检返回 204.
- `RateLimiter`: 用信号量按 `PerformanceConfig.MaxConcurrentRequests` 限制并发, 满则回错误.
- `Auth` 与 `ProxyAuth`: 认证中间件, 详见下节.
- `SecurityHeaders` 与 `StaticCache`: 前者加安全响应头, 后者对 `/assets/` 与常见静态后缀设置 `Cache-Control: public, max-age=2592000, immutable` 并把 `Expires` 设为一年后.

## 认证与密钥提取

`extractAuthKey` 依次尝试 query 参数 `key` (取用后会从 `RawQuery` 中删除), `Authorization: Bearer`, `X-Api-Key`, `X-Goog-Api-Key`. `Auth` 用 `subtle.ConstantTimeCompare` 与 `AuthConfig.Key` 做常量时间比较, 并跳过 `isMonitoringEndpoint` (当前仅 `/health`). `ProxyAuth` 先取 `c.Param("group_name")` 查 `GroupManager.GetGroupByName`, 命中 `EffectiveConfig.ProxyKeysMap` 或 `ProxyKeysMap` 之一才放行. 新增密钥来源时改 `extractAuthKey`, 新增免鉴权路径时改 `isMonitoringEndpoint`, 不要在各中间件内散落判断.

## 关键约束

`ProxyRouteDispatcher` 的参数是只含 `GetIntegrationInfo` 的接口, 便于路由层传入 `handler.Server` 而不引入更多耦合. `RateLimiter` 的信号量在中间件构造时创建, 容量取自构造时的配置, 运行期改配置不会改变已建实例. 认证失败与并发超限都经 `response.Error` 输出, 保持统一错误格式. 本目录没有测试文件, 改动后从仓库根执行 `go build ./...`, 并启动服务做一次真实请求验证.
