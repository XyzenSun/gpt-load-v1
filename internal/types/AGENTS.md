# internal/types

本目录定义全局共享的类型与接口, 是 internal 依赖图的叶子节点, 目前只有 types.go, 不 import 任何 internal 包.

- `ConfigManager`: 配置读取契约, 由 `internal/config.Manager` 实现, 服务通过它获取认证 / CORS / 日志 / 数据库 / 加密等配置.
- `SystemSettings`: 全部系统设置的单一结构, 是反射驱动配置机制的核心.
- `ServerConfig` / `AuthConfig` / `CORSConfig` / `PerformanceConfig` / `LogConfig` / `DatabaseConfig`: 环境变量配置的值对象.
- `RetryError`: 代理重试过程中的错误载体.

关键约束:

- `SystemSettings` 的 struct tag 是反射契约. `json` 是持久化键 (system_settings 表行键) 与组级覆盖键, `default` / `validate` / `name` / `category` / `desc` 被 `internal/utils/config_utils.go` 与 `internal/config/system_settings.go` 反射读取, 用于生成默认值, 元数据与校验规则. 新增设置项必须补齐这些 tag, 且 json tag 要与 `internal/models.GroupConfig` 的对应字段一致.
- `SystemSettings.ProxyKeysMap` 标记 `json:"-"`, 是运行时缓存派生字段, 不参与序列化与数据库存储.
- 保持本包零 internal 依赖, 引入其他 internal 包会形成导入环.
- 改动 `ConfigManager` 接口需要同步 `internal/config.Manager` 的实现以及 `internal/container` 的注入.
