# internal/keypool 模块约定

本目录管理密钥池: 从 store 原子选择并轮换密钥, 异步更新密钥状态, 定时校验无效密钥并恢复黑名单. `KeyProvider` 同时维护 DB(持久化)与 `store.Store`(热路径缓存与活跃列表), 两者需要保持一致.

主要文件: `provider.go`(密钥池与状态机), `validator.go`(单次/批量校验), `cron_checker.go`(后台定时校验).

# 关键约束

存储布局固定: `key:<id>` HASH 存明细(含 `key_string`, `status`, `failure_count`), `group:<id>:active_keys` LIST 存活跃 ID. `SelectKey` 用 `store.Rotate` 原子取用后再从 HASH 解密 `key_string`; 解密失败时按原值使用以兼容未加密的历史数据, 因此不能假设取出的值一定是密文.

`UpdateStatus` 立即返回, 实际更新在 goroutine 中执行, 调用方不能假设返回时状态已落库. 失败累加先 `HIncrBy failure_count`(store 内原子), 再以 `CASE WHEN failure_count < ? THEN ? ELSE failure_count END` 更新 DB, 避免并发回退计数; 达到 `BlacklistThreshold`(>0)时置为 `invalid` 并从活跃列表 `LRem`. 成功恢复时先 `LRem` 再 `LPush`, 防止活跃列表出现重复项.

并发删除已处理: `handleFailure` 在 HASH 的 `id` 为空时直接返回; DB 更新 `RowsAffected==0` 且确认行不存在时清理对应 HASH. 新增状态更新逻辑时要保留这类缺失校验.

`executeTransactionWithRetry` 只对包含 `database is locked` 的错误重试(最多 3 次), 其他错误立即返回. `AddKeys`, `RestoreKeys`, `RestoreMultipleKeys`, `RemoveKeys` 在 DB 事务内完成持久化与 store 同步, store 失败会回滚事务.

`RemoveKeysFromStore` 会删除整条 `active_keys` 列表再逐个删除 HASH, 供 `GroupService.DeleteGroup` 清理缓存使用, 不做定向 `LRem`, 调用前应确认没有并发写入需求.

`LoadKeysFromDB` 只在 Master 启动时由 `app.App.Start` 调用, 分批写入并在可用时走 Redis pipeline. `CronChecker` 的 `Start`/`Stop` 也只在 Master 注册(见 `app.go`); 它以 5 分钟为 tick, 跳过聚合分组, 只校验 `invalid` 状态的密钥, 按 `KeyValidationConcurrency` 并发, 校验前先 `Decrypt`, 每个分组结束时更新 `last_validated_at`.

`KeyValidator.ValidateSingleKey` 在 `EffectiveConfig.AppUrl` 为空时重新解析配置, 超时来自 `KeyValidationTimeoutSeconds`, 校验结果同样经 `UpdateStatus` 生效. 批量校验 `TestMultipleKeys` 先按 `key_hash` 匹配库内密钥, 库中不存在的直接判为无效且不发起请求.

# 跨模块触点

- `internal/store`: `Rotate`, `HIncrBy`, `HSet`, `LRem`, `LPush`, `RedisPipeliner`.
- `internal/channel`: `Factory.GetChannel`, `ChannelProxy.ValidateKey`.
- `internal/services`: `KeyService` 调用增删改恢复方法, `GroupService.DeleteGroup` 调用 `RemoveKeysFromStore`.
- `internal/encryption`: 加解密与 `Hash`; 未配置密钥时为 noop, 此时 `key_string` 与日志中的密钥为明文.
- `internal/config`, `internal/app`, `internal/errors`.

本目录暂无测试文件, 改动后可在仓库根执行 `go build ./internal/keypool/...` 做编译校验(不发起真实上游请求).
