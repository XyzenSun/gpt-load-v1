# .github/ISSUE_TEMPLATE 维护约定

本目录存放 GitHub Issue 模板, 当前有 `bug_report.md` 与 `feature_request.md` 两个纯 Markdown 模板, 没有 config.yml, 模板选择器使用 GitHub 默认行为.

每个模板顶部是 YAML front matter, 包含 `name`、`about`、`title`、`labels` (分别为 `bug` 与 `enhancement`)、`assignees`. `name`/`about` 采用中英双语并注明语言, 正文同样以 `/` 分隔双语标题与提示, 复选框和提示注释保持双语.

维护时遵守以下约束:

- 保持 front matter 的 `labels` 与仓库实际标签一致, 否则新建 Issue 会打上不存在的标签.
- 各模板的分节顺序与例行检查清单保持统一, 便于维护者按同一节奏处理.
- 新增模板时沿用 Markdown + front matter 的形式, 并保持与现有模板一致的中英双语结构和免责提示 (不遵循规则的 issue 可能被忽视或关闭).
- 本目录只承载模板内容, 不参与构建, 也不被代码引用.
