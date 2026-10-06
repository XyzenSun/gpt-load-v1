# internal/models

本目录存放 GORM 数据模型与 API 传输结构, 是整个服务的数据契约层.

- `types.go`: 数据表模型与响应 DTO. 表模型有 `SystemSetting` (system_settings), `Group` (groups), `GroupSubGroup` (group_sub_groups, 聚合分组与子分组的关联), `APIKey` (api_keys), `RequestLog` (request_logs), `GroupHourlyStat` (group_hourly_stats). `GroupConfig` 表示组级配置覆盖, `HeaderRule` 表示请求头规则, 另有 `SubGroupInfo` / `ParentAggregateGroupInfo` / `DashboardStatsResponse` 等响应结构.
- `setting_info.go`: `SystemSettingInfo` 与 `CategorizedSettings`, 是系统设置的元数据响应.
- 本包 import `internal/failover` 与 `internal/types`.

关键约束:

- 这些 GORM 定义是 `AutoMigrate` 的 schema 来源, 调用点见 `internal/app/app.go` (Master 启动) 与 `internal/commands/migrate.go`. 启动链路先 `HandleLegacyIndexes` 再 `AutoMigrate` 建表改列, 最后由 `db.MigrateDatabase` 做历史数据修复. 改字段 tag 只影响 schema; 存量数据的结构变更或回填必须新增 `internal/db/migrations` 迁移, 不要靠改模型 tag 代替数据迁移.
- `GroupConfig` 全字段为指针, nil 表示不覆盖. `SystemSettingsManager.GetEffectiveConfig` 只对非 nil 指针字段用组配置覆盖系统设置. 保持指针语义, 不要改成值类型.
- `GroupConfig` 的字段名与 json tag 必须与 `types.SystemSettings` 的对应项一致, 组级覆盖合并按字段名匹配, 覆盖校验按 json tag 匹配.
- 标记 `gorm:"-"` 的字段是运行时或缓存字段, 不入库: `Group` 的 `EffectiveConfig` / `Endpoint` / `ProxyKeysMap` / `HeaderRuleList` / `ModelRedirectMap` / `FailoverStatusCodeMatcher` / `SubGroups`, 以及 `GroupSubGroup.SubGroupName`.
- 需同时兼容 SQLite / MySQL / PostgreSQL, 使用可移植的 gorm type tag, 避免方言专有类型.
- json tag 是 API 线格式契约, 改动会影响前端 `web/src`.
- `APIKey.KeyValue` 是可加密字段, `KeyHash` 由迁移 v1.1.0 回填; `RequestLog` 也有 `key_hash` 索引.
