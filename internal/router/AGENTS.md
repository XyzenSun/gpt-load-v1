# internal/router 模块约定

本目录负责 HTTP 路由装配, 只有 `router.go`. `NewRouter` 接收 `handler.Server`, `proxy.ProxyServer`, `configManager`, `groupManager`, `embed.FS` 与 `indexPage`, 设置 `gin.ReleaseMode` 后返回 `*gin.Engine`, 是 Gin 与各业务入口之间的唯一接线点.

## 全局中间件

`NewRouter` 中的注册顺序即执行顺序, 改动需谨慎: `Recovery` -> `ErrorHandler` -> `Logger` -> `CORS` -> `RateLimiter` -> `SecurityHeaders`, 之后是一个把 `serverStartTime` 写入 context 的内联中间件. 中间件实现在 `internal/middleware`.

## 路由分区

- `registerSystemRoutes`: `GET /health`.
- `registerAPIRoutes`: `/api` 组先挂 `i18n.Middleware()`; `/auth/login` 与 `/integration/info` 公开, 其余经 `middleware.Auth` 保护, 覆盖分组, 子分组, 密钥, 任务, 仪表盘, 日志, 设置.
- `registerProxyRoutes`: `/proxy/:group_name` 依次挂 `ProxyRouteDispatcher` 与 `ProxyAuth`, 再由 `proxyServer.HandleProxy` 处理 `/*path`.
- `registerFrontendRoutes`: 启用 gzip 与 `StaticCache`, 用 `EmbedFolder` 从 `web/dist` 提供静态资源; `NoRoute` 对 `/api`, `/proxy` 前缀返回 404 JSON, 其余返回 `indexPage` 并置 no-cache 头; `NoMethod` 返回 405.

## 关键约束

代理路由先经过 `ProxyRouteDispatcher` 再鉴权, 使 `/proxy/:group_name/api/integration/info` 能在无密钥时返回集成信息, 调整顺序会破坏该行为. 新增管理类接口应放入 `registerProtectedAPIRoutes` 以自动获得鉴权; 新增公开接口需明确意识到其无鉴权. `EmbedFolder` 在 `fs.Sub(buildFS, "web/dist")` 失败时直接 `panic`, 内嵌路径必须与 `main.go` 的 `go:embed web/dist` 一致. 本目录没有测试文件, 改动后从仓库根执行 `go build ./...`, 并实际请求路由确认注册生效.
