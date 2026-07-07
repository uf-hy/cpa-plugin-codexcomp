#!/usr/bin/env python3
"""Codex Candy Eval - Multi-protocol CPA version

Tests gpt-5.5 reasoning via CPA with multiple client protocols.
Compares truncation-fold plugin behavior across openai-response, openai-chat, etc.

Usage:
    # Test single protocol
    python3 candy_eval.py --url http://127.0.0.1:35502 --key YOUR_KEY \
        --protocol openai-response -n 5 -r high

    # Test multiple protocols with concurrency
    python3 candy_eval.py --url http://127.0.0.1:35502 --key YOUR_KEY \
        --protocol openai-response,openai-chat -n 5 -r high --concurrency 3

    # Baseline (no plugin) vs with plugin
    python3 candy_eval.py --url http://127.0.0.1:35502 --key YOUR_KEY \
        --protocol openai-response,openai-chat -n 5 --label with_plugin
"""

import argparse
import json
import re
import sys
import time
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from typing import Optional

try:
    import requests
except ImportError:
    print("ERROR: requests library not found. Install with: pip install requests", file=sys.stderr)
    sys.exit(1)


# ---------------------------------------------------------------------------
# Shared test prompt and answer checking
# ---------------------------------------------------------------------------

CODEX_PROMPT = """不使用任何外部工具回答以下问题：

在一个黑色的袋子里放有三种口味的糖果，每种糖果有两种不同的形状（圆形和五角星形，不同的形状靠手感可以分辨）。现已知不同口味的糖和不同形状的数量统计如下表。参赛者需要在活动前决定摸出的糖果数目，那么，最少取出多少个糖果才能保证手中同时拥有不同形状的苹果味和桃子味的糖？（同时手中有圆形苹果味匹配五角星桃子味糖果，或者有圆形桃子味匹配五角星苹果味糖果都满足要求）

        苹果味  桃子味  西瓜味
圆形       7      9      8
五角星形   7      6      4
"""

ANSWER_PATTERN = re.compile(r"(?<!\d)21(?!\d)")


# ---------------------------------------------------------------------------
# Result dataclass
# ---------------------------------------------------------------------------

@dataclass
class RoundResult:
    protocol: str
    round_no: int
    ok: bool = False
    input_tokens: int = 0
    output_tokens: int = 0
    reasoning_tokens: int = 0
    elapsed: float = 0.0
    rounds: int = 1
    stopped_reason: str = ""
    answer_preview: str = ""
    error: str = ""


# ---------------------------------------------------------------------------
# Protocol adapters
# ---------------------------------------------------------------------------

class ProtocolAdapter:
    """Base class for protocol-specific request/response handling."""

    name: str = "base"
    endpoint: str = ""
    content_type: str = "application/json"

    def build_body(self, model: str, effort: str) -> dict:
        raise NotImplementedError

    def parse_stream(self, lines) -> RoundResult:
        """Parse SSE stream lines and return metrics."""
        raise NotImplementedError


class OpenAIResponseAdapter(ProtocolAdapter):
    """OpenAI Responses API: POST /v1/responses"""

    name = "openai-response"
    endpoint = "/v1/responses"

    def build_body(self, model: str, effort: str) -> dict:
        return {
            "model": model,
            "stream": True,
            "input": [{
                "type": "message",
                "role": "user",
                "content": [{"type": "input_text", "text": CODEX_PROMPT}],
            }],
            "reasoning": {"effort": effort},
        }

    def parse_stream(self, lines) -> RoundResult:
        result = RoundResult(protocol=self.name, round_no=0)
        text = ""
        usage = {}
        meta = {}

        for line in lines:
            if not line.startswith("data:"):
                continue
            data_str = line[5:].strip()
            if data_str == "[DONE]":
                break
            try:
                ev = json.loads(data_str)
            except json.JSONDecodeError:
                continue

            etype = ev.get("type", "")
            if etype == "response.output_text.delta":
                text += ev.get("delta", "")
            elif etype in ("response.completed", "response.failed", "response.incomplete"):
                r = ev.get("response", {})
                usage = r.get("usage", {})
                meta = r.get("metadata", {})

        result.input_tokens = usage.get("input_tokens", 0)
        result.output_tokens = usage.get("output_tokens", 0)
        result.reasoning_tokens = usage.get("output_tokens_details", {}).get("reasoning_tokens", 0)
        rounds = meta.get("proxy_rounds", [])
        result.rounds = len(rounds) if rounds else 1
        result.stopped_reason = meta.get("proxy_stopped_reason", "")
        result.ok = bool(ANSWER_PATTERN.search(text))
        result.answer_preview = text[:50].replace("\n", " ")
        return result


