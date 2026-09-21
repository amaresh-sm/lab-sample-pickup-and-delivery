"""Apply archived cache backfill and rebuild benchmarking-data.md."""

from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path

from build_benchmarking_data import render as render_benchmarking_data
from build_benchmarking_data import row as benchmarking_data_row


ROOT = Path("benchmarking-runs")
RUNS = ROOT / "runs"
REPORT_PATH = ROOT / "cache-backfill-dry-run.json"
NOW = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")

CODE_SUFFIXES = {
    ".go", ".ts", ".tsx", ".js", ".jsx", ".css", ".scss", ".html", ".py",
    ".sh", ".bash", ".rs", ".java", ".kt", ".rb", ".php", ".c", ".h",
    ".cc", ".cpp", ".hpp", ".swift", ".vue", ".svelte", ".proto",
}
EXCLUDED_DIRS = {
    ".git", "node_modules", ".npm-cache", ".cache", ".gocache", ".gopath", "gopath", "__pycache__",
    ".pytest_cache", "dist", "build", "coverage", "target", "vendor", "logs",
    "tmp", "temp",
}
EXCLUDED_PARTS = {"migrations", "migration", "seed", "seeds", "fixtures", "dumps", "dump", "cache", "caches"}
EXCLUDED_NAMES = {"package-lock.json", "yarn.lock", "pnpm-lock.yaml", "go.sum", "Cargo.lock", "composer.lock", "poetry.lock", "Pipfile.lock"}


def resolve_run(item: dict) -> Path:
    exact = RUNS / item["model"] / item["reasoning"] / item["run"]
    choices = [exact] if exact.is_dir() else []
    choices.extend(p.parents[1] for p in RUNS.glob(f"{item['model']}/{item['reasoning']}/*/telemetry/openhands-telemetry.json"))
    choices = list(dict.fromkeys(choices))
    matches = []
    for directory in choices:
        try:
            metadata = json.loads((directory / "metadata.json").read_text())
            telemetry = json.loads((directory / "telemetry/openhands-telemetry.json").read_text())
            if (
                metadata.get("model") == item["model"]
                and metadata.get("reasoning") == item["reasoning"]
                and int(telemetry.get("usage", {}).get("input_tokens") or 0) == int(item["input"])
            ):
                matches.append(directory)
        except (OSError, json.JSONDecodeError):
            continue
    if len(matches) != 1:
        raise RuntimeError(f"could not uniquely resolve {item}: {matches}")
    return matches[0]


def candidate_root(metadata: dict) -> Path | None:
    source = Path(metadata.get("source_run_path", ""))
    choices = []
    if str(source) and source != Path("."):
        choices.append(source / "candidate")
    if metadata.get("run_id"):
        choices.extend([
            Path("benchmarking-candidates") / metadata["run_id"] / "candidate",
            Path("benchmarking-candidates/repaired") / metadata["run_id"] / "candidate",
        ])
    return next((path for path in choices if path.is_dir()), None)


