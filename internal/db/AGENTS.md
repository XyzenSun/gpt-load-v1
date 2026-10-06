# internal/db

本目录负责数据库连接与历史数据迁移.

- `database.go`: `NewDB` 根据 DSN 创建并返回 `*gorm.DB`, 同时把连接赋给包级全局 `DB`. DSN 以 `postgres://` 或 `postgresql://` 开头走 postgres 驱动, 含 `@tcp` 走 mysql, 其余按 SQLite 处理并追加 `?_busy_timeout=5000` 且创建父目录. 连接池固定为 MaxIdleConns=50, MaxOpenConns=500, ConnMaxLifetime=1h, `PrepareStmt=true`; 仅当日志级别为 `debug` 时才挂 SQL logger.
- `migrations/`: 历史数据修复. `migration.go` 的 `MigrateDatabase` 按顺序调用各迁移, `HandleLegacyIndexes` 清理旧版本索引 (MySQL 用 information_schema 判断, 其它方言用 `DROP INDEX IF EXISTS`). 现有迁移为 `v1_0_22_DropRetriesColumn` 与 `v1_1_0_AddKeyHashColumn`.
- 调用点: `internal/app/app.go` 仅在 Master 启动时执行 `HandleLegacyIndexes` -> `AutoMigrate` -> `MigrateDatabase`; `internal/commands/migrate.go` 的 migrate-keys 也会先做 `HandleLegacyIndexes` 与 `AutoMigrate`.

关键约束:

- 职责分工明确: `AutoMigrate` 负责 schema (建表与加列, 模型清单在 `internal/app/app.go`), `MigrateDatabase` 负责历史数据修复 (如回填 `key_hash`). 不要在 `AutoMigrate` 的模型定义里做数据修复.
- 迁移必须在每次 Master 启动时幂等可重跑: 先检测列或行是否存在再操作, 参考 `v1_0_22` 的 `HasColumn` 与 `v1_1_0` 的计数跳过写法. 新增迁移优先新文件并在 `MigrateDatabase` 登记; 确实要修复已有迁移时, 需确认仍可幂等重跑且不破坏历史兼容.
- 必须兼容 SQLite / MySQL / PostgreSQL, 方言相关 SQL 用 `db.Dialector.Name()` 分支, 不要硬编码单一方言.
- `NewDB` 同时返回注入用连接并赋值包级 `db.DB`, 保留这一兼容行为; `internal/config/system_settings.go` 仍直接读取它. 新增依赖优先走构造函数注入, 避免重复创建连接池.
- DSN 探测顺序与 SQLite 目录创建属于启动关键路径, 调整前确认三种数据库均可跑通.
- `internal/db/migrations` 的约定在本文件一并说明, 保持单一入口.