class OpenAIChatAdapter(ProtocolAdapter):
    """OpenAI Chat Completions API: POST /v1/chat/completions"""

    name = "openai-chat"
    endpoint = "/v1/chat/completions"

    def build_body(self, model: str, effort: str) -> dict:
        return {
            "model": model,
            "stream": True,
            "stream_options": {"include_usage": True},
            "messages": [{
                "role": "user",
                "content": CODEX_PROMPT,
            }],
            "reasoning_effort": effort,
        }

    def parse_stream(self, lines) -> RoundResult:
        result = RoundResult(protocol=self.name, round_no=0)
        text = ""
        usage = {}

        for line in lines:
            if not line.startswith("data:"):
                continue
            data_str = line[5:].strip()
            if data_str == "[DONE]":
                break
            try:
                ev = json.loads(data_str)
            except json.JSONDecodeError:
                continue

            # Extract usage from the final chunk (stream_options.include_usage)
            if "usage" in ev and ev["usage"]:
                usage = ev["usage"]

            choices = ev.get("choices", [])
            if choices:
                delta = choices[0].get("delta", {})
                content = delta.get("content", "")
                if content:
                    text += content

        result.input_tokens = usage.get("prompt_tokens", 0)
        result.output_tokens = usage.get("completion_tokens", 0)
        result.reasoning_tokens = usage.get("completion_tokens_details", {}).get("reasoning_tokens", 0)
        result.rounds = 1  # chat completions has no proxy_rounds metadata
        result.stopped_reason = ""
        result.ok = bool(ANSWER_PATTERN.search(text))
        result.answer_preview = text[:50].replace("\n", " ")
        return result


class AnthropicAdapter(ProtocolAdapter):
    """Anthropic Messages API: POST /v1/messages"""

    name = "anthropic"
    endpoint = "/v1/messages"

    def build_body(self, model: str, effort: str) -> dict:
        # Map reasoning effort to thinking budget
        budget_map = {"low": 5000, "medium": 10000, "high": 16000}
        budget = budget_map.get(effort, 16000)
        return {
            "model": model,
            "stream": True,
            "max_tokens": 10240,
            "thinking": {
                "type": "enabled",
                "budget_tokens": budget,
            },
            "messages": [{
                "role": "user",
                "content": CODEX_PROMPT,
            }],
        }

    def headers(self, key: str) -> dict:
        return {
            "Content-Type": self.content_type,
            "x-api-key": key,
            "anthropic-version": "2023-06-01",
            "Accept": "text/event-stream",
        }

    def parse_stream(self, lines) -> RoundResult:
        result = RoundResult(protocol=self.name, round_no=0)
        text = ""
        usage = {}

        for line in lines:
            # Anthropic SSE has "event: ..." and "data: ..." lines
            if not line.startswith("data:"):
                continue
            data_str = line[5:].strip()
            try:
                ev = json.loads(data_str)
            except json.JSONDecodeError:
                continue

            etype = ev.get("type", "")
            if etype == "content_block_delta":
                delta = ev.get("delta", {})
                if delta.get("type") == "text_delta":
                    text += delta.get("text", "")
            elif etype == "message_delta":
                u = ev.get("usage", {})
                if u:
                    usage = u
            elif etype == "message_start":
                msg = ev.get("message", {})
                u = msg.get("usage", {})
                if u:
                    usage.update(u)

        result.input_tokens = usage.get("input_tokens", 0)
        result.output_tokens = usage.get("output_tokens", 0)
        result.reasoning_tokens = 0  # Claude format doesn't expose reasoning_tokens
        result.rounds = 1
        result.stopped_reason = ""
        result.ok = bool(ANSWER_PATTERN.search(text))
        result.answer_preview = text[:50].replace("\n", " ")
        return result


