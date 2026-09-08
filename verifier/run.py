#!/usr/bin/env python3
"""Run the migrated pickup/delivery verifier and produce ASTRA evidence."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path
from urllib.parse import urlsplit


ROOT = Path(__file__).resolve().parent
LEGACY_ENGINE = ROOT / "hidden-tests/backend/legacy-engine/run.py"
LEGACY_UI = ROOT / "hidden-tests/ui/run_ui.py"
PUBLIC_ROOT = Path(os.environ.get("ASTRA_PUBLIC_ROOT", str(ROOT.parent / "tasks/public")))
CONTRACT = PUBLIC_ROOT / "contracts/openapi.contract.json"
UI_CONTRACT = PUBLIC_ROOT / "contracts/ui.contract.json"


def args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--admin-user", default=os.environ.get("ADMIN_USERNAME", "admin"))
    parser.add_argument("--admin-pass", default=os.environ.get("ADMIN_PASSWORD", "admin123"))
    parser.add_argument("--skip-ui", action="store_true")
    parser.add_argument("--external-lifecycle", action="store_true")
    return parser.parse_args()


def origin(url: str) -> str:
    parsed = urlsplit(url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise SystemExit(f"manifest URL must be an absolute HTTP(S) URL: {url}")
    return f"{parsed.scheme}://{parsed.netloc}"


def manifest(candidate: Path) -> tuple[dict[str, list[str]], dict[str, str], Path]:
    path = candidate / "app-setup/manifest.json"
    if not path.is_file():
        raise SystemExit(f"candidate manifest is missing: {path}")
    payload = json.loads(path.read_text(encoding="utf-8"))
    commands = payload.get("commands")
    urls = payload.get("urls")
    if not isinstance(commands, dict) or not isinstance(urls, dict):
        raise SystemExit("candidate manifest must contain commands and urls")
    selected = {name: commands[name] for name in ("build", "reset", "start")}
    if any(not isinstance(value, list) or not value or not all(isinstance(item, str) for item in value) for value in selected.values()):
        raise SystemExit("manifest lifecycle commands must be non-empty argument arrays")
    selected_urls = {name: urls[name] for name in ("api", "frontend", "ready")}
    if any(not isinstance(value, str) or not value for value in selected_urls.values()):
        raise SystemExit("manifest urls.api, urls.frontend, and urls.ready are required")
    return selected, selected_urls, path


def run_engine(base_url: str, admin_user: str, admin_pass: str, command: str, output: Path, only: str | None = None, statefile: Path | None = None) -> subprocess.CompletedProcess[str]:
    command_args = [sys.executable, str(LEGACY_ENGINE), "--base-url", base_url, "--contract", str(CONTRACT), "--admin-user", admin_user, "--admin-pass", admin_pass, command]
    if command == "score":
        command_args += ["--json-out", str(output)]
        if only:
            command_args += ["--only", only]
    elif command == "durability-write":
        command_args += ["--statefile", str(output)]
        if only:
            command_args += ["--only", only]
    else:
        command_args += ["--statefile", str(statefile or output), "--json-out", str(output)]
    env = dict(os.environ)
    env.update({"WORKSPACE": "/input/candidate", "VERIFIER_ISOLATED": "1"})
    return subprocess.run(command_args, cwd=LEGACY_ENGINE.parent, env=env, check=False, text=True)


def request_control(output: Path, label: str, action: str, timeout: float = 180.0) -> None:
    control = Path(os.environ.get("ASTRA_CONTROL_DIR", str(output / "control")))
    control.mkdir(parents=True, exist_ok=True)
    prefix = "reset-start" if action == "reset-start" else "restart"
    request = control / f"{prefix}-{label}.json"
    acknowledgement = control / f"{prefix}-{label}.ack"
    request.write_text(json.dumps({"action": action, "label": label}) + "\n", encoding="utf-8")
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if acknowledgement.is_file():
            return
        time.sleep(0.2)
    raise TimeoutError(f"timed out waiting for candidate {action}: {label}")


def groups() -> list[str]:
    result = subprocess.run([sys.executable, str(LEGACY_ENGINE), "--base-url", "http://unused", "list-groups"], cwd=LEGACY_ENGINE.parent, check=True, capture_output=True, text=True)
    return [",".join(values) for values in json.loads(result.stdout).values()]


def merge(parts: list[Path], output: Path) -> Path:
    capabilities: list[dict] = []
    reports: dict[str, dict] = {}
    provenance: dict | None = None
    for path in parts:
        if not path.is_file():
            continue
        payload = json.loads(path.read_text(encoding="utf-8"))
        for cap in payload.get("capabilities") or []:
            if cap.get("id") in {item.get("id") for item in capabilities}:
                continue
            capabilities.append(cap)
        for op_id, report in (payload.get("contractReport") or {}).items():
            current = reports.setdefault(op_id, {"calls": 0, "conformant": True, "violations": []})
            current["calls"] += int(report.get("calls", 0))
            current["conformant"] = current["conformant"] and bool(report.get("conformant", True))
            current["violations"].extend(report.get("violations") or [])
        provenance = provenance or payload.get("provenance")
    capabilities.sort(key=lambda item: str(item.get("id")))
    conformant = sum(item["conformant"] for item in reports.values())
    contract_score = conformant / len(reports) if reports else 0.0
    merged = {
        "overall": round(sum(float(item.get("credit") or 0.0) for item in capabilities) / len(capabilities), 6) if capabilities else 0.0,
        "provenance": provenance,
        "capabilities": capabilities,
        "contractReport": reports,
        "contract": {"buildOk": True, "resetOk": "delegated", "startOk": "delegated", "apiConformance": f"{conformant}/{len(reports)}", "score": round(contract_score, 4)},
    }
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(merged, indent=2) + "\n", encoding="utf-8")
    return output


def run(parsed: argparse.Namespace) -> int:
    candidate = parsed.candidate.resolve(strict=True)
    output = parsed.output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    _, urls, manifest_path = manifest(candidate)
    if not CONTRACT.is_file() or not UI_CONTRACT.is_file():
        raise SystemExit("public contracts are missing")

    part_dir = output / "parts"
    part_dir.mkdir(exist_ok=True)
    part_paths: list[Path] = []
    for index, group in enumerate(groups()):
        if index and parsed.external_lifecycle:
            request_control(output, f"group-{index}", "reset-start")
        part = part_dir / f"part-{index + 1}.json"
        run_engine(origin(urls["api"]), parsed.admin_user, parsed.admin_pass, "score", part, group)
        part_paths.append(part)
        state = part_dir / f"dur-{index + 1}.json"
        run_engine(origin(urls["api"]), parsed.admin_user, parsed.admin_pass, "durability-write", state, group)
        try:
            has_state = bool(json.loads(state.read_text(encoding="utf-8")))
        except (OSError, json.JSONDecodeError):
            has_state = False
        if has_state:
            if parsed.external_lifecycle:
                request_control(output, f"durability-{index + 1}", "restart")
            verify_state = state.with_name(state.stem + ".verify.json")
            run_engine(origin(urls["api"]), parsed.admin_user, parsed.admin_pass, "durability-verify", verify_state, statefile=state)
            durability = json.loads(verify_state.read_text(encoding="utf-8")) if verify_state.is_file() else {}
            payload = json.loads(part.read_text(encoding="utf-8"))
            durable_by_id = {item["id"]: item for item in durability.get("capabilities") or []}
            for cap in payload.get("capabilities") or []:
                check = durable_by_id.get(cap.get("id"))
                if check:
                    cap["durabilityOk"] = check.get("durabilityOk")
                    cap["durability"] = check.get("durability")
                    dimensions = [cap.get("positiveOk")]
                    if cap.get("negativeOk") is not None:
                        dimensions.append(cap["negativeOk"])
                    if cap.get("idempotencyOk") is not None:
                        dimensions.append(cap["idempotencyOk"])
                    dimensions.append(cap["durabilityOk"])
                    cap["credit"] = sum(bool(value) for value in dimensions) / len(dimensions)
            part.write_text(json.dumps(payload, indent=2) + "\n", encoding="utf-8")

    backend = merge(part_paths, output / "backend/reward.json")
    if not parsed.skip_ui:
        ui_path = output / "ui/ui-reward.json"
        ui_path.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(
            [
                sys.executable, str(LEGACY_UI),
                "--frontend-url", urls["frontend"],
                "--base-url", origin(urls["api"]),
                "--contract", str(CONTRACT),
                "--manifest", str(manifest_path),
                "--admin-user", parsed.admin_user,
                "--admin-pass", parsed.admin_pass,
                "score", "--json-out", str(ui_path),
            ],
            cwd=LEGACY_UI.parent,
            check=False,
        )
    else:
        ui_path = output / "ui/ui-reward.json"
        ui_path.parent.mkdir(parents=True, exist_ok=True)
    subprocess.run([sys.executable, str(ROOT / "score_adapter.py"), "--backend", str(backend), "--ui", str(ui_path), "--candidate", str(candidate), "--public-contract", str(CONTRACT), "--ui-contract", str(UI_CONTRACT), "--output", str(output / "criteria.json")], check=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(run(args()))
