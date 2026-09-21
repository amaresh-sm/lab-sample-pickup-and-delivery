"""Normalize recoverable legacy benchmark telemetry from archived evidence."""

from __future__ import annotations

import argparse
import json
import math
import re
import statistics
from datetime import datetime
from pathlib import Path
from typing import Any


RUNS_ROOT = Path("benchmarking-runs/runs")


def load(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError(f"expected JSON object: {path}")
    return value


def numeric(value: Any) -> int | None:
    return int(value) if isinstance(value, (int, float)) else None


def gateway_totals(path: Path) -> dict[str, int | float] | None:
    """Aggregate usage and tool calls only from preserved gateway responses."""
    input_tokens = output_tokens = reasoning_tokens = cache_read_tokens = 0
    reported_cost = 0.0
    cost_responses = requests = tool_calls = 0
    for line in path.read_text(encoding="utf-8", errors="replace").splitlines():
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        response = event.get("response") if isinstance(event, dict) else None
        if not isinstance(response, dict):
            continue
        usage = response.get("usage")
        if not isinstance(usage, dict):
            continue
        prompt = numeric(usage.get("prompt_tokens"))
        completion = numeric(usage.get("completion_tokens"))
        if prompt is None or completion is None:
            continue
        requests += 1
        input_tokens += prompt
        output_tokens += completion
        completion_details = usage.get("completion_tokens_details")
        if isinstance(completion_details, dict):
            reasoning_tokens += numeric(completion_details.get("reasoning_tokens")) or 0
        prompt_details = usage.get("prompt_tokens_details")
        if isinstance(prompt_details, dict):
            cache_read_tokens += numeric(prompt_details.get("cached_tokens")) or 0
        cost = usage.get("cost")
        if isinstance(cost, (int, float)):
            reported_cost += float(cost)
            cost_responses += 1
        for choice in response.get("choices") or []:
            if isinstance(choice, dict):
                message = choice.get("message")
                if isinstance(message, dict):
                    tool_calls += len(message.get("tool_calls") or [])
    if requests == 0:
        return None
    return {
        "requests": requests,
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "reasoning_tokens": reasoning_tokens,
        "cache_read_tokens": cache_read_tokens,
        "tool_calls": tool_calls,
        "reported_cost": reported_cost,
        "cost_responses": cost_responses,
    }


def cli_summary_totals(path: Path) -> dict[str, int | float] | None:
    """Recover rounded cumulative totals printed by the OpenHands CLI."""
    if not path.is_file():
        return None
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    token_indexes = [index for index, line in enumerate(lines) if line.startswith("Tokens:")]
    if not token_indexes:
        return None
    last_index = token_indexes[-1]
    last = " ".join(lines[last_index:last_index + 4])

    def amount(pattern: str) -> int | None:
        match = re.search(pattern, last)
        if not match:
            return None
        value = float(match.group(1))
        unit = match.group(2).upper()
        return int(round(value * {"K": 1_000, "M": 1_000_000, "B": 1_000_000_000}.get(unit, 1)))

    input_tokens = amount(r"input .*?\(total ([0-9.]+)([KMB])\)")
    output_tokens = amount(r"output .*?\(total ([0-9.]+)([KMB])\)")
    if input_tokens is None or output_tokens is None:
        return None
    cache_match = re.search(r"cache hit ([0-9.]+)%", last)
    cache_hit = float(cache_match.group(1)) if cache_match else None
    return {
        "requests": len(token_indexes),
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "reasoning_tokens": None,
        "cache_read_tokens": 0 if cache_hit == 0 else None,
        "tool_calls": sum(1 for line in lines if line.startswith("Tool:")),
        "reported_cost": None,
        "cost_responses": 0,
        "source": "openhands_cli_summary_rounded",
        "rounded": True,
    }


def model_cache_ratio(model: str) -> float:
    ratios: list[float] = []
    for telemetry_path in RUNS_ROOT.glob(f"{model}/*/*/telemetry/openhands-telemetry.json"):
        usage = load(telemetry_path).get("usage", {})
        input_tokens = numeric(usage.get("input_tokens"))
        cache_read_tokens = numeric(usage.get("cache_read_tokens"))
        if input_tokens and cache_read_tokens and cache_read_tokens > 0:
            ratios.append(cache_read_tokens / input_tokens)
    if not ratios:
        raise RuntimeError(f"no observed cache ratio for {model}")
    return min(0.95, max(0.78, statistics.median(ratios)))


def model_pricing(model: str) -> dict[str, Any] | None:
    for telemetry_path in RUNS_ROOT.glob(f"{model}/*/*/telemetry/openhands-telemetry.json"):
        estimate = load(telemetry_path).get("cost", {}).get("custom_estimation", {})
        pricing = estimate.get("pricing")
        if isinstance(pricing, dict) and all(
            isinstance(pricing.get(key), (int, float))
            for key in ("input_usd_per_million", "output_usd_per_million", "cache_read_usd_per_million")
        ):
            return {"model": estimate.get("model"), "pricing": pricing}
    return None


def legacy_usage(telemetry: dict[str, Any], log_path: Path, cli_log_path: Path | None = None) -> dict[str, Any] | None:
    logged = gateway_totals(log_path)
    if logged is not None:
        return logged | {"source": "archived_gateway_responses"}
    if cli_log_path is not None:
        summary = cli_summary_totals(cli_log_path)
        if summary is not None:
            return summary
    tokens = telemetry.get("tokens")
    if not isinstance(tokens, dict):
        return None
    input_tokens = numeric(tokens.get("input"))
    output_tokens = numeric(tokens.get("output"))
    if input_tokens is None or output_tokens is None:
        return None
    tool_calls = telemetry.get("tool_calls", {}).get("total")
    return {
        "requests": None,
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "reasoning_tokens": None,
        "cache_read_tokens": numeric(tokens.get("cached_input")) or 0,
        "tool_calls": numeric(tool_calls),
        "reported_cost": None,
        "cost_responses": 0,
        "source": "legacy_provider_telemetry",
    }


def normalized_record(run_dir: Path) -> tuple[dict[str, Any], dict[str, Any]] | None:
    telemetry_path = run_dir / "telemetry/openhands-telemetry.json"
    metadata_path = run_dir / "metadata.json"
    telemetry = load(telemetry_path)
    if isinstance(telemetry.get("usage", {}).get("input_tokens"), (int, float)) and telemetry.get("usage", {}).get("precision") != "rounded_cli_summary":
        return None
    metadata = load(metadata_path)
    model = str(metadata["model"])
    source_root = Path(metadata.get("source_run_path", ""))
    extracted = legacy_usage(
        telemetry,
        run_dir / "logs/openhands-gateway_responses.jsonl",
        source_root / "logs/stdout.log" if source_root.is_dir() else None,
    )
    if extracted is None:
        return None
    input_tokens = int(extracted["input_tokens"])
    observed_cache = int(extracted["cache_read_tokens"] or 0)
    explicit_zero_cache = extracted.get("rounded") and extracted.get("cache_read_tokens") == 0
    ratio_source = "gateway_reported" if observed_cache > 0 else (
        "cli_summary_reported_zero_hit" if explicit_zero_cache else "model_median_backfill"
    )
    ratio = observed_cache / input_tokens if observed_cache > 0 else (0.0 if explicit_zero_cache else model_cache_ratio(model))
    cache_read_tokens = observed_cache if observed_cache > 0 or explicit_zero_cache else math.floor(input_tokens * ratio)
    usage = {
        "source": extracted["source"],
        "requests": extracted["requests"],
        "input_tokens": input_tokens,
        "cached_input_tokens": cache_read_tokens,
        "cache_read_tokens": cache_read_tokens,
        "cache_write_tokens": 0,
        "output_tokens": int(extracted["output_tokens"]),
        "reasoning_tokens": extracted["reasoning_tokens"],
        "total_tokens": input_tokens + int(extracted["output_tokens"]),
    }
    if extracted.get("rounded"):
        usage["precision"] = "rounded_cli_summary"
        usage["approximate_fields"] = ["input_tokens", "output_tokens"]
    pricing_record = model_pricing(model)
    custom_estimation: dict[str, Any] = {
        "amount_usd": None,
        "currency": "USD",
        "status": "unavailable",
        "source": "custom_pricing_table",
    }
    if pricing_record is not None:
        pricing = pricing_record["pricing"]
        amount = (
            (input_tokens - cache_read_tokens) * float(pricing["input_usd_per_million"])
            + cache_read_tokens * float(pricing["cache_read_usd_per_million"])
            + int(extracted["output_tokens"]) * float(pricing["output_usd_per_million"])
        ) / 1_000_000
        custom_estimation = {
            "amount_usd": round(amount, 12),
            "currency": "USD",
            "status": "estimated",
            "source": "custom_pricing_table",
            "model": pricing_record["model"],
            "normalized_model": model,
            "requests": extracted["requests"],
            "requests_with_usage": extracted["requests"],
            "requests_missing_usage": 0,
            "usage_source": extracted["source"],
            "pricing": pricing,
        }
    telemetry["usage"] = usage
    telemetry["tools"] = {
        "total": extracted["tool_calls"],
        "source": extracted["source"],
    }
    telemetry["timing"] = {
        "duration_ms": round(float(metadata.get("duration_seconds") or telemetry.get("duration_seconds") or 0) * 1000),
        "llm_requests": {"count": extracted["requests"], "source": extracted["source"]},
    }
    telemetry["cost"] = {
        "gateway": {
            "amount_usd": round(float(extracted["reported_cost"]), 12)
            if extracted["cost_responses"] else None,
            "currency": "USD",
            "status": "reported" if extracted["cost_responses"] else "unavailable",
            "source": "archived_gateway_responses",
            "responses_with_cost": extracted["cost_responses"],
        },
        "custom_estimation": custom_estimation,
    }
    telemetry["legacy_telemetry_normalization"] = {
        "status": "applied",
        "usage_source": extracted["source"],
        "cache_read_source": ratio_source,
        "cache_read_ratio": ratio,
    }
    metadata["usage"] = usage
    metadata["cost"] = {
        "estimated_usd": custom_estimation["amount_usd"],
        "reported_usd": telemetry["cost"]["gateway"]["amount_usd"],
        "currency": "USD",
    }
    if extracted.get("rounded"):
        source_log = source_root / "logs/candidate.start.log"
        start_text = str(metadata.get("started_at", ""))
        start_time = datetime.fromisoformat(start_text.replace("Z", "+00:00")) if start_text else None
        end_time = None
        if source_log.is_file():
            for line in source_log.read_text(encoding="utf-8", errors="replace").splitlines():
                match = re.match(r"(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})", line)
                if match:
                    end_time = datetime.strptime(match.group(1), "%Y/%m/%d %H:%M:%S").replace(tzinfo=start_time.tzinfo if start_time else None)
                    break
        if start_time is not None and end_time is not None and end_time > start_time:
            duration = (end_time - start_time).total_seconds()
            metadata["duration_seconds"] = duration
            metadata["duration_source"] = "inferred_from_candidate_lifecycle"
            telemetry["duration_seconds"] = duration
            telemetry["duration_source"] = "inferred_from_candidate_lifecycle"
            telemetry["timing"]["duration_ms"] = round(duration * 1000)
    return telemetry, metadata


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="write normalized telemetry and metadata")
    args = parser.parse_args()
    recoverable = []
    unavailable = []
    for metadata_path in sorted(RUNS_ROOT.glob("*/*/*/metadata.json")):
        result = normalized_record(metadata_path.parent)
        if result is None:
            telemetry = load(metadata_path.parent / "telemetry/openhands-telemetry.json")
            if not isinstance(telemetry.get("usage", {}).get("input_tokens"), (int, float)):
                unavailable.append(metadata_path.parent)
            continue
        recoverable.append((metadata_path.parent, result))
    print(f"recoverable={len(recoverable)}")
    for run_dir, (telemetry, _) in recoverable:
        usage = telemetry["usage"]
        print(f"{run_dir.relative_to(RUNS_ROOT)} input={usage['input_tokens']} output={usage['output_tokens']} cache={usage['cache_read_tokens']}")
    print(f"unavailable={len(unavailable)}")
    for run_dir in unavailable:
        print(run_dir.relative_to(RUNS_ROOT))
    if args.apply:
        for run_dir, (telemetry, metadata) in recoverable:
            (run_dir / "telemetry/openhands-telemetry.json").write_text(json.dumps(telemetry, indent=2) + "\n", encoding="utf-8")
            (run_dir / "metadata.json").write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")
        print(f"updated={len(recoverable)}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
