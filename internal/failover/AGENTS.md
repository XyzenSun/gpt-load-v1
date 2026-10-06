# internal/failover

本目录只有 `status_code_matcher.go`,提供 HTTP 状态码区间的解析与匹配,用于失败转移判断.

## 主要文件与职责

`StatusCodeMatcher` 内部保存一组已排序、已合并的闭区间 `[Start, End]`,`Match` 用二分查找判断状态码是否命中;零值 matcher 不匹配任何码,`IsEmpty` 判断是否为空.`ParseStatusCodeMatcher` 解析逗号分隔的配置:单项如 `404`,闭区间如 `250-260`;换行会被当作逗号,token 与 `-` 周围的空白会被忽略,空 token 被跳过.

## 关键约束

解析会做归一化与校验:每项要求 `start <= end`,状态码必须在 `100-999`,否则返回 error;成功后统一排序并合并重叠或相邻区间,因此存储的区间数不等于用户输入的项数.匹配只认整数状态码,不能匹配非 HTTP 语义的码.

调用触点:`internal/services/group_manager.go` 用 `group.EffectiveConfig.FailoverStatusCodes` 解析后挂到 `group.FailoverStatusCodeMatcher`,解析失败仅记录 Warn 并保留零值 matcher;`internal/config/system_settings.go` 用同一解析器校验 `failover_status_codes` 设置;`internal/proxy/server.go` 的 `shouldFailoverOnStatusCode` 据此决定是否按状态码重试.
