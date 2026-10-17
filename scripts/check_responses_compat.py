#!/usr/bin/env python3
"""Check #11 against a real CPA binary and plugin, using a local mock upstream.

Usage: python3 scripts/check_responses_compat.py --cpa /path/to/cli-proxy-api \
    --plugin /path/to/codexcomp.so
No real credentials or model requests are used. The mock also acts as the
configured outbound HTTP proxy, so the upstream hostname need not resolve.
"""

import argparse
import http.server
import json
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import threading
import time
import urllib.error
import urllib.request


class Upstream(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if "delayed" in json.dumps(body["input"]):
            time.sleep(0.4)  # Exercise the plugin's asynchronous startup path.
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        item = {
            "type": "message", "id": "msg_test", "role": "assistant",
            "status": "completed",
            "content": [{"type": "output_text", "text": "Hello!", "annotations": []}],
        }
        if "tool-call" in json.dumps(body["input"]):
            item = {
                "type": "function_call", "id": "fc_test", "call_id": "call_test",
                "name": "lookup", "arguments": '{"key":"value"}', "status": "completed",
            }
        response = {
            "id": "resp_test", "object": "response", "model": body["model"],
            "status": "in_progress", "output": [],
        }
        events = [
            {"type": "response.created", "response": response},
            {"type": "response.output_item.added", "output_index": 0, "item": item},
            {"type": "response.output_item.done", "output_index": 0, "item": item},
            {"type": "response.completed", "response": {
                **response, "status": "completed", "output": [item],
                "usage": {"input_tokens": 2, "output_tokens": 10,
                          "output_tokens_details": {"reasoning_tokens": 7}},
            }},
        ]
        for event in events:
            self.wfile.write(("data: " + json.dumps(event) + "\n\n").encode())
            self.wfile.flush()

    def log_message(self, *args):
        pass


def check(cpa, plugin, root, proxy_port):
    with socket.socket() as sock:
        sock.bind(("127.0.0.1", 0))
        port = sock.getsockname()[1]
    plugins = root / "plugins"
    plugins.mkdir()
    shutil.copy2(plugin, plugins / "codexcomp.so")
    config = {
        "host": "127.0.0.1", "port": port, "auth-dir": str(root / "auths"),
        "api-keys": ["local-test"], "request-retry": 0,
        "proxy-url": f"http://127.0.0.1:{proxy_port}",
        "remote-management": {"disable-control-panel": True},
        "codex-api-key": [{
            "api-key": "dummy-test-key", "base-url": "http://codexcomp-test.invalid",
            "models": [{"name": "gpt-6-astra"}, {"name": "gpt-5.5"}],
        }],
        "plugins": {"enabled": True, "dir": str(plugins), "configs": {
            "codexcomp": {"enabled": True, "priority": 1, "models": ["gpt-6-astra", "gpt-5.5"]},
        }},
    }
    config_path = root / "config.yaml"
    config_path.write_text(json.dumps(config))  # JSON is also valid YAML.
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    base = f"http://127.0.0.1:{port}"
    with (root / "cpa.log").open("w") as log:
        process = subprocess.Popen(
            [str(cpa), "--config", str(config_path), "--local-model"],
            cwd=root, stdout=log, stderr=log,
        )
        try:
            for _ in range(100):
                if process.poll() is not None:
                    raise RuntimeError("CPA exited during startup")
                try:
                    with opener.open(base + "/", timeout=1):
                        break
                except OSError:
                    time.sleep(0.1)
            else:
                raise RuntimeError("CPA did not start")
            for model in ["gpt-6-astra", "gpt-5.5"]:
                for case in ["passthrough", "text", "delayed", "tool-call"]:
                    value = "Hi" if case == "passthrough" else [{"role": "user", "content": case}]
                    body = {"model": model, "stream": True, "reasoning": {"effort": "low"}, "input": value}
                    request = urllib.request.Request(
                        base + "/v1/responses", data=json.dumps(body).encode(),
                        headers={"Content-Type": "application/json", "Authorization": "Bearer local-test"},
                    )
                    try:
                        response = opener.open(request, timeout=10)
                    except urllib.error.HTTPError as error:
                        response = error
                    with response:
                        raw = response.read().decode()
                        assert response.status == 200, (model, case, response.status, raw)
                    events = [json.loads(line[5:]) for line in raw.splitlines()
                              if line.startswith("data:") and line[5:].strip() != "[DONE]"]
                    assert events[0]["type"] == "response.created", events
                    completed = [e["response"] for e in events if e["type"] == "response.completed"]
                    assert len(completed) == 1, events
                    result = completed[0]
                    assert result["status"] == "completed", result
                    if case == "tool-call":
                        assert result["output"][0]["call_id"] == "call_test", result
                        assert result["output"][0]["arguments"] == '{"key":"value"}', result
                    else:
                        assert result["output"][0]["content"][0]["text"] == "Hello!", result
                    if case != "passthrough":
                        assert len(result["metadata"]["proxy_rounds"]) == 1, result
                        assert result["usage"]["output_tokens_details"]["reasoning_tokens"] == 7, result
                    print(f"PASS {model} {case}", flush=True)
        finally:
            process.terminate()
            try:
                process.wait(timeout=10)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cpa", type=Path, required=True)
    parser.add_argument("--plugin", type=Path, required=True)
    args = parser.parse_args()
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), Upstream)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    try:
        with tempfile.TemporaryDirectory(prefix="codexcomp-issue11-") as directory:
            root = Path(directory)
            try:
                check(args.cpa.resolve(), args.plugin.resolve(), root, server.server_port)
            except Exception:
                print((root / "cpa.log").read_text())
                raise
    finally:
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    main()
