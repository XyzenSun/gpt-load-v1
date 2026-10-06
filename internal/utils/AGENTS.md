# internal/utils

本目录是跨模块通用工具集,按用途分文件,被 handler、proxy、channel、services、commands、container 广泛引用.

## 主要文件与职责

`string_utils.go`:敏感串处理.`MaskAPIKey` 对长度不超过 8 的串原样返回,否则前 4 位 + `****` + 后 4 位;`RedactSecret` 用掩码形式替换文本中出现的密钥;另有 `TruncateString`、`SplitAndTrim`、`StringToSet`.

`password_utils.go`:`ValidatePasswordStrength` 只打印 Warn(长度不足 16 或命中弱模式均不报错);`DeriveAESKey` 用 PBKDF2-SHA256(100000 次,固定 salt)派生 32 字节密钥.

`compression_utils.go`:`Decompressor` 注册表内置 gzip、br、deflate、zstd;`DecompressResponse` 按 `Content-Encoding` 解压,编码未知或解压失败时返回原数据而不报错,新增算法用 `RegisterDecompressor`.

`header_utils.go`:`ResolveHeaderVariables` 替换 `${CLIENT_IP}`、`${TIMESTAMP_MS}`、`${TIMESTAMP_S}`、`${GROUP_NAME}`、`${API_KEY}` 等占位符;`ApplyHeaderRules` 按 `remove`/`set` 修改请求头.`${API_KEY}` 会注入明文密钥,配置 header 规则时须警惕落库与日志泄漏.

`config_utils.go`:用反射和 struct tag 生成系统设置元数据与默认值(`GenerateSettingsMetadata`、`DefaultSystemSettings`、`SetFieldFromString`);环境变量解析(`ParseInteger`、`ParseBoolean`、`ParseArray`、`GetEnvOrDefault`);`GetValidationEndpoint` 按渠道返回默认校验路径.

`logger_utils.go`:`SetupLogger` 依据配置设置日志级别、JSON/Text 格式与可选文件输出.

## 关键约束

密钥的存储与展示走两条路径:落库用 `internal/encryption` 的 `Encrypt`(`MaskAPIKey` 不能代替加密),任何要写日志、错误信息或回传的密钥文本先经 `MaskAPIKey`/`RedactSecret`;上游错误文本可能含密钥(如 Gemini 把 key 放进 URL),`internal/proxy` 已对其脱敏.`DeriveAESKey` 的 salt 固定,擅自更改会让历史密文与 `key_hash` 全部失配.环境变量解析在启动时读取、不热重载.测试只在 `string_utils_test.go` 覆盖 `RedactSecret`,不要假设其它函数已有测试;`TruncateString` 与 `errors.truncateString` 是两份独立实现.
