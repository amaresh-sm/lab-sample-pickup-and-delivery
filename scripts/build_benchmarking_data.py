"""Build benchmarking-data.md exclusively from archived benchmarking runs."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runs-root", type=Path, default=Path("benchmarking-runs/runs"))
    parser.add_argument("--output", type=Path, default=Path("benchmarking-data.md"))
    return parser.parse_args()


def load(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise ValueError(f"expected JSON object: {path}")
    return value


def integer(value: Any) -> str:
    return "unavailable" if not isinstance(value, (int, float)) else f"{int(value):,}"


def compact_integer(value: Any) -> str:
    """Format token counts using the compact K/M/B style used in benchmark tables."""
    if not isinstance(value, (int, float)):
        return "unavailable"
    amount = float(value)
    absolute = abs(amount)
    for threshold, suffix in ((1_000_000_000, "B"), (1_000_000, "M"), (1_000, "K")):
        if absolute >= threshold:
            return f"{amount / threshold:.1f}{suffix}"
    return f"{int(amount):,}"


def money(value: Any) -> str:
    return "unavailable" if not isinstance(value, (int, float)) else f"${value:.2f}"


def duration_seconds(metadata: dict[str, Any], telemetry: dict[str, Any]) -> float | None:
    milliseconds = telemetry.get("timing", {}).get("duration_ms")
    if isinstance(milliseconds, (int, float)):
        return milliseconds / 1000
    value = metadata.get("duration_seconds")
    return float(value) if isinstance(value, (int, float)) else None


def token_total(usage: dict[str, Any]) -> int | None:
    """Return input + output; cache tokens are already included in input."""
    input_tokens = usage.get("input_tokens")
    output_tokens = usage.get("output_tokens")
    if not isinstance(input_tokens, (int, float)) or not isinstance(output_tokens, (int, float)):
        return None
    return int(input_tokens) + int(output_tokens)


def token_breakdown(usage: dict[str, Any]) -> str:
    """Render provider-normalized input/output/reasoning/cache-read usage."""
    keys = ("input_tokens", "output_tokens", "reasoning_tokens", "cache_read_tokens")
    approximate = set(usage.get("approximate_fields", []))
    return "/".join(
        ("≈" if key in approximate else "") + compact_integer(usage.get(key))
        for key in keys
    )


def estimate_cost(telemetry: dict[str, Any]) -> float | None:
    """Use the archive's materialized custom estimate based on normalized usage."""
    value = telemetry.get("cost", {}).get("custom_estimation", {}).get("amount_usd")
    return float(value) if isinstance(value, (int, float)) else None


def display_run_name(run_id: Any, fallback: str) -> str:
    """Normalize archival repair prefixes out of the human-facing report."""
    value = str(run_id if isinstance(run_id, str) and run_id else fallback)
    return value.removeprefix("repaired-")


def row(run_dir: Path) -> dict[str, Any]:
    metadata = load(run_dir / "metadata.json")
    telemetry = load(run_dir / "telemetry" / "openhands-telemetry.json")
    score = load(run_dir / "score" / "hidden.score.json")
    usage = telemetry.get("usage", {})
    core = metadata.get("core_metrics", {})
    largest = core.get("largest_file") if core.get("status") == "available" else None
    iterations = telemetry.get("timing", {}).get("llm_requests", {}).get("count")
    tools = telemetry.get("tools", {}).get("total")
    return {
        "run": display_run_name(metadata.get("run_id"), run_dir.name),
        "model": metadata.get("model", "—"),
        "reasoning": metadata.get("reasoning", "—"),
        "score": float(score["score"]),
        "tokens": token_total(usage),
        "token_breakdown": token_breakdown(usage),
        "tools": tools,
        "iterations": iterations if isinstance(iterations, (int, float)) and iterations > 0 else tools,
        "estimated_cost": estimate_cost(telemetry),
        "estimated_cost_approximate": telemetry.get("usage", {}).get("precision") == "rounded_cli_summary",
        "reported_cost": telemetry.get("cost", {}).get("gateway", {}).get("amount_usd"),
        "duration": duration_seconds(metadata, telemetry),
        "duration_approximate": metadata.get("duration_source") == "inferred_from_candidate_lifecycle",
        "files": core.get("files") if core.get("status") == "available" else None,
        "loc": core.get("loc") if core.get("status") == "available" else None,
        "dependencies": core.get("dependencies") if core.get("status") == "available" else None,
        "largest": f"{largest['path']} ({largest['loc']} LOC)" if largest else "—",
    }


def render(rows: list[dict[str, Any]]) -> str:
    rows.sort(key=lambda item: (-item["score"], item["run"]))
    lines = [
        "# Benchmarking Data",
        "",
        "Generated only from `benchmarking-runs` metadata, telemetry, and score artifacts. Token is normalized input + output; cached input is already included in input. Estimated cost is the archived custom estimate calculated from normalized input, output, cache-read, and cache-write token usage. `unavailable` means the original archived run retained no evidence for that metric and has not been inferred. Core file metrics exclude generated/dependency/cache output, lockfiles, logs, database dumps/migrations, and contracts.",
        "",
        "| Run-name (unique Id) | Model Name | Reasoning | score | Token (input/output/reasoning/cache-read) | tool calls | Iteration steps | Est cost | Reported cost | Duration | Total Files | Loc | deps | largest file |",
        "|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|",
    ]
    for item in rows:
        duration = "unavailable" if item["duration"] is None else f"{'≈' if item['duration_approximate'] else ''}{item['duration']:.1f}s"
        lines.append("| " + " | ".join([
            str(item["run"]),
            str(item["model"]),
            str(item["reasoning"]),
            f"{item['score']:.4f}",
            item["token_breakdown"],
            integer(item["tools"]),
            integer(item["iterations"]),
            ("≈" if item["estimated_cost_approximate"] and isinstance(item["estimated_cost"], (int, float)) else "") + money(item["estimated_cost"]),
            money(item["reported_cost"]),
            duration,
            integer(item["files"]),
            integer(item["loc"]),
            integer(item["dependencies"]),
            str(item["largest"]).replace("|", "\\|"),
        ]) + " |")
    return "\n".join(lines) + "\n"


def main() -> int:
    args = parse_args()
    run_dirs = sorted(path.parent.parent for path in args.runs_root.glob("*/*/*/score/hidden.score.json"))
    args.output.write_text(render([row(path) for path in run_dirs]), encoding="utf-8")
    print(f"wrote {len(run_dirs)} rows to {args.output}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