PROTOCOLS = {
    "openai-response": OpenAIResponseAdapter,
    "openai-chat": OpenAIChatAdapter,
    "anthropic": AnthropicAdapter,
}


# ---------------------------------------------------------------------------
# Test runner
# ---------------------------------------------------------------------------

def run_one(base_url: str, key: str, adapter: ProtocolAdapter,
            model: str, effort: str, round_no: int) -> RoundResult:
    """Run a single test round and return result."""
    url = base_url.rstrip("/") + adapter.endpoint
    body = adapter.build_body(model, effort)

    # Build headers (anthropic uses x-api-key instead of Bearer)
    if hasattr(adapter, "headers"):
        hdrs = adapter.headers(key)
    else:
        hdrs = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {key}",
            "Accept": "text/event-stream",
        }

    result = RoundResult(protocol=adapter.name, round_no=round_no)
    start = time.perf_counter()

    try:
        resp = requests.post(url, json=body, headers=hdrs, stream=True, timeout=300)
        if resp.status_code != 200:
            result.error = f"HTTP {resp.status_code}: {resp.text[:200]}"
            result.elapsed = time.perf_counter() - start
            return result

        # Use iter_content + split by blank line to handle long SSE data lines
        # that iter_lines may truncate. Each SSE event is separated by \n\n.
        lines = []
        buf = b""
        for chunk in resp.iter_content(chunk_size=None):
            buf += chunk
            while b"\n\n" in buf:
                event_bytes, buf = buf.split(b"\n\n", 1)
                for line in event_bytes.decode("utf-8", errors="replace").split("\n"):
                    if line.strip():
                        lines.append(line)
        # Flush remaining buffer
        if buf:
            for line in buf.decode("utf-8", errors="replace").split("\n"):
                if line.strip():
                    lines.append(line)

        result = adapter.parse_stream(lines)
        result.round_no = round_no
        result.elapsed = time.perf_counter() - start
    except Exception as e:
        result.error = str(e)[:200]
        result.elapsed = time.perf_counter() - start

    return result


def run_protocol(base_url: str, key: str, protocol_name: str,
                 model: str, effort: str, rounds: int, concurrency: int) -> list:
    """Run all rounds for one protocol, with optional concurrency."""
    adapter_cls = PROTOCOLS[protocol_name]
    adapter = adapter_cls()
    results = [None] * rounds  # pre-allocate to preserve order

    if concurrency <= 1:
        for i in range(1, rounds + 1):
            r = run_one(base_url, key, adapter, model, effort, i)
            results[i - 1] = r
            _print_row(r)
    else:
        with ThreadPoolExecutor(max_workers=concurrency) as pool:
            futures = {}
            for i in range(1, rounds + 1):
                f = pool.submit(run_one, base_url, key, adapter, model, effort, i)
                futures[f] = i
            for f in as_completed(futures):
                idx = futures[f]
                results[idx - 1] = f.result()
                _print_row(results[idx - 1])

    return [r for r in results if r is not None]


def _print_row(r: RoundResult):
    """Print a single result row to the table."""
    if r.error:
        proto_short = r.protocol[:12]
        print(f"{r.protocol:>14}  {r.round_no:>3}  ERR  {r.error[:70]}")
        return

    tps = f"{r.output_tokens / r.elapsed:.1f}" if r.output_tokens and r.elapsed > 0 else "-"
    ok_str = "✓" if r.ok else "✗"
    print(f"{r.protocol:>14}  {r.round_no:>3}  {ok_str:^3}  "
          f"{r.input_tokens:>6}  {r.output_tokens:>6}  {r.reasoning_tokens:>6}  "
          f"{r.elapsed:>6.1f}  {tps:>6}  {r.rounds:>6}  "
          f"{r.stopped_reason or '-':>20}  {r.answer_preview}")


