# internal/app 模块约定

本目录是应用生命周期层, 只有 `app.go`. `App` 结构体持有 gin 引擎, 配置管理器, 各业务服务与 `*http.Server`, 负责把 dig 装配好的依赖编排成一次完整的启动与优雅关闭. 进程入口 `main.go` 通过 dig 取出 `*app.App`, 调用 `Start()` 后等待 SIGINT/SIGTERM, 再调用 `Stop(ctx)`.

## 启动顺序与主从分支

`Start()` 是非阻塞调用, 内部把 `ListenAndServe` 放到独立 goroutine. 启动顺序存在实际依赖, 调整时保持: 先初始化 i18n; 当 `configManager.IsMaster()` 为真时依次执行 `storage.Clear()`, `db.HandleLegacyIndexes`, `db.AutoMigrate` (登记 `SystemSetting`/`Group`/`GroupSubGroup`/`APIKey`/`RequestLog`/`GroupHourlyStat`), `db.MigrateDatabase`, `settingsManager.EnsureSettingsInitialized`, `settingsManager.Initialize`, `keyPoolProvider.LoadKeysFromDB`, 最后启动 `requestLogService`/`logCleanupService`/`cronChecker`; Slave 分支只执行 `settingsManager.Initialize`. 之后两条分支统一执行 `DisplayServerConfig()`, `groupManager.Initialize()`, 再依据 `GetEffectiveServerConfig()` 构造并启动 `http.Server`.

`settingsManager.Initialize` 必须早于 `groupManager.Initialize`, 后者加载分组时要向它取有效配置. 新增只应在 Master 运行的后台任务时, 在 `Start()` 的 Master 分支启动, 并在 `Stop()` 的 `stoppableServices` 中登记, 否则关闭阶段不会等待它.

## 关闭流程

`Stop(ctx)` 把总超时 `GracefulShutdownTimeout` 中固定预留 5 秒给后台服务, 其余给 HTTP `Shutdown`; HTTP 超时会再 `Close()` 强制断开. 随后用 `sync.WaitGroup` 并发停止 `groupManager`, `settingsManager`, Master 下的 `cronChecker`/`logCleanupService`/`requestLogService`, 最后 `storage.Close()`.

`Start()` 用 `configManager.IsMaster()` 判断主从, `Stop()` 用 `GetEffectiveServerConfig()` 返回的 `IsMaster`; 两者当前都取自环境派生的同一份 `ServerConfig`.

## 维护建议

新增依赖通过 `AppParams` (dig.In) 注入并在 `NewApp` 中赋值, 不要在包内直接构造服务. 改动后从仓库根执行 `go build ./...` 与 `go test ./internal/app/...`; 本目录当前没有测试文件, 上述命令只能确认可构建, 不代表已有测试覆盖.
