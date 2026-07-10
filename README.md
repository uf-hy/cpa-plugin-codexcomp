# CPA 插件：CodexComp

[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[简体中文](README.md) | [English](README_EN.md)

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 插件，检测并修复可配置模型（默认 `gpt-5.5` 和 `gpt-5.6-luna`）流式请求中的推理截断，以减少偶发的模型降智，支持 OpenAI Responses API、OpenAI Chat Completions API 和 Anthropic Messages API 三种客户端协议

gpt-5.5 在 agent 场景下推理 token 会精确停在 518n−2（516、1034、1552……），这个截断会导致意料之外的降智问题。使用插件检测到该种截断后，通过 encrypted_content 重放自动续写推理，并将多轮折叠为单个响应，对下游客户端完全透明。

## 快速安装（Agent）

如果你使用 AI agent（自动化代理，如 Codex、Claude Code 等），把下面这段提示词发给它：

```
请帮我安装 CPA 插件 codexcomp。安装说明在 https://github.com/uf-hy/cpa-plugin-codexcomp/blob/master/SETUP.md ，请先读取这个文档再执行安装。
```

人类手动安装请见 [安装章节](#安装)。

## 工作原理

插件通过 CPA 的 C ABI 插件系统拦截配置列表中的模型（默认 `gpt-5.5` 和 `gpt-5.6-luna`）流式请求，内部以 codex 格式与上游通信，每当上游完成后检查 reasoning_tokens 是否匹配 518n−2 模式。若匹配且存在 encrypted_content，则触发续写

续写轮重放原始 input 加上之前所有思考内容和一条提示消息使得模型从截断点继续而非重来

默认情况设定为最多 3 轮续写, 以通过可能的更多耗时来换取相对提升的思考长度和时间进而提升模型智力

### 异步流式与首字节延迟

gpt-5.5 high reasoning effort 模式下，模型可能需要 25-30 秒才产生第一个 SSE 事件。很多客户端（包括 Codex CLI）会在 10 秒时超时。插件使用 CPA 的异步流式模式：响应头立即返回，由 goroutine 处理折叠逻辑。上游事件一到就转发给客户端，简单问题首字节实测低于 500ms，复杂推理也会在上游产出第一个事件后立即转发。

## 接管范围

插件只拦截**同时满足以下条件**的请求：

- 模型位于 `models` 配置列表中（默认 `gpt-5.5` 和 `gpt-5.6-luna`）
- 客户端协议为 `openai-response`（Responses API）、`openai`（Chat Completions API）或 `claude`（Anthropic Messages API）
- 流式请求（`stream: true`）
- 不含 `previous_response_id`
- `input` 为数组格式（仅 `openai-response` 协议要求；字符串格式的 `input` 会直接透传，不触发续写）

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

## 安装

### 方式一：CPA WebUI 安装（推荐）

在 CPA 的 WebUI 中添加本仓库为插件源：

1. 打开 **配置面板 → 可视化编辑 → 完整 → 高级与实验 → 插件**
2. 确保插件系统为开启
3. 在「第三方插件源」里点击「添加」，填入：

```
https://raw.githubusercontent.com/uf-hy/cpa-plugin-codexcomp/master/registry.json
```

4. 在最下方点击对钩保存更改即可热重载
5. 随后在插件商店页面找到 CodexComp（可用搜索功能），点击安装即可

CPA 会自动下载对应架构的 `.so`、校验 SHA256、热重载，通常无需再次重启。Enjoy it!

### 方式二：手动安装

从 [Releases](https://github.com/uf-hy/cpa-plugin-codexcomp/releases/latest) 下载对应平台的 zip 包，解压后放到 `plugins/` 目录：

```bash
# 确认架构后下载对应 zip，解压到 plugins 目录
mkdir -p <CPA_DIR>/plugins
unzip -o codexcomp_<version>_linux_<amd64|arm64>.zip -d <CPA_DIR>/plugins/
```

在 `config.yaml` 中启用插件：

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    codexcomp:
      enabled: true
      priority: 1
```

如果使用 Docker，在 `docker-compose.yml` 中挂载插件目录：

```yaml
volumes:
  - ./plugins:/CLIProxyAPI/plugins
```

重启 CPA。

更适合 AI agent（自动化代理）的完整步骤见 [SETUP.md](SETUP.md)。

### 从源码编译

`go.mod` 中有 `replace` 指令指向相邻目录的 CLIProxyAPI，需要先 clone 依赖仓库：

```bash
git clone https://github.com/router-for-me/CLIProxyAPI.git ../CLIProxyAPI
go build -buildmode=c-shared -o codexcomp.so .
```

## 配置

默认无需额外配置。插件通过 C ABI 自注册，自动路由。需要做 A/B 测试或排障时，可以打开下面这些参数。

```yaml
plugins:
  configs:
    codexcomp:
      models:
        - gpt-5.5
        - gpt-5.6-luna
      marker_text: "Continue thinking..."
      max_continue: 3
      max_tier_n: 6
      debug_log: false
```

`marker_text` 是检测到截断后插入的续写提示，默认值是 `Continue thinking...`。

`max_continue` 是最多续写轮数，默认 3；设为 0 可以临时禁用续写，只保留接管和元数据路径，方便对比。

`max_tier_n` 是允许续写的最大截断层级，默认 6；设为 0 表示不限制上限。

`debug_log` 会通过 CPA 的 host log（宿主日志）输出配置和续写轮次信息，默认关闭，排障时再开。

`models` 是要拦截的模型标识符列表，默认接管 `gpt-5.5` 和 `gpt-5.6-luna`。已观测到 Luna 仍存在 516 截断现象，因此加入默认列表；Terra 和 Sol 暂未观测到相同问题，不默认接管。如果后续遇到截断，可以自行加入配置：

```yaml
plugins:
  configs:
    codexcomp:
      models:
        - gpt-5.5
        - gpt-5.6-luna
        - gpt-5.6-terra
        - gpt-5.6-sol
```

**实验功能，不建议使用：实际测试中效果不理想，续写轮经常只增加很少甚至 0 个推理 token。** `min_reasoning_tokens` 是可选的每模型最小推理 token 阈值配置，默认不启用、没有默认阈值。配置后会作用于该模型的所有被接管请求，不区分请求中的 reasoning effort（推理强度）；所有已折叠轮次的推理 token 总数低于配置值时，插件会尝试触发续写。键为精确模型标识符（如 `gpt-5.6-luna`），值为非负整数阈值。例如：

```yaml
plugins:
  configs:
    codexcomp:
      min_reasoning_tokens:
        gpt-5.6-luna: 1200
```

这会使 `gpt-5.6-luna` 在总推理 token 少于 1200 时尝试续写，直到达到阈值、达到 `max_continue` 限制，或上游不再返回 `encrypted_content`。它不能保证最终达到阈值；此触发条件与原有的 `518n-2` 截断触发条件是或（OR）关系。

### 直连 CPA 的稳定缓存

如果客户端直接调用 CPA 的 OpenAI 兼容接口（`/v1/chat/completions` 或 `/v1/responses`），建议每个会话都带上稳定的 `X-CPA-Session-Id` 请求头：

```http
X-CPA-Session-Id: your-stable-session-id
```

这个值用于生成上游 `prompt_cache_key`，让同一会话里的多轮请求稳定命中提示缓存。插件也兼容 `X-CodexComp-Session-Id` 和旧的 `X-Claude-Code-Session-Id`，但新接入建议使用 `X-CPA-Session-Id`。

如果想尝试更强的续写提示，可以参考 [openai/codex#30364 的相关讨论](https://github.com/openai/codex/issues/30364#issuecomment-4828984707)，把 `marker_text` 换成 `Spend time on thinking; you do not need to use the commentary channel to report progress to me.`。它更明确地要求模型把时间花在 thinking（思考）上，不要把 commentary channel（进度汇报通道）用于报告进度。不同客户端和任务里的效果可能不同，建议按自己的场景测试后再启用。

## Metadata 注入

最终 `response.completed` 事件包含：

- `metadata.proxy_rounds` — 每轮信息（轮次号、推理 token 数、截断层级 `n`；触发续写的轮次会额外包含 `continue_reason`，例如 `truncation` 或 `low_reasoning_tokens`）
- `metadata.proxy_billed_usage` — 所有轮次的合计用量
- `metadata.proxy_stopped_reason` — 非自然停止时非空（`no_encrypted_content`、`max_continue`、`tier_out_of_window`）

> **注意**：以上 metadata 字段仅在 `openai-response`（Responses API）协议下保证可见。对于 `openai`（Chat Completions API）和 `claude`（Anthropic Messages API）协议，CPA 的协议翻译层不会传递 `response.completed` 中的 metadata，因此客户端无法获取 `proxy_rounds` 等字段。续写功能本身在所有协议下均正常工作，只是诊断信息仅在 Responses API 中可观测。

## 基准测试

使用 [codex-candy-eval](https://github.com/haowang02/codex-candy-eval) 的糖果问题——一个触发 gpt-5.5 截断的推理深度测试。正确答案为 21。

仓库内置了多协议测试脚本 `scripts/candy_eval.py`，支持 `openai-response`、`openai-chat`、`anthropic` 三种客户端协议，可并发测试：

**命令**：

```bash
python3 scripts/candy_eval.py \
  --url http://your-cpa:port --key YOUR_KEY \
  --protocol openai-response --rounds 5 -r high
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
