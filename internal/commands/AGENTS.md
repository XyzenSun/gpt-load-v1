# internal/commands

本目录只有 `migrate.go`,提供唯一子命令 `migrate-keys`,由 `main.go` 的 `runCommand` 分发.

## 主要文件与职责

`RunMigrateKeys` 解析 `--from`/`--to`,构建容器并执行 `MigrateKeysCommand.Execute`.三种场景:`--to` 启用加密、`--from` 禁用加密、两者同时给出则更换密钥(相同值报错).`--to` 会走 `ValidatePasswordStrength`,仅 Warn 不阻断.

`Execute` 流程:`HandleLegacyIndexes` + `AutoMigrate(APIKey)` → `validateAndGetScenario` → `preCheck` → `createBackupTableAndMigrate` → `verifyTempColumns` → `switchColumns` → `clearCache` → `dropTempTable`.

## 实际行为与约束

所谓"备份"是写入 `temp_migration` 临时表,不是数据库文件备份,迁移前仍须按 `README_CN.md` 停服并手工备份.整个过程不是一笔原子事务:`HandleLegacyIndexes`/`AutoMigrate`、`preCheck`、分批写入 `temp_migration`、`switchColumns` 各为独立步骤,只有单批写入与整表 `switchColumns` 用事务.原 `api_keys` 表在 `switchColumns` 成功前不变,中途失败可重跑;但 `temp_migration` 不会自动清理,依赖下次运行时 DROP 重建,`clearCache`/`dropTempTable` 失败只 Warn.

`preCheck` 仅在给出 `--from` 时才用真实密钥逐条 `Decrypt`(按批 1000,任一失败即中止);仅给 `--to` 启用加密时用 noop 直通服务,`Decrypt` 不会失败,改由 `detectIfAlreadyEncrypted` 抽样 20 条比对 `key_hash` 与 SHA256,一致性不符或 `--to` 能解出样本即报错.`verifyTempColumns` 只按行数比对,并最多再解密校验 1000 条样本,并非全量加密验证.

字段变更要同时覆盖 mysql、postgres、sqlite 三种方言分支.
