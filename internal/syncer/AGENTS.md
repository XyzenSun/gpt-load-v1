# internal/syncer 模块约定

本目录只有一个通用组件, 负责基于 `store.Store` 发布订阅的跨实例内存缓存同步. `GroupManager` 与 `SystemSettingsManager` 都复用它.

## 主要文件

- `cache_syncer.go`: 泛型 `CacheSyncer[T]`, `LoaderFunc[T]`, 以及 `NewCacheSyncer`/`Get`/`Invalidate`/`Stop`.

## 关键约束

- 构造时同步执行一次 `loader` 完成初始加载, 失败则 `NewCacheSyncer` 返回错误, 监听 goroutine 不会启动; 调用方要在依赖就绪后再创建.
- `Invalidate` 只向频道发布一条 `reload`, 不会在本地立即重载. 真正重载发生在所有订阅者(包括发布者自身, 因为它也订阅了该频道)的监听循环里, 因此写库后缓存更新是最终一致的, 不要假设 `Get` 立刻返回新值.
- `reload` 在加锁赋值后, 锁外调用 `afterReload(newValue)` 钩子; 钩子可能重复, 并发触发, 必须幂等, 且不能假设持有写锁.
- `Stop` 关闭 `stopChan` 并等待监听 goroutine 退出, 只应调用一次; 停止期间仍可能有一次在途重载.
- 监听循环的容错: `store` 为 nil 时直接退出; 订阅失败 5 秒后重试; 频道关闭后 2 秒重订阅.
- 缓存值由钩子与 `Get` 的调用方保证不可变, `Get` 只加读锁返回引用.

## 跨模块触点

`services.GroupManager` 使用频道 `groups:updated`, `afterReload` 重建子分组选择器; `config.SystemSettingsManager` 使用 `SettingsUpdateChannel`, 其 `afterReload` 仅在 Master 上触发分组缓存失效. 新增需要跨实例同步的缓存实体时复用本组件, 并相应登记 `Stop`.
