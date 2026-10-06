# internal/i18n 维护约定

本目录是后端多语言层. `i18n.go` 负责初始化 go-i18n bundle、解析 `Accept-Language` 与提供 `T()` 翻译; `middleware.go` 把 localizer 注入 gin.Context, 并给出取文案的辅助函数; `locales/` 下是 zh-CN、en-US、ja-JP 三份 `map[string]string` 文案, 包名统一为 `locales`.

## 目录与职责

- `i18n.go`: `Init()` 加载语言、`GetLocalizer()` 解析请求语言、`T()` 执行翻译, 内部的 `getMessages()` 按语言选 map.
- `middleware.go`: `Middleware()` 读取请求头并写入 Context, `GetLocalizerFromContext`/`GetLangFromContext` 取回, `Message()` 供 handler 直接取文案.
- `locales/*.go`: `MessagesZhCN`/`MessagesEnUS`/`MessagesJaJP`, 键名如 `group.created`、`validation.invalid_channel_type`.

## 关键维护约束

三个 locale map 的键必须完全一致, 新增文案时同时补齐三份. 缺少某个键时 `T()` 会捕获错误并原样返回键名, 界面会直接显示 `group.not_found` 这类标识符而不报错, 因此不补齐不会被编译或测试拦住.

键名约定为 `<模块>.<动作>` 的小写点分结构; 带占位符的文案用 go-i18n 的 `{{.name}}` 语法, 调用 `T()`/`Message()` 时传入 `map[string]any`.

`Accept-Language` 只取请求头里的第一个语言, 再经 `normalizeLanguageCode` 归一化 (按 `zh`/`en`/`ja` 前缀映射). 要让新语言生效, 需同时改 `Init()` 的 `languages` 切片、`getMessages()` 的 switch, 并在 `locales/` 增加对应 map.

`Middleware()` 在 `internal/router/router.go` 中挂到 `/api` 分组, 业务 handler 用 `i18n.Message(c, ...)` 取文案, 不要硬编码中文字符串. 新增接口响应统一走 `internal/response` 的 helper (输出 `{code, message, data}`); 本目录自带的 `Success`/`Error` 等 helper 输出另一套 `{success, message, data, lang}` 结构, 不要混用两套结构.

前端文案在 `web/src/locales/` 独立维护, 与本目录不共享数据, 改动用户可见文案时通常前后端两侧都要改.