def core_metrics(metadata: dict) -> dict:
    root = candidate_root(metadata)
    if root is None:
        return {"status": "unavailable", "reason": "candidate_workspace_not_present"}
    files: list[tuple[int, str]] = []
    dependencies = 0
    for path in root.rglob("*"):
        if not path.is_file():
            continue
        relative = path.relative_to(root)
        relative_parts = relative.parts[:-1]
        parts = set(relative_parts)
        is_go_module_cache = any(
            relative_parts[index:index + 2] == ("pkg", "mod")
            for index in range(len(relative_parts) - 1)
        )
        if parts & EXCLUDED_DIRS or parts & EXCLUDED_PARTS or is_go_module_cache:
            continue
        if path.name in EXCLUDED_NAMES or path.suffix.lower() not in CODE_SUFFIXES:
            continue
        try:
            data = path.read_bytes()
        except OSError:
            continue
        if b"\x00" in data[:8192]:
            continue
        try:
            lines = len(data.decode("utf-8").splitlines())
        except UnicodeDecodeError:
            continue
        files.append((lines, str(relative).replace("\\", "/")))

    for path in root.rglob("package.json"):
        if any(part in EXCLUDED_DIRS for part in path.relative_to(root).parts[:-1]):
            continue
        try:
            package = json.loads(path.read_text())
        except (OSError, json.JSONDecodeError):
            continue
        dependencies += len(package.get("dependencies", {})) + len(package.get("devDependencies", {}))
    for path in root.rglob("go.mod"):
        if any(part in EXCLUDED_DIRS for part in path.relative_to(root).parts[:-1]):
            continue
        inside = False
        for raw in path.read_text(errors="ignore").splitlines():
            line = raw.strip()
            if line.startswith("require ("):
                inside = True
            elif inside and line == ")":
                inside = False
            elif inside and line and not line.startswith("//"):
                dependencies += 1
            elif not inside and line.startswith("require "):
                dependencies += 1
    for path in [*root.rglob("requirements.txt"), *root.rglob("requirements-dev.txt")]:
        if any(part in EXCLUDED_DIRS for part in path.relative_to(root).parts[:-1]):
            continue
        dependencies += sum(
            1 for line in path.read_text(errors="ignore").splitlines()
            if line.strip() and not line.lstrip().startswith(("#", "-"))
        )

    largest = max(files, default=None)
    return {
        "status": "available",
        "files": len(files),
        "loc": sum(lines for lines, _ in files),
        "dependencies": dependencies,
        "largest_file": {"path": largest[1], "loc": largest[0]} if largest else None,
    }


def update_cache(directory: Path, item: dict) -> None:
    telemetry_path = directory / "telemetry/openhands-telemetry.json"
    metadata_path = directory / "metadata.json"
    telemetry = json.loads(telemetry_path.read_text())
    usage = telemetry.setdefault("usage", {})
    input_tokens = int(usage.get("input_tokens") or 0)
    current_cached = int(usage.get("cached_input_tokens") or 0)
    current_read = int(usage.get("cache_read_tokens") or 0)
    proposed = int(item["cached"])
    already_applied = (
        telemetry.get("cache_backfill", {}).get("status") == "applied"
        and current_cached == proposed
        and current_read == proposed
    )
    if not already_applied:
        if input_tokens <= 0 or current_cached != 0 or current_read != 0:
            raise RuntimeError(f"eligibility changed for {telemetry_path}")
        if proposed < 0 or proposed > input_tokens:
            raise RuntimeError(f"invalid cache value for {telemetry_path}")
        usage["cached_input_tokens"] = proposed
        usage["cache_read_tokens"] = proposed

    provenance = {
        "status": "applied",
        "ratio": item["ratio"],
        "ratio_source": item["ratio_source"],
        "previous_cached_input_tokens": 0,
        "previous_cache_read_tokens": 0,
        "updated_at": telemetry.get("cache_backfill", {}).get("updated_at", NOW),
    }
    telemetry["cache_backfill"] = provenance
    custom = telemetry.setdefault("cost", {}).setdefault("custom_estimation", {})
    pricing = custom.get("pricing", {})
    input_rate = pricing.get("input_usd_per_million")
    output_rate = pricing.get("output_usd_per_million")
    cache_rate = pricing.get("cache_read_usd_per_million")
    estimated = custom.get("amount_usd")
    if input_rate is not None and output_rate is not None and cache_rate is not None:
        output_tokens = int(usage.get("output_tokens") or 0)
        cached_tokens = int(usage.get("cached_input_tokens") or 0)
        estimated = round(
            ((input_tokens - cached_tokens) * float(input_rate)
             + cached_tokens * float(cache_rate)
             + output_tokens * float(output_rate)) / 1_000_000,
            12,
        )
        custom["amount_usd"] = estimated
        custom["status"] = "estimated"
        custom["cache_backfill"] = provenance
    telemetry_path.write_text(json.dumps(telemetry, indent=2, ensure_ascii=False) + "\n")

    metadata = json.loads(metadata_path.read_text())
    metadata["usage"] = usage
    metadata["cost"] = {
        "estimated_usd": estimated,
        "reported_usd": telemetry.get("cost", {}).get("gateway", {}).get("amount_usd"),
        "currency": "USD",
    }
    metadata["cache_backfill"] = provenance
    metadata_path.write_text(json.dumps(metadata, indent=2, ensure_ascii=False) + "\n")
    item["run"] = metadata.get("run_id", item["run"])


