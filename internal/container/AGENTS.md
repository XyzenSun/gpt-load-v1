# internal/container 模块约定

本目录是依赖注入组合根, 只有 `container.go`. `BuildContainer()` 用 `go.uber.org/dig` 建容器, 逐个 `Provide` 各层构造函数, 任一注册失败立即返回错误. 进程入口 `main.go` 与 `internal/commands/migrate.go` 都调用它.

## 注册顺序与分组

文件按注释分为 Infrastructure (config, encryption, db, system settings, store, httpclient, channel factory), Business (services.* 与 keypool.*), Handlers, Proxy & Router, Application (`app.NewApp`) 五段. 分组只影响阅读顺序, dig 按类型惰性解析, 与注册先后无关.

## 关键约束

新增服务统一在此 `Provide` 其构造函数, 业务代码通过构造参数获取依赖, 不要在别处 `new` 或引入全局单例. 加密服务用闭包注册, 从 `configManager.GetEncryptionKey()` 构造并返回 error, 保持这种返回错误的写法, 让配置问题在启动期暴露.

`router.NewRouter` 依赖 `embed.FS` 与 `[]byte` 两个 UI 资产, 它们不在本文件注册: `main.go` 在 `BuildContainer()` 之后用匿名 provider 补充 `web/dist` 与 `index.html`. 单独复用 `BuildContainer()` 时, 只要不触发 `router` 或 `app` 的构造就不会缺少这两个依赖, `migrate-keys` 命令即属此情况. 若新增依赖嵌入资产的服务, 需同样由调用方补齐 provider.

## 维护建议

调整注册后从仓库根执行 `go build ./...` 校验依赖图, 或本地启动一次服务触发 dig 解析. 本目录没有测试文件, 不要依赖单测发现依赖缺失.
