# internal/proxy 模块约定

本目录是代理主流程: 接收客户端请求, 选组选密钥, 改写请求并转发到上游, 处理重试/故障转移, 响应回传与请求日志. 入口 `ProxyServer.HandleProxy` 由 `internal/router` 挂到 `/proxy/:group_name`.

主要文件: `server.go`(主流程, 重试, 日志), `response_handlers.go`(流式/普通响应回传), `request_helpers.go`(参数覆盖与错误体解压), `model_list_handler.go`(模型列表拦截与改写).

# 关键约束

重试边界: `executeRequestWithRetry` 仅在两类情况判定重试, 即 `client.Do` 返回错误, 或响应状态码命中 `group.FailoverStatusCodeMatcher`(由 `FailoverStatusCodes` 解析). 判定发生在向客户端写任何字节之前; 一旦进入 `c.Status` 与 `handleStreamingResponse`, 中途读写失败只记录并返回, 不会故障转移. 因此流式响应开始后不再重试, 中途断流直接结束. 模型列表请求即使 `isStream` 为真也会走 `handleModelListResponse` 读完整个响应.

客户端断连等可忽略错误(`errors.IsIgnorableError`)直接中止重试, 以 499 记录日志且不计入密钥失败. 失败计数不在本层累加, 统一交给 `keyProvider.UpdateStatus`(内部异步执行); 其中 `errors.IsUnCounted` 命中的错误文本会跳过失败处理, 该判定只匹配 2 条子串(`resource has been exhausted`, `please reduce the length of the messages`), 并非所有限流或参数错误都免计数.

请求生命周期: `bodyBytes` 只读一次并复用于重试与日志; `ApplyModelRedirect` 返回不同 body 时替换 `req.Body` 与 `ContentLength`. 转发前删除客户端自带的 `Authorization`, `X-Api-Key`, `X-Goog-Api-Key`, 再由 `ModifyRequest` 注入上游密钥. 流式请求用 `context.WithCancel`(无超时), 非流式用 `context.WithTimeout(cfg.RequestTimeout)`, 并 `defer cancel()`; `c.Request.Body` 读后关闭, `resp.Body` 用 `defer` 关闭. 重试是递归调用, 父帧的 defer 要等子调用返回才执行, 因此错误体重试前会被读完, 但外层的关闭与取消会延迟到递归链回溯.

参数覆盖(`applyParamOverrides`)在流式判定之前执行, 但 `isStream` 仍依据原始 `bodyBytes` 判定, 覆盖不会改变流式判断结果.

日志落库的 `KeyValue` 经 `encryptionSvc.Encrypt` 处理并记录 `KeyHash`; 加密服务在未配置密钥时是 noop, 此时记录的是明文. 错误文本回传与落库前用 `utils.RedactSecret` 对当前密钥脱敏, `logRequest` 的 `RequestType` 区分 `retry` 与 `final`, 聚合分组会把原始分组记为 parent.

# 跨模块触点

- `internal/channel`: `ChannelProxy` 全部方法; `GetChannel` 的缓存语义决定配置热更新是否生效.
- `internal/keypool`: `SelectKey`, `UpdateStatus`.
- `internal/services`: `GroupManager`, `SubGroupManager`, `RequestLogService`.
- `internal/config`, `internal/encryption`, `internal/errors`, `internal/response`, `internal/utils`.

本目录暂无测试文件, 改动后可在仓库根执行 `go build ./internal/proxy/...` 做编译校验(不发起真实上游请求).
