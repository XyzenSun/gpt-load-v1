# internal/version 维护约定

本目录只有一个文件 `version.go`, 导出进程级版本号 `var Version = "1.0.0"`. 它是编译期注入点: Dockerfile 与 release 系列工作流都用 `-ldflags "-X gpt-load/internal/version.Version=<tag>"` 覆盖该变量, 源码里的 `1.0.0` 只是本地构建的兜底值.

维护时遵守以下约束:

- 不要为发版修改源码中的默认值, 版本由构建参数注入; 手工改默认值会让本地构建与发布产物的版本口径不一致.
- 包路径与变量名 `internal/version.Version` 是注入契约, Dockerfile 与 `.github/workflows/release-*.yml` 均按此路径写入. 重命名包或变量必须同步改这些构建脚本, 否则 `-X` 会静默失效、版本回退到默认值.
- 该变量目前仅被 `internal/app/app.go` 在启动日志中读取. 新增消费方时直接读取本包变量, 不要再定义第二份版本来源.
- 前端版本是另一条链路, 来自 Vite 的 `VITE_VERSION` (见 `web/src/services/version.ts`). 构建脚本一般让两者取同一个 tag, 改动注入方式时注意保持前后端一致.
