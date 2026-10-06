# internal/httpclient 模块约定

本目录负责按配置指纹创建并复用 `http.Client`, 供 `internal/channel` 的普通与流式客户端使用. 核心是 `HTTPClientManager`, 对外只有 `GetClient(*Config)`.

主要文件: `manager.go`(管理器, 传输层构造, 重定向凭据保护), `manager_test.go`(跨域重定向脱敏的回归测试).

# 关键约束

客户端按 `Config.getFingerprint()` 缓存: 指纹相同则复用同一 `*http.Client`, 不同则新建. 管理器没有失效或驱逐接口, 旧客户端会一直保留在 map 中; 配置变更不会使旧客户端失效, 只会为新的指纹新增条目. 新增 `Config` 字段时必须同步加入 `getFingerprint`, 否则不同配置会错误复用同一客户端.

`GetClient` 用 RWMutex 做双重检查: 快路径读锁, 未命中再取写锁并在锁内二次确认, 避免并发重复创建.

传输层固定 `DialContext` 超时为 `ConnectTimeout`, `KeepAlive` 为 30 秒. `ProxyURL` 非空则 `http.ProxyURL`, 解析失败会告警并回退到 `ProxyFromEnvironment`; 为空时同样使用环境代理. `Client.Timeout` 等于 `RequestTimeout`, 会覆盖整个请求(含读取响应体), 流式场景由调用方传 `RequestTimeout=0` 关闭.

`CheckRedirect` 固定为 `stripSensitiveOnCrossHostRedirect`: 跨主机重定向时删除 `Authorization`, `x-api-key`, `api-key`, `X-Goog-Api-Key`, `X-Auth-Token`, 同主机保留, 并把重定向上限保持在 10 次. 新增自定义凭据头时要加进 `sensitiveProxyHeaders`, 否则会在跨域重定向中泄漏.

# 跨模块触点

- `internal/channel/factory.go` 的 `newBaseChannel` 是 `Config` 的主要构造方, 普通与流式两套参数都从这里来.
- 客户端复用依赖 `models.Group.EffectiveConfig` 中的超时, 连接池与代理字段; 这些字段变化会改变指纹并触发新客户端, 但不会回收旧客户端.

验证重定向脱敏逻辑时, 可在仓库根执行 `go test ./internal/httpclient/...`, 该测试用本地 httptest 服务器, 不访问真实上游.