def _print_header():
    print(f"{'Protocol':>14}  {'Run':>3}  {'OK':^3}  "
          f"{'InTok':>6}  {'OutTok':>6}  {'ReaTok':>6}  "
          f"{'Time':>6}  {'TPS':>6}  {'Rounds':>6}  "
          f"{'Stopped':>20}  Answer")
    print("-" * 130)


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                     formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", required=True, help="CPA base URL (e.g. http://127.0.0.1:35502)")
    parser.add_argument("--key", required=True, help="API key")
    parser.add_argument("-m", "--model", default="gpt-5.5")
    parser.add_argument("-r", "--reasoning-effort", default="high",
                        choices=["low", "medium", "high"])
    parser.add_argument("-n", "--rounds", type=int, default=1,
                        help="Rounds per protocol")
    parser.add_argument("--protocol", default="openai-response",
                        help="Comma-separated protocol list: openai-response,openai-chat,anthropic")
    parser.add_argument("--concurrency", type=int, default=1,
                        help="Concurrent requests within one protocol")
    parser.add_argument("--label", default="", help="Label for this run")
    args = parser.parse_args()

    protocols = [p.strip() for p in args.protocol.split(",") if p.strip()]
    for p in protocols:
        if p not in PROTOCOLS:
            print(f"ERROR: unknown protocol '{p}'. Available: {', '.join(PROTOCOLS.keys())}", file=sys.stderr)
            sys.exit(1)

    print(f"CPA: {args.url}")
    print(f"Model: {args.model}  Effort: {args.reasoning_effort}  "
          f"Rounds: {args.rounds}/protocol  Concurrency: {args.concurrency}")
    print(f"Protocols: {', '.join(protocols)}")
    if args.label:
        print(f"Label: {args.label}")
    print()

    _print_header()

    all_results = {}
    for proto in protocols:
        print(f"\n--- {proto} ---")
        results = run_protocol(args.url, args.key, proto, args.model,
                               args.reasoning_effort, args.rounds, args.concurrency)
        all_results[proto] = results

    # Summary
    print("\n" + "=" * 130)
    print(f"{'Protocol':>14}  {'Total':>5}  {'Correct':>7}  {'Accuracy':>8}  "
          f"{'AvgTime':>7}  {'AvgReaTok':>9}")
    print("-" * 60)

    summary = {}
    for proto, results in all_results.items():
        total = len(results)
        correct = sum(1 for r in results if r.ok)
        acc = correct / total * 100 if total else 0
        times = [r.elapsed for r in results if r.elapsed > 0]
        avg_time = sum(times) / len(times) if times else 0
        rea_toks = [r.reasoning_tokens for r in results if r.reasoning_tokens > 0]
        avg_rea = sum(rea_toks) / len(rea_toks) if rea_toks else 0
        print(f"{proto:>14}  {total:>5}  {correct:>7}  {acc:>7.1f}%  "
              f"{avg_time:>6.1f}s  {avg_rea:>9.0f}")
        summary[proto] = {
            "total": total, "correct": correct, "accuracy": acc,
            "avg_time": avg_time, "avg_reasoning_tokens": avg_rea,
        }

    label = f" [{args.label}]" if args.label else ""
    print(f"\n{label} Summary: {len(protocols)} protocols, {sum(len(r) for r in all_results.values())} total rounds")

    # Save JSON
    import os
    safe_label = args.label.replace(" ", "_").replace("[", "").replace("]", "") or "results"
    filename = f"candy_eval_{safe_label}.json"
    output = {
        "label": args.label,
        "config": {
            "url": args.url,
            "model": args.model,
            "effort": args.reasoning_effort,
            "rounds": args.rounds,
            "concurrency": args.concurrency,
            "protocols": protocols,
        },
        "summary": summary,
        "results": {
            proto: [
                {
                    "round": r.round_no,
                    "ok": r.ok,
                    "input_tokens": r.input_tokens,
                    "output_tokens": r.output_tokens,
                    "reasoning_tokens": r.reasoning_tokens,
                    "elapsed": r.elapsed,
                    "rounds": r.rounds,
                    "stopped_reason": r.stopped_reason,
                    "answer_preview": r.answer_preview,
                    "error": r.error,
                }
                for r in results
            ]
            for proto, results in all_results.items()
        },
    }
    # Save to scripts/ directory
    script_dir = os.path.dirname(os.path.abspath(__file__))
    filepath = os.path.join(script_dir, filename)
    with open(filepath, "w") as f:
        json.dump(output, f, indent=2, ensure_ascii=False)
    print(f"Results saved to {filepath}")


if __name__ == "__main__":
    main()
