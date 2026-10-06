# internal/response

本目录统一 HTTP JSON 响应结构与分页,供各 handler 与 proxy 使用.

## 主要文件与职责

`response.go` 定义 `SuccessResponse{code, message, data}`(data 带 `omitempty`)与 `ErrorResponse{code, message}` 及发送 helper.`Success`/`SuccessI18n` 固定返回 200、`code` 为 0,`Success` 只用 `common.success`,`SuccessI18n` 接收 msgID 与模板数据;`Error` 与 `ErrorI18nFromAPIError` 采用 `APIError` 的 HTTPStatus 与 Code,`ErrorI18n` 则接收显式状态码与 code 且当前无调用方.文案经 `internal/i18n.Message` 本地化.

`pagination.go` 的 `Paginate` 对 `*gorm.DB` 执行分页:`page` 默认 1,`page_size` 默认 15、上限 1000,参数非法时回落到默认,`page_size` 超过上限时截断为 1000;先 `Count` 总数再 `Limit`/`Offset` 取数据,返回 `PaginatedResponse{items, pagination}`.

## 关键约束

业务接口响应统一走这里的 helper;登录、健康检查、导出、代理透传上游原始 JSON 等既有特例仍直接 `c.JSON`,改动时保持其语义.`Error` 系列要求传入 `errors.APIError`,由它决定状态码,不要另行硬编码.`DefaultPageSize`、`MaxPageSize` 是常量,调大上限需评估一次查询的容量与代价;`Paginate` 会对传入的 `query` 连续调用 `Count` 与 `Find`,调用方应传入未被复用的干净查询.

`internal/i18n` 另有一套输出 `{success, message, data, lang}` 的 `Success`/`Error` helper,与这里结构不同且没有调用方,不要混用;取本地化文案只用 `internal/i18n.Message`.
