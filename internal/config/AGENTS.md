# internal/config

本目录实现两层配置管理, 是配置的唯一入口.

- `manager.go`: 环境变量静态配置. `Manager` 实现 `types.ConfigManager`. `ReloadConfig` 先 `godotenv.Load()` 读 `.env`, 再从环境变量构建 `Config`. `Validate` 校验端口区间, `MAX_CONCURRENT_REQUESTS>=1`, `AUTH_KEY` 必填且做强度校验, `GracefulShutdownTimeout` 不足 10s 时重置为 10, CORS 开启时必须设置 `ALLOWED_ORIGINS`. `EncryptionKey` 与 `RedisDSN` 允许为空.
- `system_settings.go`: 数据库动态配置. `SystemSettingsManager` 用 `syncer.CacheSyncer[types.SystemSettings]` 缓存设置. `Initialize(store, groupManager, isMaster)` 从 `db.DB` 读 system_settings 表, 以 `DefaultSystemSettings` 为底按 json tag 反射覆盖, afterLoader 仅在 Master 触发 `groupManager.Invalidate`, 并监听 `SettingsUpdateChannel` 做跨实例同步. `EnsureSettingsInitialized` 为缺失项插入默认行, 其中 `app_url` 与 `proxy_keys` 取自环境变量或认证配置. `UpdateSettings` 先校验再按 `setting_key` upsert, 最后 `Invalidate`. `GetEffectiveConfig` 用组配置的非 nil 指针字段覆盖系统设置.

关键约束:

- 静态配置只来自环境变量且启动时读取一次; 适合启动期固定的参数放环境变量, 需要运行时热改的参数应放进 `types.SystemSettings` 走数据库.
- `ENCRYPTION_KEY` 可为空: 空值时 `encryption.NewService("")` 返回 noop 实现 (数据不加密), `Validate` 也不强制该值. 根级 'API Key 落库前必须加密' 以配置了该变量为前提, 未配置时数据以明文存储.
- 生效优先级是组配置 > 系统设置(数据库). 环境变量只参与 `app_url` 与 `proxy_keys` 的初始行, 系统设置其余字段用 `default` tag; 组配置只覆盖 `GroupConfig` 已定义且非 nil 的字段. 不要把它泛化成环境变量的全面覆盖层.
- 设置项按 json tag 反射映射, 键名必须与 `types.SystemSettings` 的 json tag 一致; 新增字段即可自动获得元数据与数据库行, 但需补齐 tag.
- 修改设置必须经 `UpdateSettings` 或 `Invalidate` 触发各实例重载, 直接改数据库不会自动生效.
- `SystemSettingsManager` 通过包级 `db.DB` 访问数据库, 依赖 app 先 `NewDB` 再 `Initialize` 的启动顺序.
- `Manager.GetEffectiveServerConfig` 当前直接返回环境配置, 服务器参数未与系统设置合并.
