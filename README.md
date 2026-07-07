# CPA 插件：CodexComp

[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[简体中文](README.md) | [English](README_EN.md)

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 插件，检测并修复 gpt-5.5 流式请求中的推理截断以减少偶发的模型降智，支持 OpenAI Responses API、OpenAI Chat Completions API 和 Anthropic Messages API 三种客户端协议

gpt-5.5 在 agent 场景下推理 token 会精确停在 518n−2（516、1034、1552……），这个截断会导致意料之外的降智问题。使用插件检测到该种截断后，通过 encrypted_content 重放自动续写推理，并将多轮折叠为单个响应，对下游客户端完全透明。

## 快速安装（Agent）

如果你使用 AI agent（自动化代理，如 Codex、Claude Code 等），把下面这段提示词发给它：

```
请帮我安装 CPA 插件 codexcomp。安装说明在 https://github.com/uf-hy/cpa-plugin-codexcomp/blob/master/SETUP.md ，请先读取这个文档再执行安装。
```

## 工作原理

插件通过 CPA 的 C ABI 插件系统拦截 gpt-5.5 流式请求，内部以 codex 格式与上游通信，每当上游完成后检查 reasoning_tokens 是否匹配 518n−2 模式。若匹配且存在 encrypted_content，则触发续写

续写轮重放原始 input 加上之前所有思考内容和一条提示消息使得模型从截断点继续而非重来

默认情况设定为最多 3 轮续写, 以通过可能的更多耗时来换取相对提升的思考长度和时间进而提升模型智力

### 异步流式与首字节延迟

gpt-5.5 high reasoning effort 模式下，模型可能需要 25-30 秒才产生第一个 SSE 事件。很多客户端（包括 Codex CLI）会在 10 秒时超时。插件使用 CPA 的异步流式模式：响应头立即返回，由 goroutine 处理折叠逻辑。上游事件一到就转发给客户端，简单问题首字节实测低于 500ms，复杂推理也会在上游产出第一个事件后立即转发。

## 接管范围

插件只拦截**同时满足以下条件**的请求：

- 模型为 `gpt-5.5`
- 客户端协议为 `openai-response`（Responses API）、`openai`（Chat Completions API）或 `claude`（Anthropic Messages API）
- 流式请求（`stream: true`）
- 不含 `previous_response_id`

插件内部统一以 codex 格式与上游通信，由 CPA 的适配层自动完成客户端协议与 codex 之间的双向翻译，对下游客户端完全透明。其他请求全部透传给 CPA 正常处理。

## 与 codexcomp / CodexCont 的区别

| | [CodexCont](https://github.com/neteroster/CodexCont) | [codexcomp](https://github.com/dzshzx/codexcomp) | 本插件 |
|---|---|---|---|
| **语言** | Python (Starlette/uvicorn) | Python (uv) | Go (C ABI 共享库) |
| **部署** | 独立本地代理 (127.0.0.1:8787) | 独立本地代理 (127.0.0.1:8787) | CPA 插件（进程内加载） |
| **集成** | 手动改 `openai_base_url` | 手动改 `openai_base_url` | CPA 自动路由，无需改配置 |
| **传输** | HTTP/SSE | WebSocket + SSE 回退 | CPA 宿主模型流（`host.model.execute_stream`） |
| **递归规避** | 不适用（独立进程） | 不适用（独立进程） | `host_callback_id` 跳过自身路由/拦截器 |
| **并发** | 单进程 | 单进程 | CPA 管理，每请求一个 goroutine |
| **配置** | `config.toml` | 零配置（uv tool） | 默认零配置，可选调试参数（C ABI 自注册） |
| **折叠逻辑** | 最初的 `518n−2` 检测 + 续写 | 改进的折叠（传输无关） | codexcomp `fold.py` 的 Go 移植 |

CodexCont 是最初的续写机制。codexcomp 将其改进为传输无关的折叠。本插件将折叠逻辑移植到 Go 并直接集成到 CPA 插件系统中，无需独立代理进程。

## 手动安装

从 [Releases](https://github.com/uf-hy/cpa-plugin-codexcomp/releases/latest) 下载对应平台的 `.so` 文件：

- `codexcomp-linux-amd64.so` — Linux x86_64（最常见）
- `codexcomp-linux-arm64.so` — Linux ARM64

```bash
# 按 CPA 运行环境的 CPU 架构二选一。
# Linux x86_64：
wget -qO <CPA_DIR>/plugins/codexcomp.so \
  "https://github.com/uf-hy/cpa-plugin-codexcomp/releases/latest/download/codexcomp-linux-amd64.so"

# Linux ARM64：
wget -qO <CPA_DIR>/plugins/codexcomp.so \
  "https://github.com/uf-hy/cpa-plugin-codexcomp/releases/latest/download/codexcomp-linux-arm64.so"
```

启用插件：

1. 将 `codexcomp.so` 放到 `<CPA_DIR>/plugins/`
2. 在 `config.yaml` 中启用插件：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    codexcomp:
      enabled: true
      priority: 1
```

3. 如果使用 Docker，在 `docker-compose.yml` 中挂载插件目录：

```yaml
volumes:
  - ./plugins:/CLIProxyAPI/plugins:ro
```

4. 重启 CPA。

更适合 AI agent（自动化代理）的完整步骤见 [SETUP.md](SETUP.md)。

## 配置

默认无需额外配置。插件通过 C ABI 自注册，自动路由。需要做 A/B 测试或排障时，可以打开下面这些参数。

```yaml
plugins:
  configs:
    codexcomp:
      marker_text: "Spend time on thinking; you do not need to use the commentary channel to report progress to me."
      max_continue: 3
      max_tier_n: 6
      truncation_step: 518
      debug_log: false
```

`marker_text` 是检测到截断后插入的续写提示，默认值仍是 `Continue thinking...`。

`max_continue` 是最多续写轮数，默认 3；设为 0 可以临时禁用续写，只保留接管和元数据路径，方便对比。

`max_tier_n` 是允许续写的最大截断层级，默认 6；设为 0 表示不限制上限。

`truncation_step` 是检测 `step*n−2` 截断模式时使用的步长，默认 518。除非在新样本里观察到稳定的新步长，否则不建议改。

`debug_log` 会通过 CPA 的 host log（宿主日志）输出配置和续写轮次信息，默认关闭，排障时再开。

上面的提示词来自 [openai/codex#30364 的相关讨论](https://github.com/openai/codex/issues/30364#issuecomment-4828984707)。它更明确地要求模型把时间花在 thinking（思考）上，不要把 commentary channel（进度汇报通道）用于报告进度。不同客户端和任务里的效果可能不同，建议按自己的场景测试后再启用。

## Metadata 注入

最终 `response.completed` 事件包含：

- `metadata.proxy_rounds` — 每轮信息（轮次号、推理 token 数、截断层级 `n`）
- `metadata.proxy_billed_usage` — 所有轮次的合计用量
- `metadata.proxy_stopped_reason` — 非自然停止时非空（`no_encrypted_content`、`max_continue`、`tier_out_of_window`）

## 基准测试

使用 [codex-candy-eval](https://github.com/haowang02/codex-candy-eval) 的糖果问题——一个触发 gpt-5.5 截断的推理深度测试。正确答案为 21。

仓库内置了流式测试脚本 `scripts/candy_eval_cpa.py`，直接对你的 CPA 端点发流式 Responses 请求：

**命令**：

```bash
python3 scripts/candy_eval_cpa.py \
  --url http://your-cpa:port/v1/responses \
  --key YOUR_KEY -n 5 -r high
```

### 无插件（baseline）

| 运行 | 推理 Token | 答案 | 正确 |
|------|-----------|------|------|
| 1    | 516       | 29   | ✗    |
| 2    | 516       | 29   | ✗    |
| 3    | 1552      | 21   | ✓    |
| 4    | 516       | 29   | ✗    |
| 5    | 3069      | 21   | ✓    |

**准确率：2/5 (40%)** — 5 个响应中有 3 个在 516 token 处截断（n=1），导致答错。

### 有插件

| 运行 | 推理 Token | 答案 | 正确 |
|------|-----------|------|------|
| 1    | 3641      | 21   | ✓    |
| 2    | 2059      | 21   | ✓    |
| 3    | 3273      | 21   | ✓    |
| 4    | 4555      | 21   | ✓    |
| 5    | 3100      | 21   | ✓    |

**准确率：5/5 (100%)** — 所有截断均被检测并续写，答案全部正确。

## 免责声明

本插件依赖非契约的模型行为（`518n−2` 截断模式和 `encrypted_content` 字段）。如果 OpenAI 改变截断模式或移除 `encrypted_content`，插件将不再触发，变为透明透传。续写轮消耗真实 token，总量记录在 `metadata.proxy_billed_usage` 中。

## 致谢

- **[CodexCont](https://github.com/neteroster/CodexCont)**（MIT）— 最初的续写机制，识别了 `518n−2` 截断模式并开创了 `encrypted_content` 重放方案
- **[codexcomp](https://github.com/dzshzx/codexcomp)**（MIT）— 改进的传输无关折叠算法；本插件直接移植自其 `fold.py`
- **[codex-candy-eval](https://github.com/haowang02/codex-candy-eval)** — 本 README 使用的推理深度基准测试
- **[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)** — 插件宿主框架，使进程内拦截成为可能
- **[LINUX DO](https://linux.do)** 社区 — gpt-5.5 “516” 推理截断/降智问题的主要讨论、定位与验证现场；感谢社区成员提供复现样例、部署反馈和测试验证

## 许可证

MIT。本插件包含来自 [CodexCont](https://github.com/neteroster/CodexCont) 和 [codexcomp](https://github.com/dzshzx/codexcomp) 的派生代码，详见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
