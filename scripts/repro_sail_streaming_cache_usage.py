#!/usr/bin/env python3
"""Reproduce SAIL streaming usage omitting cache-token details.

This intentionally uses only the Python standard library so it can be copied
into a provider support ticket. It prints usage metadata only, never response
text or API keys.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any


DEFAULT_ENV_FILE = Path(".telos-harbor/provider.env")
DEFAULT_BASE_URL = "https://api.sailresearch.com/v1"
DEFAULT_MODEL = "deepseek-ai/DeepSeek-V4-Pro"


def load_env_file(path: Path) -> None:
    if not path.exists():
        return
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        value = value.strip().strip("'\"")
        if key and key not in os.environ:
            os.environ[key] = value


def post_chat_completion(
    *,
    base_url: str,
    api_key: str,
    payload: dict[str, Any],
    timeout_sec: int,
) -> dict[str, Any]:
    request = urllib.request.Request(
        f"{base_url.rstrip('/')}/chat/completions",
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=timeout_sec) as response:
        return json.loads(response.read().decode("utf-8"))


def stream_chat_completion(
    *,
    base_url: str,
    api_key: str,
    payload: dict[str, Any],
    timeout_sec: int,
) -> list[dict[str, Any]]:
    request = urllib.request.Request(
        f"{base_url.rstrip('/')}/chat/completions",
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Authorization": f"Bearer {api_key}",
            "Content-Type": "application/json",
        },
        method="POST",
    )
    usage_events: list[dict[str, Any]] = []
    with urllib.request.urlopen(request, timeout=timeout_sec) as response:
        for raw_line in response:
            line = raw_line.decode("utf-8", errors="replace").strip()
            if not line or line == "data: [DONE]":
                continue
            if line.startswith("data:"):
                line = line[5:].strip()
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            usage = event.get("usage")
            if isinstance(usage, dict):
                usage_events.append(usage)
    return usage_events


def cached_tokens(usage: dict[str, Any]) -> int | None:
    details = usage.get("prompt_tokens_details")
    if isinstance(details, dict) and "cached_tokens" in details:
        return int(details.get("cached_tokens") or 0)
    if "prompt_cache_hit_tokens" in usage:
        return int(usage.get("prompt_cache_hit_tokens") or 0)
    return None


def build_prompt(repetitions: int) -> str:
    repeated = "alpha beta gamma delta epsilon. " * repetitions
    return f"Static cache probe. {repeated}Reply with ok only."


def main() -> int:
    parser = argparse.ArgumentParser(
        description=(
            "Call SAIL Chat Completions in non-streaming and streaming modes "
            "and compare whether cache-token usage fields are present."
        )
    )
    parser.add_argument("--env-file", type=Path, default=DEFAULT_ENV_FILE)
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL)
    parser.add_argument("--model", default=DEFAULT_MODEL)
    parser.add_argument("--api-key-env", default="SAIL_API_KEY")
    parser.add_argument("--attempts", type=int, default=3)
    parser.add_argument("--prompt-repetitions", type=int, default=900)
    parser.add_argument("--max-completion-tokens", type=int, default=4)
    parser.add_argument("--sleep-sec", type=float, default=1.0)
    parser.add_argument("--timeout-sec", type=int, default=180)
    args = parser.parse_args()

    load_env_file(args.env_file)
    api_key = os.environ.get(args.api_key_env)
    if not api_key:
        print(
            f"missing {args.api_key_env}; set it in the environment or {args.env_file}",
            file=sys.stderr,
        )
        return 2

    base_payload = {
        "model": args.model,
        "messages": [{"role": "user", "content": build_prompt(args.prompt_repetitions)}],
        "max_completion_tokens": args.max_completion_tokens,
        "temperature": 0,
    }

    report: dict[str, Any] = {
        "base_url": args.base_url,
        "model": args.model,
        "non_streaming": [],
        "streaming": None,
    }

    try:
        for attempt in range(1, args.attempts + 1):
            response = post_chat_completion(
                base_url=args.base_url,
                api_key=api_key,
                payload={**base_payload, "stream": False},
                timeout_sec=args.timeout_sec,
            )
            usage = response.get("usage") or {}
            report["non_streaming"].append(
                {
                    "attempt": attempt,
                    "usage": usage,
                    "cached_tokens": cached_tokens(usage),
                }
            )
            if attempt != args.attempts:
                time.sleep(args.sleep_sec)

        stream_usage_events = stream_chat_completion(
            base_url=args.base_url,
            api_key=api_key,
            payload={
                **base_payload,
                "stream": True,
                "stream_options": {"include_usage": True},
            },
            timeout_sec=args.timeout_sec,
        )
        report["streaming"] = {
            "usage_events": stream_usage_events,
            "cached_tokens": [
                cached_tokens(usage) for usage in stream_usage_events if cached_tokens(usage) is not None
            ],
        }
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        print(f"HTTP {exc.code}: {body[:1000]}", file=sys.stderr)
        return 3
    except Exception as exc:
        print(f"{type(exc).__name__}: {exc}", file=sys.stderr)
        return 3

    non_stream_cached = [
        item["cached_tokens"]
        for item in report["non_streaming"]
        if isinstance(item.get("cached_tokens"), int) and item["cached_tokens"] > 0
    ]
    stream_cached_values = report["streaming"]["cached_tokens"] if report["streaming"] else []
    reproduced = bool(non_stream_cached) and not stream_cached_values

    report["reproduced"] = reproduced
    report["interpretation"] = (
        "SAIL non-streaming reported prompt cache hits, but streaming usage omitted cache-token fields."
        if reproduced
        else "Could not reproduce a non-streaming cache hit with missing streaming cache fields in this run."
    )
    print(json.dumps(report, indent=2, sort_keys=True))
    return 0 if reproduced else 1


if __name__ == "__main__":
    raise SystemExit(main())
