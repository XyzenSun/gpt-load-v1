# internal/store 模块约定

本目录是 KV 存储抽象层, 向业务提供统一的 `Store` 接口, 让内存实现与 Redis 实现可互换. 调用方只依赖接口, 不直接使用 Redis 客户端.

## 主要文件

- `store.go`: `Store` 接口(字符串, HASH, LIST, SET, 发布订阅, `Clear`), 以及 `Message`, `Subscription` 和可选的 `RedisPipeliner`/`Pipeliner` 接口.
- `memory.go`: `MemoryStore`, 单进程内存实现, 所有数据类型放在同一个 map 里, 按 key 做类型断言, 读取时惰性删除过期项.
- `redis.go`: `RedisStore`, 真实 Redis 实现, 所有 key 与频道统一加 `gpt-load:` 前缀.
- `factory.go`: `NewStore(cfg)`, 配置了 Redis DSN 时连接并 Ping, 否则回退内存实现.

## 关键约束

- 同一个 key 只存一种数据结构. `MemoryStore` 用类型断言区分 string/hash/list/set, 混用会返回 type mismatch 错误.
- `MemoryStore.LRem` 只实现了 `count == 0`(删除全部匹配项), 其他 count 返回错误, Redis 实现支持完整语义. 调用方统一传 0, 改动时需保证两种实现行为一致.
- key 不存在时 `Get`/`Rotate` 返回 `ErrNotFound`, 调用方用 `errors.Is` 判断, 不要匹配错误字符串.
- 发布订阅不保证投递: `MemoryStore.Publish` 在订阅者缓冲(10)满时 1 秒后丢弃消息, Redis 的 pub/sub 同样是 fire-and-forget. 不要把 pub/sub 当作可靠队列.
- `Clear` 语义有差异: Redis 只删除 `gpt-load:*` 前缀的 key, `MemoryStore` 只清空数据 map, 不动订阅者.
- 管线是可选能力: 需要批量 HSet 时对 `store.RedisPipeliner` 做类型断言, 失败则降级为逐条写入.

## 跨模块触点

key 布局由使用方定义: `keypool/provider.go` 使用 `group:<id>:active_keys`(LIST)与 `key:<id>`(HASH); `services/task_service.go` 使用 `global_task`(STRING); `services/request_log_service.go` 使用 `request_log:<uuid>`(STRING)与 `pending_log_keys`(SET); `syncer` 使用频道做失效通知. 新增 key 时复用这些命名约定, 避免前缀冲突. `app.go` 在 Master 启动时调用 `Clear`, `commands/migrate.go` 在密钥重加密后也调用它.
