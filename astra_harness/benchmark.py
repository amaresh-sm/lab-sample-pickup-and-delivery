"""Build a leaderboard from ASTRA normalized score and telemetry artifacts."""

from __future__ import annotations

import argparse
import json
from pathlib import Path


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--candidates-root",
        "--runs-root",
        dest="candidates_root",
        type=Path,
        default=Path("benchmarking-candidates"),
        help="directory containing candidate run artifacts (default: benchmarking-candidates)",
    )
    parser.add_argument("--output", type=Path, default=Path("benchmark.md"))
    return parser.parse_args()


def load(path: Path) -> dict:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return {}
    return value if isinstance(value, dict) else {}


def rows(runs_root: Path) -> list[dict]:
    result: list[dict] = []
    for run_dir in sorted(path for path in runs_root.glob("**/score.json") if path.parent.name == "reports"):
        score = load(run_dir)
        if not score or not isinstance(score.get("score"), (int, float)):
            continue
        root = run_dir.parent.parent
        metadata = load(root / "metadata.json")
        telemetry = load(root / "telemetry.json")
        result.append({
            "task": score.get("task_id", root.parent.name),
            "candidate": root.name,
            "model": metadata.get("model", "—"),
            "reasoning": metadata.get("reasoning", "—"),
            "score": float(score["score"]),
            "hard_pass": bool(score.get("hard_pass")),
            "duration": telemetry.get("duration_seconds", metadata.get("duration_seconds", "—")),
            "changed_files": (telemetry.get("solution_size") or {}).get("agent_changed_files", "—"),
        })
    return sorted(result, key=lambda row: row["score"], reverse=True)


def render(items: list[dict]) -> str:
    lines = [
        "# Candidate benchmark",
        "",
        "Normalized ASTRA task scores; each row is backed by reports/score.json and telemetry.json.",
        "",
        "| Task | Candidate | Model | Reasoning | Score | Hard pass | Duration (s) | Changed files |",
        "|---|---|---|---|---:|:---:|---:|---:|",
    ]
    for row in items:
        lines.append(f"| {row['task']} | {row['candidate']} | {row['model']} | {row['reasoning']} | {row['score']:.4f} | {'yes' if row['hard_pass'] else 'no'} | {row['duration']} | {row['changed_files']} |")
    return "\n".join(lines) + "\n"


def main() -> int:
    args = parse_args()
    output = render(rows(args.candidates_root.resolve()))
    args.output.write_text(output, encoding="utf-8")
    print(output, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
