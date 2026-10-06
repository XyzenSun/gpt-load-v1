# internal/encryption

本目录只有 `encryption.go`,定义 API Key 的加解密与哈希能力,是密钥落库的保护层.

## 主要文件与职责

`encryption.go` 定义 `Service` 接口(`Encrypt`/`Decrypt`/`Hash`)及两个实现.`NewService(key)` 在 `key == ""` 时返回 `noopService`,即直通:加解密原样返回,`Hash` 用无盐 SHA256;`key` 非空时返回 `aesService`,用 AES-256-GCM;AES 密钥由 `utils.DeriveAESKey` 通过 PBKDF2(100000 次,固定 salt `gpt-load-encryption-v1`)派生,`Encrypt` 输出 `hex(nonce+ciphertext)`,`Hash` 用 HMAC-SHA256(派生密钥).

## 关键约束

`Encrypt` 与 `Hash` 职责不同:前者可逆、带随机 nonce(同明文每次密文不同),用于落库;后者确定性、不可逆,用于 `key_hash` 列的查找与去重,不能用 `Hash` 代替 `Encrypt`.改动任一编码格式都必须同步 `internal/commands/migrate.go` 的迁移逻辑与 `key_hash` 数据.

`NewService` 对非空密钥调用 `utils.ValidatePasswordStrength`,该函数只打印 Warn 不返回错误,短密钥或弱模式仍会被接受.容器在 `internal/container/container.go` 用 `configManager.GetEncryptionKey()` 构造单例并注入,业务代码通过依赖注入拿 `encryption.Service`,不要自行 `NewService`.

<system-reminder>根级 `AGENTS.md` 的"API Key 落库前必须加密"默认指已配置 `ENCRYPTION_KEY` 的情况.本项目允许留空密钥禁用加密,此时 `NewService("")` 返回 noop,密钥明文落库;需要真正保护时必须配置密钥,不能依赖留空.</system-reminder>
