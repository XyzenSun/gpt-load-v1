# web/src 前端维护约定

本目录是 Vue 3 + TypeScript + Vite + naive-ui 管理前端的源码,构建产物输出到 `web/dist` 并被后端 `go:embed` 内嵌.技术栈为 Vue 3、vue-router、vue-i18n、naive-ui、axios,路径别名 `@` 指向 `src`(见 `web/vite.config.ts` 与 `web/tsconfig.app.json`).

## 目录结构

- `main.ts`、`App.vue`:应用入口与根组件.
- `api/`:按业务域划分的接口封装(dashboard、keys、logs、settings).
- `components/`:通用与业务组件,`common/`、`keys/`、`logs/` 为子分组.
- `views/`:路由页面(Dashboard、Keys、Logs、Settings、Login).
- `router/index.ts`:路由表与登录守卫.
- `services/`:auth、version 等领域服务.
- `locales/`:vue-i18n 实例与 zh-CN、en-US、ja-JP 文案.
- `types/`:模型与全局类型声明(models.ts、env.d.ts).
- `utils/`:http、state、app-state、theme、display、clipboard.
- `assets/`:全局样式与 CSS 变量.

## 关键维护约束

状态管理用自研轻量方案:`utils/state.ts` 的 `useState()` 基于全局 reactive 对象,`utils/app-state.ts` 的 `appState` 承载 loading 与跨组件刷新信号.项目没有 Pinia/Vuex,新增共享状态沿用这两个文件,不要引入状态库.

所有 HTTP 请求走 `utils/http.ts` 导出的 axios 实例(baseURL `/api`、超时 60s),不要另建实例.响应拦截器直接返回 `response.data`,因此调用方拿到的是后端信封 `{code, message, data}`,读 `res.data`、`res.message`;非 GET 请求默认弹出后端文案,单次请求可传 `hideMessage: true` 抑制.出错时读取 `error.response.data.message` 提示,401(且当前不在 `/login`)会登出并跳转 `/login`.

认证信息存在 localStorage 的 `authKey`,由 `services/auth.ts` 读写;路由守卫调用其 `checkLogin()`.`utils/http.ts` 的请求拦截器也直接读取该字符串并在 401 时登出,改动登录态存储键时要同时同步这两处.

多语言基于 vue-i18n(`legacy: false`,用 `i18n.global.t` 或 `useI18n`),文案分散在 `locales/` 三份文件中,新增键要同时补齐 zh-CN、en-US、ja-JP.当前语言存 localStorage 的 `locale`,`setLocale()` 会刷新页面以便后端数据同步切换;`Accept-Language` 同时通过 axios 默认头和请求拦截器发送.

主题由 `utils/theme.ts` 管理,模式为 auto/light/dark,存 localStorage 的 `gpt-load-theme-mode`,并把 `dark`/`light` class 写到 `<html>`;naive-ui 主题与 `window.$message` 在 `components/GlobalProviders.vue` 集中配置.样式变量在 `assets/variables.css` 与 `assets/style.css` 中按明暗两套定义,新增颜色需两套都补.

## 已知问题(维护建议,勿照抄为可用命令)

`web/package.json` 的 `check-all` 脚本写作 `npm lint:check && npm format:check && npm type-check`,缺少 `run`,直接执行 `npm run check-all` 会失败.在修复前请分别运行 `npm run lint:check`、`npm run format:check`、`npm run type-check`.
