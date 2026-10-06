# internal/channel 模块约定

本目录是上游协议适配器层. `channel.go` 定义 `ChannelProxy` 接口, 各实现把统一代理请求改写为目标上游的 URL, 鉴权头与模型名. 上层 `internal/proxy` 只依赖接口, 不感知具体协议.

主要文件: `channel.go`(接口), `base_channel.go`(`BaseChannel` 通用实现与加权轮询), `factory.go`(注册表与按分组缓存), `openai_channel.go`, `openai_response_channel.go`, `anthropic_channel.go`, `gemini_channel.go`.

# 关键约束

新增协议时实现 `ChannelProxy`, 并在本目录文件的 `init()` 中调用 `Register("<type>", constructor)`. 重复注册同一类型会 `panic`, 工厂与路由无需改动. `newBaseChannel` 从 `group.Upstreams` 解析上游, 从 `group.EffectiveConfig` 构造客户端, 是各实现共用的入口.

`Factory.GetChannel` 按 `group.ID` 缓存实例, 命中后调用 `IsConfigStale` 决定是否重建. `BaseChannel` 只比较构造时快照的字段(`channelType`, `TestModel`, `ValidationEndpoint`, `Upstreams`, `EffectiveConfig`, 重定向规则与 strict 开关). 新增会影响请求行为的缓存字段时, 必须同步扩展 `IsConfigStale`, 否则分组配置热更新后仍复用旧实例; 缓存重建在 `cacheLock` 内完成.

`newBaseChannel` 从 `httpclient.HTTPClientManager` 取两个客户端: 普通客户端带 `RequestTimeout`, 流式客户端 `RequestTimeout=0`, 关闭压缩并使用更大的连接池. 校验与代理路径应统一走该管理器以复用连接, 不要自行 `new http.Client`.

`BaseChannel.getUpstreamURL` 用互斥锁保护的平滑加权轮询选择上游; 权重 `<=0` 的上游在构造时被忽略. 若所有上游权重都不合法, `newBaseChannel` 不会报错, 只是返回上游列表为空的实例, 直到 `BuildUpstreamURL`/`ValidateKey` 真正选路时才因无可用上游失败.

`gemini` 的 `ModifyRequest` 在 `v1beta/openai` 路径使用 Bearer 头, 其他路径把 key 放进 URL query(`key=`), 因此传输层错误信息可能带出密钥. 鉴权头注入集中在各实现的 `ModifyRequest`, 转发层会先删除客户端凭据再由这里注入上游密钥.

模型重定向分两条路径: `ApplyModelRedirect` 改写请求, `TransformModelList` 改写模型列表响应. Gemini 原生格式走路径改写并保留 `:method` 后缀, OpenAI 兼容格式走 JSON body 改写; `ModelRedirectStrict` 为真时未命中的模型会报错或只返回配置模型.

`ValidateKey` 使用普通客户端, 超时由 `KeyValidator` 传入的 context 控制. 仅 2xx 视为有效, 其他状态码经 `errors.ParseUpstreamError` 提取原因后作为校验失败错误返回; Gemini 校验把 key 放在 query 上.

# 跨模块触点

- `models.Group`: `Upstreams`, `EffectiveConfig`, `TestModel`, `ValidationEndpoint`, `ModelRedirectMap`, `ModelRedirectStrict`, `HeaderRuleList`.
- `internal/httpclient`: `channel/factory.go` 是 `Config` 的主要构造方, 字段变更需同时检查指纹.
- `internal/utils`: `GetValidationEndpoint`, `NewHeaderVariableContext`, `ApplyHeaderRules`.
- `internal/errors`: `ParseUpstreamError`.
- `internal/container`: `NewFactory` 在此注册注入.

本目录暂无测试文件, 改动后可在仓库根执行 `go build ./internal/channel/...` 做编译校验(不发起真实上游请求).
