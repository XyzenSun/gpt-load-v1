# internal/services 模块约定

本目录承载分组, 聚合分组, 密钥, 日志与任务等业务逻辑, 处在 handler 与 keypool/proxy 之间. 分组缓存, 子分组选择, 密钥导入导出, 请求日志落库都在这里编排.

## 主要文件

- `group_manager.go`: `GroupManager`, 基于 `CacheSyncer[map[string]*models.Group]` 缓存分组; 加载时计算 EffectiveConfig, 解析失败转移状态码, HeaderRules 与模型重定向, 并组装聚合分组的子分组关系; `afterReload` 重建子分组选择器.
- `subgroup_manager.go`: `SubGroupManager`, 用平滑加权轮询为聚合分组选择子分组, 并通过 `store.LLen("group:<id>:active_keys")` 判断子分组是否有可用密钥.
- `group_service.go`: 分组校验与事务化增删改, 排序, 复制, 统计, 配置项; 定义 `I18nError`.
- `aggregate_group_service.go`: 聚合分组子分组的校验, 增删与权重更新.
- `key_service.go`: 密钥解析, 去重, 加密与批量导入导出, 测试; `key_import_service.go`, `key_delete_service.go`, `key_manual_validation_service.go` 分别封装异步任务.
- `task_service.go`: 用 store 中的单个 `global_task` key 维护全局任务状态.
- `log_service.go`, `log_cleanup_service.go`, `request_log_service.go`: 日志查询与 CSV 导出, 过期清理, 异步落库与统计聚合.

## 关键约束

- 任何分组, 子分组, 聚合分组的写操作完成后调用 `groupManager.Invalidate()`. 它只发布失效通知, 缓存重载是异步的; `GetGroupByName` 可能短暂返回旧值.
- 密钥的持久化经 `KeyProvider`(keypool)完成, 它同时维护数据库与 `group:<id>:active_keys`, `key:<id>` 缓存. `KeyService` 只对 `APIKey` 做查询(计数, 列表, 流式导出), 不要绕过 `KeyProvider` 直接写密钥行.
- 导入与删除是两条路径. 导入(`processAndCreateKeys`)先按 `EncryptionSvc.Hash` 去重, 再用 `Encrypt` 生成 `KeyValue`, 分块调 `KeyProvider.AddKeys`; 删除(`RemoveKeys`/`processAndDeleteKeys`)只把 key 值分块交给 `KeyProvider.RemoveKeys`(内部按 `Hash` 匹配 `key_hash`), 不经过 `Encrypt`. 配置 `ENCRYPTION_KEY` 时密钥加密落库, 留空则 `Encrypt`/`Decrypt` 为 noop, `key_string` 即为明文. 明文不应落日志或错误信息, 但当前 `processAndCreateKeys` 在 `Encrypt` 失败时会记录明文密钥, 改动该路径时应一并去掉.
- 异步任务在 goroutine 中执行, 通过 `TaskService.StartTask/UpdateProgress/EndTask` 汇报; `TaskService` 全局只允许一个任务, 任务状态 key 的 TTL 为 60 分钟(运行中的状态同样会过期).
- `RequestLogService` 与 `LogCleanupService` 的 `Start` 只由 Master 调用(见 `internal/app/app.go`), 启动/停止在 `app.go` 登记; `RequestLogService.Record` 仍由 proxy 在任意节点调用, 每轮读取 `RequestLogWriteIntervalMinutes`, 为 0 时同步写库.
- 分组加载依赖 `settingsManager.GetEffectiveConfig` 计算生效配置; 聚合分组统计不统计密钥数, 标准分组才统计.

## 跨模块触点

密钥能力来自 `keypool`(`KeyProvider`, `KeyValidator`), 分组数据来源是 GORM. `GroupManager` 缓存被 handler 与 proxy/middleware 共用. 生命周期上, `app.go` 的 `Start` 调用 `GroupManager.Initialize` 完成初始加载, `Stop` 调用 `GroupManager.Stop`; `LogCleanupService`/`RequestLogService` 仅 Master 启动, 也仅 Master 停止. 新增后台服务时一并登记, 并在 `internal/container/container.go` 中 Provide 构造函数.
