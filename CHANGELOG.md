# 更新日志

## v0.1.8

修复在 CPA 8.0.28 上接管 Responses 流式请求后返回 HTTP 502 的问题。

插件现在会正确声明 Responses 输出格式，避免响应内容在格式转换时被丢弃。（issue #11，感谢 @grain-lu 报告）
