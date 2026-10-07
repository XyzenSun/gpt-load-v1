# .github/workflows 维护约定

本目录是 GitHub Actions 工作流定义,共四个文件:`docker-build.yml` 构建并推送多架构镜像,`release-linux.yml`、`release-macos.yml`、`release-windows.yml` 分别产出三个平台的二进制并创建 draft release.

## 触发条件

`docker-build.yml` 只在推送 `v1.*` 标签时触发,用于 v1 分支镜像发布.三个 `release-*.yml` 都在推送任意标签(`*`)时触发,因此打一个 `v1.*` 标签会同时启动 Docker 构建和三个平台构建.

## v1 镜像标签规则

镜像仅发布到当前仓库对应的 GHCR, 通过 Bash 将 `${{ github.repository }}` 转为小写; 当前地址为 `ghcr.io/xyzensun/gpt-load-v1`. 使用 `GITHUB_TOKEN` 登录, 不依赖 Docker Hub 凭据. `flavor` 设置 `latest=false`.`docker/metadata-action` 生成的标签有三类:`type=ref,event=tag` 取 Git 标签本身;`type=raw,value=1,enable=${{ !contains(github.ref, '-') }}` 仅在标签不含连字符(即正式版)时生成 `1`;`type=raw,value=beta,enable=${{ contains(github.ref, '-beta') }}` 仅在标签含 `-beta` 时生成 `beta`.因此 `1` 代表稳定版,beta 版不会获得该标签.

## 分平台构建

Docker 在 `linux/amd64,linux/arm64` 双架构构建,通过 `docker/setup-qemu-action` 提供 arm64 支持,构建参数 `VERSION=${{ github.ref_name }}` 传给 Dockerfile.

三个 release 工作流先构建前端(`working-directory: ./web`,执行 `npm ci && VITE_VERSION=${{ github.ref_name }} npm run build`),再用 Go 1.25.x 编译后端并通过 `-ldflags "-s -w -X gpt-load/internal/version.Version=${{ github.ref_name }}"` 注入版本.产出:Linux 为 `linux/amd64` 原生与 `linux/arm64` 交叉(`CGO_ENABLED=0`);macOS 为 `darwin/arm64` 原生与 `darwin/amd64` 交叉;Windows 仅 `windows/amd64`.

## 维护约束

版本注入路径 `gpt-load/internal/version.Version` 必须与 `internal/version` 包保持一致,重命名该包或变量要同步改三个 release 工作流与 `Dockerfile`(`docker-build.yml` 只负责把标签作为 `VERSION` 构建参数传下去).

新增交叉编译目标时按现有模式补 `CGO_ENABLED=0 GOOS/GOARCH`,原生构建保留默认;并确认前端构建与版本注入在同一个 job 内完成.

发布步骤都带有 `if: startsWith(github.ref, 'refs/tags/')`、`draft: true` 与 `generate_release_notes: true`,改动发布行为时保留这些语义.

修改 YAML 时注意缩进与表达式大小写,`github.ref_name` 是标签名(含 `v` 前缀),不要在注入时自行拼接前缀.
