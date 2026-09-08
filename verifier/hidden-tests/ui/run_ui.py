#!/usr/bin/env python3
"""Scorer for the deterministic UI verifier (phase 1).

    python3 run_ui.py score \\
        --frontend-url http://127.0.0.1:18082 \\
        --base-url     http://127.0.0.1:18082 \\
        --contract tasks/public/contracts/openapi.contract.json \\
        --admin-user admin --admin-pass admin123 \\
        --json-out /logs/ui-reward.json

Discovers every scenario under ``scenarios/`` (each exposing a module-level
``CAPABILITY``), seeds its state over HTTP, drives a headless, network-isolated
Chromium against ``--frontend-url``, and writes a SEPARATE ``ui-reward.json``
sub-score. It is intentionally decoupled from the API engine's ``run.py`` so
the UI score reads independently of the API score.

Run it INSIDE the application's container (same network namespace) so the
loopback ``--frontend-url`` resolves and no traffic leaves the box -- the same
way the HTTP engine runs in-container. Requires the ``playwright`` Python
package and a Chromium build (the base image ships one at
``$PLAYWRIGHT_BROWSERS_PATH``).
"""
from __future__ import annotations

import argparse
import importlib
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from urllib.parse import urlsplit

UI_DIR = Path(__file__).resolve().parent           # engine/ui_capabilities/
ENGINE_DIR = UI_DIR.parent                          # engine/
SCEN_DIR = UI_DIR / "scenarios"
for _p in (str(ENGINE_DIR), str(UI_DIR), str(SCEN_DIR)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

from playwright.sync_api import sync_playwright  # noqa: E402

from model import UiCapability  # noqa: E402
from driver import UiEngine  # noqa: E402


def discover() -> list[UiCapability]:
    caps: list[UiCapability] = []
    seen: set[str] = set()
    for f in sorted(SCEN_DIR.glob("*.py")):
        if f.stem == "__init__":
            continue
        module = importlib.import_module(f.stem)
        cap = getattr(module, "CAPABILITY", None)
        if cap is None:
            continue
        if cap.id in seen:
            raise ValueError(f"duplicate UI capability id {cap.id!r} (from {f.name})")
        seen.add(cap.id)
        caps.append(cap)
    return caps


def _manifest_info(manifest_path: str | None) -> dict | None:
    """Load just enough of ``app-setup/manifest.json`` to restart the app
    between scenarios: the reset/start commands, the ready URL, and the port
    to kill. Returns None (restarts disabled) if no manifest was given."""
    if not manifest_path:
        return None
    manifest = json.loads(Path(manifest_path).read_text())
    commands = manifest.get("commands", {})
    urls = manifest.get("urls", {})
    frontend = urls.get("frontend") or urls.get("api")
    port = urlsplit(frontend).port if frontend else None
    return {
        "reset_cmd": list(commands.get("reset") or []),
        "start_cmd": list(commands.get("start") or []),
        "ready_url": urls.get("ready"),
        "port": port,
    }


def _restart_app(info: dict, label: str, timeout: float = 90.0) -> bool:
    """Kill whatever is bound to the app's known port (binary-name-agnostic --
    works for ANY candidate's process, since only the port is knowable in
    advance), reset the DB, relaunch the start command, and poll ready.

    This uses the same restart isolation as the API engine
    gets between every capability; the UI engine never had it. A single
    scenario's own leftover workers/locations/connections otherwise pile up on
    ONE never-restarted live app across all 15 scenarios -- confirmed live
    (terra/opus/luna-xhigh): a seeder that succeeds on a fresh app fails after
    a handful of PRIOR scenarios have run against the same unrestarted
    process, purely from resource pile-up, not a candidate defect.

    Resetting the DB WITHOUT restarting the process was tried first and
    reverted: the admin user is bootstrapped only once, at process start, so a
    live reset against a still-running process permanently kills admin auth
    and can leave in-memory caches pointing at deleted rows without ever
    failing the health check -- confirmed live to cause a silent false
    positive (a candidate wrongly accepting invalid input with 201) after only
    ONE such reset. Restarting the actual process on every cycle avoids that
    entire class of bug because every cycle gets a genuinely fresh boot.
    """
    if os.environ.get("ASTRA_EXTERNAL_LIFECYCLE") == "1":
        control_dir = Path(os.environ.get("ASTRA_CONTROL_DIR", "/output/control"))
        control_dir.mkdir(parents=True, exist_ok=True)
        request = control_dir / f"reset-start-ui-{label}.json"
        acknowledgement = control_dir / f"reset-start-ui-{label}.ack"
        request.write_text(json.dumps({"action": "reset-start", "label": f"ui-{label}"}) + "\n", encoding="utf-8")
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if acknowledgement.is_file():
                return True
            time.sleep(0.2)
        return False

    port = info.get("port")
    if port:
        subprocess.run(["fuser", "-k", f"{port}/tcp"], capture_output=True, timeout=15)
        time.sleep(1)
    if info.get("reset_cmd"):
        subprocess.run(info["reset_cmd"], cwd="/workspace", check=True, capture_output=True, timeout=60)
    if info.get("start_cmd"):
        with open("/logs/ui-app-start.log", "a") as log:
            subprocess.Popen(
                info["start_cmd"], cwd="/workspace", stdout=log, stderr=subprocess.STDOUT,
                start_new_session=True,
            )
    ready_url = info.get("ready_url")
    if not ready_url:
        return True
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(ready_url, timeout=3) as resp:
                if resp.status == 200:
                    return True
        except (urllib.error.URLError, OSError):
            pass
        time.sleep(2)
    return False


def cmd_score(args: argparse.Namespace) -> int:
    caps = discover()
    caps.sort(key=lambda c: c.id)
    manifest_info = _manifest_info(args.manifest)
    rows = []
    total = 0.0
    with sync_playwright() as pw:
        engine = UiEngine(
            playwright=pw,
            frontend_url=args.frontend_url,
            base_url=args.base_url,
            contract_path=args.contract,
            admin_user=args.admin_user,
            admin_pass=args.admin_pass,
            timeout_ms=args.timeout_ms,
        )
        try:
            for cap in caps:
                if manifest_info:
                    if not _restart_app(manifest_info, cap.id):
                        print(f"  !! app never became ready after restart before {cap.id}", file=sys.stderr)
                try:
                    report = cap.run(engine)
                    credit = report.credit()
                except Exception as exc:  # a scenario's OWN bug must not crash the run
                    from model import UiReport, UiVerdict

                    report = UiReport(positive=UiVerdict(False, f"scenario raised {exc!r}"))
                    credit = 0.0
                    print(f"  !! {cap.id} raised {exc!r}", file=sys.stderr)
                total += credit
                rows.append((cap, credit, report))
                flag = "*" if cap.priority else " "
                print(f"{flag} {cap.id:<38} {cap.ac:<12} credit={credit:.3f}")
                if args.verbose and report:
                    print(f"      positive: {report.positive.detail}")
                    if report.negative:
                        print(f"      control:  {report.negative.detail}")
        finally:
            engine.close()

    overall = total / len(caps) if caps else 0.0
    print()
    print(f"UI capabilities graded: {len(caps)}")
    print(f"UI overall score:       {overall:.4f}")

    if args.json_out:
        payload = {
            "overall": overall,
            "kind": "ui",
            "capabilities": [
                {
                    "id": cap.id,
                    "ac": cap.ac,
                    "credit": credit,
                    "positive": {"ok": report.positive.ok, "detail": report.positive.detail},
                    "negative": (
                        {"ok": report.negative.ok, "detail": report.negative.detail}
                        if report.negative
                        else None
                    ),
                    "notes": report.notes,
                }
                for cap, credit, report in rows
            ],
        }
        Path(args.json_out).write_text(json.dumps(payload, indent=2, default=str))
        print(f"wrote {args.json_out}")
    return 0


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--frontend-url", required=True, help="origin the browser drives (manifest urls.frontend)")
    p.add_argument("--base-url", required=True, help="server origin for the HTTP seeder")
    p.add_argument("--contract", default="tasks/public/contracts/openapi.contract.json")
    p.add_argument(
        "--manifest",
        default=None,
        help="path to app-setup/manifest.json; when given, restarts the app (kill by port, "
        "reset, relaunch, wait ready) before EVERY scenario for full isolation. Omit to disable.",
    )
    p.add_argument("--admin-user", default="admin")
    p.add_argument("--admin-pass", default="admin123")
    p.add_argument("--timeout-ms", type=int, default=10_000)
    sub = p.add_subparsers(dest="command", required=True)
    ps = sub.add_parser("score", help="run every UI scenario once and write ui-reward.json")
    ps.add_argument("--verbose", action="store_true")
    ps.add_argument("--json-out", default=None)
    ps.set_defaults(func=cmd_score)
    args = p.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
