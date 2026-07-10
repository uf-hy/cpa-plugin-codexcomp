# CPA Plugin: CodexComp

[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[简体中文](README.md) | [English](README_EN.md)

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that detects and repairs reasoning truncation for configurable models (defaults: `gpt-5.5` and `gpt-5.6-luna`) in streaming requests, supporting OpenAI Responses API, OpenAI Chat Completions API, and Anthropic Messages API client protocols

In agent scenarios, gpt-5.5 reasoning tokens can stop exactly at 518n−2 (516, 1034, 1552, ...). This truncation can cause unexpected degradation. When the plugin detects this kind of truncation, it replays encrypted_content to continue reasoning, then folds multiple rounds into a single response that stays fully transparent to the downstream client.

## Quick Install (Agent)

If you're using an AI agent (Codex, Claude Code, etc.), send it this prompt:

```
Please install the CPA plugin codexcomp for me. Installation instructions are at https://github.com/uf-hy/cpa-plugin-codexcomp/blob/master/SETUP.md — read that document first, then proceed with installation.
```

For manual installation by humans, see [Installation](#installation).

## How It Works

The plugin intercepts streaming requests for models in the configured list (defaults: `gpt-5.5` and `gpt-5.6-luna`) through CPA's C ABI plugin system, communicating with the upstream in codex format internally. Each time the upstream request finishes, it checks whether reasoning_tokens matches the 518n−2 pattern. If it matches and encrypted_content exists, it triggers continuation

The continuation round replays the original input, all previous thinking content, and a prompt message so the model continues from the truncation point rather than starting over

By default, it allows up to 3 continuation rounds, trading possible extra time for relatively more thinking length and time, thereby improving model intelligence

### Async Streaming & First-Byte Latency

gpt-5.5 with high reasoning effort can take 25-30 seconds before the first SSE event. Many clients (including Codex CLI) timeout at 10 seconds. This plugin uses CPA's async streaming mode: the response header is returned immediately, and a goroutine handles the fold logic. Upstream events are forwarded to the client as soon as they arrive — simple queries see first-byte under 500ms, and complex reasoning gets the first event forwarded immediately once upstream produces it.

## Scope

The plugin only intercepts requests that match **all** of:

- Model is included in the `models` configuration list (defaults: `gpt-5.5` and `gpt-5.6-luna`)
- Client protocol is `openai-response` (Responses API), `openai` (Chat Completions API), or `claude` (Anthropic Messages API)
- Request is streaming (`stream: true`)
- No `previous_response_id` present
- `input` is an array (only required for `openai-response` protocol; string `input` passes through unchanged, no continuation triggered)

The plugin communicates with the upstream exclusively in codex format. CPA's adapter layer automatically handles bidirectional translation between the client protocol and codex, making the fold transparent to downstream clients. All other requests pass through to CPA's normal routing.

## How This Differs From codexcomp / CodexCont

| | [CodexCont](https://github.com/neteroster/CodexCont) | [codexcomp](https://github.com/dzshzx/codexcomp) | This Plugin |
|---|---|---|---|
| **Language** | Python (Starlette/uvicorn) | Python (uv) | Go (C ABI shared library) |
| **Deployment** | Standalone local proxy (127.0.0.1:8787) | Standalone local proxy (127.0.0.1:8787) | CPA plugin (loaded in-process) |
| **Integration** | Manual `openai_base_url` rewrite | Manual `openai_base_url` rewrite | Auto-routed by CPA, no config change |
| **Transport** | HTTP/SSE | WebSocket + SSE fallback | CPA host model stream (`host.model.execute_stream`) |
| **Recursion guard** | N/A (separate process) | N/A (separate process) | `host_callback_id` skips own router/interceptors |
| **Concurrency** | Single process | Single process | CPA-managed, plugin goroutine per request |
| **Config** | `config.toml` | Zero-config (uv tool) | Zero-config by default, optional debug knobs (self-registers via C ABI) |
| **Fold logic** | Original `518n−2` detection + continuation | Refined fold (transport-agnostic) | Go port of codexcomp's `fold.py` |

CodexCont is the original continuation mechanism. codexcomp refined it into a transport-agnostic fold. This plugin ports that fold logic to Go and integrates it directly into CPA's plugin system, eliminating the need for a separate proxy process.

## Installation

### Option 1: CPA WebUI (recommended)

CodexComp is listed in the [official CLIProxyAPI Plugins Store](https://github.com/router-for-me/CLIProxyAPI-Plugins-Store/pull/24):

1. Open **Config Panel → Visual Editor → Full → Advanced & Experimental → Plugins**
2. Ensure the plugin system is enabled
3. Search for CodexComp on the plugin store page and click install
4. If the entry is not visible yet, refresh the store or upgrade CPA. As a fallback, add the following URL as a third-party plugin source:

```text
https://raw.githubusercontent.com/uf-hy/cpa-plugin-codexcomp/master/registry.json
```

CPA will download the dynamic library matching the current system and architecture, verify SHA256, and hot-reload — usually no second restart needed. Enjoy it!

### Option 2: Manual Installation

Download the latest zip from [Releases](https://github.com/uf-hy/cpa-plugin-codexcomp/releases/latest), extract to the `plugins/` directory:

| CPA runtime OS | Architecture | Asset | Dynamic library |
|---|---|---|---|
| Linux | amd64 / arm64 | `codexcomp_<version>_linux_<arch>.zip` | `codexcomp.so` |
| macOS | amd64 / arm64 | `codexcomp_<version>_darwin_<arch>.zip` | `codexcomp.dylib` |
| Windows | amd64 / arm64 | `codexcomp_<version>_windows_<arch>.zip` | `codexcomp.dll` |
| FreeBSD | amd64 | `codexcomp_<version>_freebsd_amd64.zip` | `codexcomp.so` |

> CLIProxyAPI's official FreeBSD arm64 asset is a `no-plugin` build, so no matching plugin asset is published.

```bash
# Linux, macOS, or FreeBSD
mkdir -p <CPA_DIR>/plugins
unzip -o "codexcomp_<version>_<goos>_<arch>.zip" -d <CPA_DIR>/plugins/
```

Native Windows releases target amd64 and arm64. Extract one with PowerShell:

```powershell
New-Item -ItemType Directory -Force -Path '<CPA_DIR>\plugins' | Out-Null
Expand-Archive -LiteralPath 'codexcomp_<version>_windows_<arch>.zip' -DestinationPath '<CPA_DIR>\plugins' -Force
```

Enable the plugin in `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    codexcomp:
      enabled: true
      priority: 1
```

If using Docker, mount the plugins directory in `docker-compose.yml`:

```yaml
volumes:
  - ./plugins:/CLIProxyAPI/plugins
```

Restart CPA.

For AI-agent-friendly installation steps, see [SETUP.md](SETUP.md).

### Building from Source

The `go.mod` has a `replace` directive pointing to an adjacent CLIProxyAPI directory. Clone the dependency first. See the [release workflow](.github/workflows/release.yml) for the authoritative target toolchains and validation steps:

```bash
git clone https://github.com/router-for-me/CLIProxyAPI.git ../CLIProxyAPI
# Linux or FreeBSD
go build -buildmode=c-shared -o codexcomp.so .

# macOS
go build -buildmode=c-shared -o codexcomp.dylib .
```

Native Windows builds require CGO and a C toolchain matching the target architecture: MSYS2 UCRT64/GCC for amd64, or MSYS2 CLANGARM64/Clang for arm64.

```powershell
git clone 'https://github.com/router-for-me/CLIProxyAPI.git' '..\CLIProxyAPI'
$env:CGO_ENABLED = '1'
go build -buildmode=c-shared -o codexcomp.dll .
```

## Configuration

No extra configuration is required by default. The plugin self-registers via the C ABI and routes automatically. For A/B testing or troubleshooting, these knobs are available.

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

`marker_text` is the continuation nudge inserted after a detected truncation. The default is `Continue thinking...`.

`max_continue` is the maximum number of continuation rounds. It defaults to 3. Set it to 0 to temporarily disable continuation while keeping routing and metadata visible for comparisons.

`max_tier_n` is the largest truncation tier eligible for continuation. It defaults to 6. Set it to 0 to remove the upper tier limit.

`debug_log` emits configuration and continuation-round details through CPA host log. It defaults to false and is intended for troubleshooting.

`models` is the exact model ID allow-list intercepted by the plugin. It defaults to `gpt-5.5` and `gpt-5.6-luna`. Luna is included because the 516-token truncation pattern has been observed on it. Terra and Sol are not included by default because the same issue has not been observed; add them manually if it appears later:

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

**Experimental and not recommended: observed effectiveness is poor because continuation rounds often add few or no reasoning tokens.** `min_reasoning_tokens` is an optional per-model minimum reasoning-token threshold. It has no default and is disabled unless explicitly configured. Once configured, it applies to every intercepted request for that model regardless of the requested reasoning effort. When cumulative folded reasoning tokens remain below the configured value, the plugin attempts another continuation:

```yaml
plugins:
  configs:
    codexcomp:
      min_reasoning_tokens:
        gpt-5.6-luna: 1200
```

This does not guarantee the threshold will be reached. Continuation stops when the threshold is reached, `max_continue` is exhausted, or the upstream no longer returns `encrypted_content`. This trigger is ORed with the original `518n-2` truncation trigger.

### Stable cache for direct CPA clients

When calling CPA's OpenAI-compatible endpoints directly (`/v1/chat/completions` or `/v1/responses`), send a stable `X-CPA-Session-Id` header for every request in the same conversation:

```http
X-CPA-Session-Id: your-stable-session-id
```

The plugin uses this value to derive the upstream `prompt_cache_key`, so multi-turn requests from the same conversation can hit prompt cache consistently. `X-CodexComp-Session-Id` and the legacy `X-Claude-Code-Session-Id` are also supported, but new integrations should prefer `X-CPA-Session-Id`.

For a more aggressive nudge, see [openai/codex#30364](https://github.com/openai/codex/issues/30364#issuecomment-4828984707). Setting `marker_text` to `Spend time on thinking; you do not need to use the commentary channel to report progress to me.` more explicitly asks the model to spend time on thinking and not use the commentary channel for progress reports. Its effect may vary across clients and tasks, so test it in your own workload before enabling it.

## Metadata Injection

The final `response.completed` event includes:

- `metadata.proxy_rounds` — per-round info (round number, reasoning tokens, truncation tier `n`; continued rounds additionally include `continue_reason`, such as `truncation` or `low_reasoning_tokens`)
- `metadata.proxy_billed_usage` — summed usage across all rounds
- `metadata.proxy_stopped_reason` — non-empty when the fold stopped for a non-natural reason (`no_encrypted_content`, `max_continue`, `tier_out_of_window`)

> **Note**: The metadata fields above are only guaranteed visible under the `openai-response` (Responses API) protocol. For `openai` (Chat Completions API) and `claude` (Anthropic Messages API) protocols, CPA's protocol translation layer does not forward the metadata from `response.completed`, so clients cannot access `proxy_rounds` and related fields. The continuation logic itself works correctly across all protocols — only the diagnostic metadata is unobservable outside the Responses API.

## Benchmark

Tested with the candy problem from [codex-candy-eval](https://github.com/haowang02/codex-candy-eval) — a reasoning depth test that triggers gpt-5.5 truncation. The correct answer is 21.

The repo includes a multi-protocol test script `scripts/candy_eval.py` supporting `openai-response`, `openai-chat`, and `anthropic` client protocols with concurrent testing:

**Command**:

```bash
python3 scripts/candy_eval.py \
  --url http://your-cpa:port --key YOUR_KEY \
  --protocol openai-response --rounds 5 -r high
```

### Without plugin (baseline)

| Run | Reasoning Tokens | Answer | Correct |
|-----|-----------------|--------|---------|
| 1   | 516             | 29     | ✗       |
| 2   | 516             | 29     | ✗       |
| 3   | 1552            | 21     | ✓       |
| 4   | 516             | 29     | ✗       |
| 5   | 3069            | 21     | ✓       |

**Accuracy: 2/5 (40%)** — 3 out of 5 responses were truncated at 516 tokens (n=1), producing wrong answers.

### With plugin

| Run | Reasoning Tokens | Answer | Correct |
|-----|-----------------|--------|---------|
| 1   | 3641            | 21     | ✓       |
| 2   | 2059            | 21     | ✓       |
| 3   | 3273            | 21     | ✓       |
| 4   | 4555            | 21     | ✓       |
| 5   | 3100            | 21     | ✓       |

**Accuracy: 5/5 (100%)** — all truncations were detected and continued, every answer correct.

## Disclaimer

This plugin relies on non-contractual model behavior (the `518n−2` truncation pattern and `encrypted_content` field). If OpenAI changes the truncation pattern or removes `encrypted_content`, the plugin will simply stop firing and become a transparent passthrough. Continuation rounds consume real tokens; the total is recorded in `metadata.proxy_billed_usage`.

## Acknowledgments

- **[CodexCont](https://github.com/neteroster/CodexCont)** (MIT) — the original continuation mechanism that identified the `518n−2` truncation pattern and pioneered the `encrypted_content` replay approach
- **[codexcomp](https://github.com/dzshzx/codexcomp)** (MIT) — the refined transport-agnostic fold algorithm; this plugin is a direct Go port of its `fold.py`
- **[codex-candy-eval](https://github.com/haowang02/codex-candy-eval)** — the reasoning depth benchmark used in this README
- **[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)** — the plugin host framework that makes in-process interception possible
- **[LINUX DO](https://linux.do)** community — where the gpt-5.5 “516” reasoning degradation was discussed, diagnosed, and validated; thanks for the reproductions, deployment feedback, and testing that shaped this plugin

## License

MIT. This plugin includes code derived from [CodexCont](https://github.com/neteroster/CodexCont) and [codexcomp](https://github.com/dzshzx/codexcomp). See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for details.
