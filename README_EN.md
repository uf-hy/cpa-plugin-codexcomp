# CPA Plugin: CodexComp

[![Go 1.26+](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

[简体中文](README.md) | [English](README_EN.md)

A [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) plugin that detects and repairs gpt-5.5 reasoning truncation in streaming requests to reduce occasional model degradation, supporting OpenAI Responses API, OpenAI Chat Completions API, and Anthropic Messages API client protocols

In agent scenarios, gpt-5.5 reasoning tokens can stop exactly at 518n−2 (516, 1034, 1552, ...). This truncation can cause unexpected degradation. When the plugin detects this kind of truncation, it replays encrypted_content to continue reasoning, then folds multiple rounds into a single response that stays fully transparent to the downstream client.

## Quick Install (Agent)

If you're using an AI agent (Codex, Claude Code, etc.), send it this prompt:

```
Please install the CPA plugin codexcomp for me. Installation instructions are at https://github.com/uf-hy/cpa-plugin-codexcomp/blob/master/SETUP.md — read that document first, then proceed with installation.
```

## How It Works

The plugin intercepts gpt-5.5 streaming requests through CPA's C ABI plugin system, communicating with the upstream in codex format internally. Each time the upstream request finishes, it checks whether reasoning_tokens matches the 518n−2 pattern. If it matches and encrypted_content exists, it triggers continuation

The continuation round replays the original input, all previous thinking content, and a prompt message so the model continues from the truncation point rather than starting over

By default, it allows up to 3 continuation rounds, trading possible extra time for relatively more thinking length and time, thereby improving model intelligence

### Async Streaming & First-Byte Latency

gpt-5.5 with high reasoning effort can take 25-30 seconds before the first SSE event. Many clients (including Codex CLI) timeout at 10 seconds. This plugin uses CPA's async streaming mode: the response header is returned immediately, and a goroutine handles the fold logic. Upstream events are forwarded to the client as soon as they arrive — simple queries see first-byte under 500ms, and complex reasoning gets the first event forwarded immediately once upstream produces it.

## Scope

The plugin only intercepts requests that match **all** of:

- Model is `gpt-5.5`
- Client protocol is `openai-response` (Responses API), `openai` (Chat Completions API), or `claude` (Anthropic Messages API)
- Request is streaming (`stream: true`)
- No `previous_response_id` present

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

## Manual Installation

Download the latest `.so` from [Releases](https://github.com/uf-hy/cpa-plugin-codexcomp/releases/latest):

- `codexcomp-linux-amd64.so` — Linux x86_64 (most common)
- `codexcomp-linux-arm64.so` — Linux ARM64

```bash
# Choose one command for the CPU architecture where CPA runs.
# Linux x86_64:
wget -qO <CPA_DIR>/plugins/codexcomp.so \
  "https://github.com/uf-hy/cpa-plugin-codexcomp/releases/latest/download/codexcomp-linux-amd64.so"

# Linux ARM64:
wget -qO <CPA_DIR>/plugins/codexcomp.so \
  "https://github.com/uf-hy/cpa-plugin-codexcomp/releases/latest/download/codexcomp-linux-arm64.so"
```

Enable the plugin:

1. Put `codexcomp.so` in `<CPA_DIR>/plugins/`
2. Enable plugins in `config.yaml`:

```yaml
plugins:
  enabled: true
  dir: plugins
  configs:
    codexcomp:
      enabled: true
      priority: 1
```

3. If using Docker, mount the plugins directory in `docker-compose.yml`:

```yaml
volumes:
  - ./plugins:/CLIProxyAPI/plugins:ro
```

4. Restart CPA.

For AI-agent-friendly installation steps, see [SETUP.md](SETUP.md).

## Configuration

No extra configuration is required by default. The plugin self-registers via the C ABI and routes automatically. For A/B testing or troubleshooting, these knobs are available.

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

`marker_text` is the continuation nudge inserted after a detected truncation. The default remains `Continue thinking...`.

`max_continue` is the maximum number of continuation rounds. It defaults to 3. Set it to 0 to temporarily disable continuation while keeping routing and metadata visible for comparisons.

`max_tier_n` is the largest truncation tier eligible for continuation. It defaults to 6. Set it to 0 to remove the upper tier limit.

`truncation_step` is the step used to detect the `step*n−2` truncation pattern. It defaults to 518. Do not change it unless new samples show a stable different step.

`debug_log` emits configuration and continuation-round details through CPA host log. It defaults to false and is intended for troubleshooting.

The marker text above comes from related discussion in [openai/codex#30364](https://github.com/openai/codex/issues/30364#issuecomment-4828984707). It more explicitly asks the model to spend time on thinking and not use the commentary channel for progress reports. Its effect may vary across clients and tasks, so test it in your own workload before enabling it.

## Metadata Injection

The final `response.completed` event includes:

- `metadata.proxy_rounds` — per-round info (round number, reasoning tokens, truncation tier `n`)
- `metadata.proxy_billed_usage` — summed usage across all rounds
- `metadata.proxy_stopped_reason` — non-empty when the fold stopped for a non-natural reason (`no_encrypted_content`, `max_continue`, `tier_out_of_window`)

## Benchmark

Tested with the candy problem from [codex-candy-eval](https://github.com/haowang02/codex-candy-eval) — a reasoning depth test that triggers gpt-5.5 truncation. The correct answer is 21.

The repo includes a streaming test script `scripts/candy_eval_cpa.py` that sends streaming Responses API requests to your CPA endpoint:

**Command**:

```bash
python3 scripts/candy_eval_cpa.py \
  --url http://your-cpa:port/v1/responses \
  --key YOUR_KEY -n 5 -r high
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