def rebuild_markdown() -> int:
    rows = []
    for metadata_path in sorted(RUNS.glob("*/*/*/metadata.json")):
        metadata = json.loads(metadata_path.read_text())
        directory = metadata_path.parent
        telemetry = json.loads((directory / "telemetry/openhands-telemetry.json").read_text())
        score = json.loads((directory / "score/hidden.score.json").read_text())
        metrics = core_metrics(metadata)
        usage = telemetry.get("usage", {})
        custom = telemetry.get("cost", {}).get("custom_estimation", {})
        gateway = telemetry.get("cost", {}).get("gateway", {})
        metadata["usage"] = usage
        metadata["cost"] = {
            "estimated_usd": custom.get("amount_usd"),
            "reported_usd": gateway.get("amount_usd"),
            "currency": "USD",
        }
        metadata["core_metrics"] = metrics
        metadata_path.write_text(json.dumps(metadata, indent=2, ensure_ascii=False) + "\n")
        duration = metadata.get("duration_seconds")
        if duration is None:
            duration = telemetry.get("timing", {}).get("duration_ms")
            duration = duration / 1000 if duration is not None else None
        largest = metrics.get("largest_file") if metrics.get("status") == "available" else None
        rows.append({
            "run": metadata.get("run_id", directory.name),
            "model": metadata.get("model", "—"),
            "reasoning": metadata.get("reasoning", "—"),
            "score": float(score["score"]),
            "tokens": usage.get("total_tokens"),
            "tools": telemetry.get("tools", {}).get("total"),
            "estimated": custom.get("amount_usd"),
            "reported": gateway.get("amount_usd"),
            "duration": duration,
            "files": metrics.get("files") if metrics.get("status") == "available" else None,
            "loc": metrics.get("loc") if metrics.get("status") == "available" else None,
            "deps": metrics.get("dependencies") if metrics.get("status") == "available" else None,
            "largest": f"{largest['path']} ({largest['loc']} LOC)" if largest else "—",
        })
    rows.sort(key=lambda row: (-row["score"], row["run"]))

    def integer(value):
        return "—" if value is None else f"{int(value):,}"

    def money(value):
        return "—" if value is None else f"${value:.2f}"

    lines = [
        "# Benchmarking Data", "",
        "Generated from `benchmarking-runs` telemetry, metadata, and score artifacts. Scores and costs are not regenerated or reverified. Core file metrics exclude dependency/cache/build output, lockfiles, logs, database dumps/migrations, contracts, and other generated artifacts.",
        "",
        "| Run-name (unique Id) | Model Name | Reasoning | score | token | tool calls | Iteration steps | Est cost | Reported cost | Duration | Total Files | Loc | deps | largest file |",
        "|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|",
    ]
    for row in rows:
        duration = "—" if row["duration"] is None else f"{row['duration']:.1f}s"
        largest = str(row["largest"]).replace("|", "\\|")
        lines.append("| " + " | ".join([
            str(row["run"]), str(row["model"]), str(row["reasoning"]), f"{row['score']:.4f}",
            integer(row["tokens"]), integer(row["tools"]), integer(row["tools"]), money(row["estimated"]),
            money(row["reported"]), duration, integer(row["files"]), integer(row["loc"]), integer(row["deps"]), largest,
        ]) + " |")
    refreshed_rows = [
        benchmarking_data_row(metadata_path.parent)
        for metadata_path in sorted(RUNS.glob("*/*/*/metadata.json"))
    ]
    Path("benchmarking-data.md").write_text(render_benchmarking_data(refreshed_rows), encoding="utf-8")
    return len(refreshed_rows)


def main() -> None:
    report = json.loads(REPORT_PATH.read_text())
    for item in report["runs"]:
        update_cache(resolve_run(item), item)
    count = rebuild_markdown()
    report["status"] = "applied"
    report["applied_at"] = NOW
    report["summary"]["updated_runs"] = len(report["runs"])
    report["summary"]["costs_updated"] = True
    REPORT_PATH.write_text(json.dumps(report, indent=2, ensure_ascii=False) + "\n")
    print(f"cache_backfill_updated={len(report['runs'])}")
    print(f"benchmark_rows={count}")


if __name__ == "__main__":
    main()
