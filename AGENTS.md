# GPT-Load 项目约定

本目录是 GPT-Load v1 分支的项目根目录。GPT-Load 是一个多渠道路由的 AI API 透明代理服务，为 OpenAI、Google Gemini、Anthropic Claude 等上游接口提供分组管理、密钥池轮换、加权负载均衡与故障转移。后端是 Go，前端是 Vue 3，前端构建产物经 `go:embed` 内嵌进二进制，单文件即可部署。

`README_CN.md` 记录了完整的配置项清单、代理调用示例和加密迁移步骤。只有在需要新增环境变量、调整代理协议或改动加密迁移流程时，才按章节查阅对应部分，不要整体加载它。

# 目录结构

后端代码集中在 `internal/`，按职责分层。新增代码优先归入已有目录，不要轻易新开目录。

- `main.go`：进程入口，负责启动服务或分发 `migrate-keys` 命令。
- `internal/app/`：应用生命周期管理，服务启动、后台任务编排与优雅关闭。
- `internal/container/`：uber/dig 依赖注入组合根，注册基础设施、业务服务与应用层构造函数；嵌入的 UI 资产由 `main.go` 补充注册。
- `internal/config/`：静态配置（环境变量）与动态配置（数据库热重载）管理。
- `internal/db/` 与 `internal/db/migrations/`：GORM 连接、自动迁移与历史数据修复。
- `internal/store/`：KV 存储抽象，内存实现与 Redis 实现可互换。
- `internal/syncer/`：基于 store 发布订阅的跨实例缓存同步。
- `internal/channel/`：各上游协议的适配器（openai、openai-response、gemini、anthropic）。
- `internal/keypool/`：密钥选择轮换、状态更新、定时校验与黑名单恢复。
- `internal/proxy/`：代理主流程、重试与故障转移。
- `internal/services/`：分组、子分组、密钥、日志、任务等业务逻辑。
- `internal/handler/` 与 `internal/router/`：HTTP 处理器与路由注册。
- `internal/middleware/`：认证、CORS、限流、日志、恢复等中间件。
- `internal/i18n/locales/`：后端多语言文案（zh-CN、en-US、ja-JP）。
- `internal/errors/`：统一 `APIError` 与上游错误分类。
- `internal/models/`、`internal/types/`：数据模型与共享类型定义。
- `internal/utils/`、`internal/response/`、`internal/httpclient/`、`internal/failover/`、`internal/encryption/`、`internal/commands/`、`internal/version/`：通用工具与专项能力。

`web/` 是 Vue 3 + TypeScript + Vite + naive-ui 管理前端，接口封装在 `web/src/api/`，文案在 `web/src/locales/`。`web/dist` 被 git 忽略，但后端编译依赖它。

# 构建与运行

前端产物是后端编译的前置条件。首次或修改前端后，必须先生成 `web/dist`，否则 `go build`、`go run` 会因 `go:embed` 找不到文件而失败。

```bash
make run     # 安装并构建前端，然后启动后端
make dev     # 以 -race 模式运行后端（需 web/dist 已存在）
go test ./...                       # 后端测试
(cd web && npm run lint:check && npm run format:check && npm run type-check)
make migrate-keys ARGS="--to <新密钥>"   # 密钥加密迁移，执行前停止服务并备份数据库
```

上述命令从仓库根目录执行，测试与构建仍需先获得用户批准，全量测试单独确认。代码改动提交前，后端至少执行 `go build ./...` 与改动相关包的 `go test`；前端在 `web/` 中分别执行 `npm run lint:check`、`npm run format:check`、`npm run type-check`。仅改文档时做内容与路径核对即可。

`web/package.json` 的 `check-all` 脚本内部缺少 `npm run`，修复前使用上面的三条独立检查命令。

# 架构约束

新增服务通过构造参数注入依赖，并在 `internal/container/container.go` 中 `Provide` 构造函数，复用已有服务。嵌入的 UI 资产由 `main.go` 补充 provider；已有 `db.DB` 等兼容访问点不作为新增全局单例的模式。

新增上游协议时实现 `channel.ChannelProxy` 接口，并在文件 `init()` 中调用 `channel.Register("<type>", constructor)` 完成注册，工厂与路由无需改动。

配置分静态与动态两层：环境变量在启动时读取、重启生效；系统设置与分组配置存数据库、支持热重载。动态配置以系统设置为基准，分组仅覆盖 `GroupConfig` 已定义且非 nil 的字段。系统设置默认值来自 struct tag，环境配置参与 `app_url`、`proxy_keys` 的初始化，不构成所有设置项的通用覆盖层。新增参数走配置管理层：启动期固定参数用环境变量，运行期可调参数用系统设置，按需开放分组覆盖。

持久化统一用 GORM，需同时兼容 SQLite、MySQL、PostgreSQL。模型与 `AutoMigrate` 负责建表、加列等支持的 schema 更新；历史数据回填、旧索引或列清理等显式迁移写到 `internal/db/migrations/`，在相应迁移入口登记。启动顺序为 `HandleLegacyIndexes` -> `AutoMigrate` -> `MigrateDatabase`，显式迁移必须幂等可重跑，不能用模型定义代替数据修复。

KV 与缓存一律使用 `store.Store` 接口，保证内存存储模式下同样可用，不要直接调用 Redis 客户端。跨实例状态同步使用 `syncer.CacheSyncer` 与 store 发布订阅。

服务启动时，仅 Master 执行数据库迁移、缺失系统设置的入库初始化（`EnsureSettingsInitialized`）与密钥池加载，并启动请求日志写入、日志清理、定时校验服务。Master 与 Slave 都初始化配置和分组缓存，注册管理 API、代理路由与前端页面；代理请求在任意节点都可调用日志记录入口。新增后台服务要在 `internal/app/app.go` 的 `Start`、`Stop` 中登记生命周期，并明确是否仅在 Master 运行。

上游错误处理统一走 `internal/errors`。`IsIgnorableError` 通过大小写敏感的子串匹配识别客户端断连等错误；`IsUnCounted` 将文本转小写后匹配免计数子串，当前仅覆盖 `resource has been exhausted` 与 `please reduce the length of the messages`，不代表所有限流或参数错误均免计数。调整错误分类或密钥失败逻辑时，核对对应列表及 proxy、keypool、channel 的调用路径，按需同步修改。

API Key 落库前统一经过 `encryption.Service.Encrypt`：配置 `ENCRYPTION_KEY` 时加密，留空时使用 noop 实现并明文存储。改变加密状态或更换密钥需先停止服务、备份数据库，再走 `migrate-keys` 流程。明文密钥仅用于上游认证及已认证管理员触发的密钥列表、日志查询与导出响应；应用日志、错误信息和代理响应必须脱敏，既有代码中的泄漏路径不得作为新增实现的范例。

# 编码与提交

Go 代码遵循 gofmt。注意：gofmt 对 Go 使用 Tab 缩进，此处以 gofmt 为准，优先于全局“统一使用空格缩进”的约定。前端遵循仓库内的 ESLint 与 Prettier 配置，组件名用 PascalCase，模板标签用 kebab-case。

多语言文案需同时更新 zh-CN、en-US、ja-JP 三份；前后端各自维护，不要只改一种语言。

提交信息使用 Conventional Commits，scope 用模块名或功能名，例如 `fix(keypool): 修复并发失败计数丢失`、`feat(ui): 优化移动端适配`。一个提交聚焦一件事，避免把重构与功能混在一起。git 的完整操作流程以 `git-standard` 技能为准。
