# internal/errors

本目录集中错误定义与错误文本分类,供 handler、proxy、services、keypool、channel 共用.

## 主要文件与职责

`errors.go` 定义 `APIError{HTTPStatus, Code, Message}` 与预定义错误;`NewAPIError` 保留状态码只换消息,`NewAPIErrorWithUpstream` 原样带上游状态码与消息.`ParseDBError` 只识别少数情况:`gorm.ErrRecordNotFound` → 404,PostgreSQL `23505` / MySQL `1062` / SQLite `unique constraint failed` → 409,其余一律 500 `DATABASE_ERROR`.

`parser.go` 的 `ParseUpstreamError` 依次尝试 `{"error":{"message"}}`、`{"error_msg"}`、`{"error"}`、`{"message"}` 四种 JSON,全部失败则返回原始 body,统一截断到 2048 字节.

`ignorable_errors.go` 的 `IsIgnorableError` 对 `err.Error()` 做大小写敏感的子串匹配(如 `context canceled`、`connection reset by peer`、`broken pipe`),用于客户端断连,不计入密钥失败.`uncounted_errors.go` 的 `IsUnCounted` 会先把错误文本转小写再匹配(大小写不敏感),当前只有两条:`resource has been exhausted`、`please reduce the length of the messages`.

## 关键约束

`IsUnCounted` 命中的子串并不等于"全部限流和参数类错误",它只覆盖上述两条,其它限流/参数错误若未命中仍会照常失败计数.要新增不计数的错误类型,必须显式往 `unCountedSubstrings` 追加子串,并同步在调用方与相关说明中体现.

调用触点:`internal/proxy/server.go` 与 `request_helpers.go` 用 `IsIgnorableError` 决定是否中止重试,`ParseUpstreamError` 还被各 `internal/channel` 适配器用于解析上游错误体,`NewAPIErrorWithUpstream` 由 `proxy/server.go` 回写;`internal/keypool/provider.go` 的 `UpdateStatus` 用 `IsUnCounted` 决定是否跳过失败处理;各 handler 与 services 大量用 `ParseDBError`.调整这两份列表会直接影响哪些错误被忽略、哪些不计入失败计数.
