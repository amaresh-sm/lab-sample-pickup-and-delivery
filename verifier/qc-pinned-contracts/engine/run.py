#!/usr/bin/env python3
"""CLI entrypoint for the pinned-contract grading engine.

    python3 run.py score --base-url http://127.0.0.1:8080 \\
        --admin-user admin --admin-pass admin123

    python3 run.py durability-write  --base-url ... --statefile /tmp/dur.json
    # ... restart the app under test (start-only, no reset) ...
    python3 run.py durability-verify --base-url ... --statefile /tmp/dur.json

Speaks only HTTP. See README.md for the capability-spec format and how a new
capability module plugs in (any `capabilities/*.py` exposing a module-level
`CAPABILITIES` list of `oracle.Capability` is picked up automatically).
"""
from __future__ import annotations

import argparse
import importlib
import json
import os
import pkgutil
import sys
from pathlib import Path

ENGINE_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(ENGINE_DIR))

from client import load_contract, make_admin_session, CONTRACT_PATH, CONTRACT_REPORT  # noqa: E402
from oracle import Engine, Capability  # noqa: E402
import capabilities  # noqa: E402


def discover_capabilities() -> list[Capability]:
    caps: list[Capability] = []
    seen_ids: set[str] = set()
    for module_info in pkgutil.iter_modules(capabilities.__path__, capabilities.__name__ + "."):
        module = importlib.import_module(module_info.name)
        for cap in getattr(module, "CAPABILITIES", []):
            if cap.id in seen_ids:
                raise ValueError(f"duplicate capability id {cap.id!r} (from {module_info.name})")
            seen_ids.add(cap.id)
            caps.append(cap)
    return caps


def _prefix_factory():
    # A random run-id, not just a counter: this engine is routinely re-run
    # against the SAME already-seeded application without a reset in
    # between (fast iteration while authoring specs), and a bare counter
    # starting back at e0001 collides with usernames/sample-type names a
    # previous run already created.
    import random
    import string

    run_id = "".join(random.choices(string.ascii_lowercase + string.digits, k=5))
    counter = {"n": 0}

    def _next() -> str:
        counter["n"] += 1
        return f"{run_id}{counter['n']:04d}"

    return _next


def _provenance(subset: list[str] | None = None) -> dict:
    """Identify the engine + run mode that produced a score. See the comment at
    the `provenance` key in cmd_score for why this exists."""
    def _git(*a: str) -> str | None:
        try:
            import subprocess
            return subprocess.run(["git", "-C", str(ENGINE_DIR), *a],
                                  capture_output=True, text=True, timeout=5).stdout.strip() or None
        except Exception:
            return None
    # The engine usually runs INSIDE the candidate container, where git cannot
    # resolve anything useful: /opt/gym is a read-only bind mount, and in a git
    # worktree `.git` is a pointer FILE to a path that is not mounted (git may
    # not even be installed). That silently produced engineCommit=None on the
    # first isolated run -- a provenance stamp that fails silently is worse than
    # none, since it looks populated. So the wrapper resolves this on the HOST
    # and passes it in; the local git attempt remains the fallback for direct
    # host-side invocations.
    commit = os.environ.get("VERIFIER_ENGINE_COMMIT") or _git("rev-parse", "HEAD")
    dirty_env = os.environ.get("VERIFIER_ENGINE_DIRTY")
    dirty = (dirty_env == "1") if dirty_env is not None else bool(_git("status", "--porcelain", "--", "."))
    prov = {
        "engineCommit": commit,
        "engineDirty": dirty,
        "engineCommitSource": "wrapper" if os.environ.get("VERIFIER_ENGINE_COMMIT") else ("local-git" if commit else "unavailable"),
        "mode": "isolated" if os.environ.get("VERIFIER_ISOLATED") == "1" else "shared",
    }
    if subset is not None:
        prov["subset"] = subset
        prov["subsetNote"] = "`overall` is the mean over this subset only -- NOT comparable to a full 52-capability score"
    return prov


def cmd_score(args: argparse.Namespace) -> int:
    contract = load_contract(Path(args.contract))
    admin = make_admin_session(args.base_url, contract, args.admin_user, args.admin_pass)
    if not admin.token:
        print("FATAL: could not authenticate the admin account", file=sys.stderr)
        return 2

    caps = discover_capabilities()
    caps.sort(key=lambda c: c.id)
    # `--only` runs a SUBSET of capabilities in this process. Added for
    # isolation experiments: the wrapper can reset+restart the app and then
    # score one capability (or one deliberately-coupled group, e.g. the
    # lifecycle step-capabilities that share `_drive_happy_path`) against a
    # pristine instance, instead of all 52 sharing accumulated state. Scoring
    # a subset makes `overall` a subset mean, so it is NOT comparable to a
    # full-suite score -- per-capability credits are what such a run is for.
    if getattr(args, "only", None):
        total = len(caps)
        wanted = {c.strip() for c in args.only.split(",") if c.strip()}
        unknown = wanted - {c.id for c in caps}
        if unknown:
            print(f"FATAL: unknown capability id(s): {', '.join(sorted(unknown))}", file=sys.stderr)
            return 2
        caps = [c for c in caps if c.id in wanted]
        print(f"[--only] scoring {len(caps)} of {total} capabilities: {', '.join(c.id for c in caps)}")
    engine = Engine(admin, _prefix_factory())

    rows = []
    total = 0.0
    for cap in caps:
        try:
            report = cap.run(engine)
            credit = report.credit()
        except Exception as exc:  # a capability's OWN bug must not crash the whole run
            credit = 0.0
            report = None
            print(f"  !! {cap.id} raised {exc!r}", file=sys.stderr)
        total += credit
        rows.append((cap.id, cap.kind, credit, report))
        flag = "*" if cap.priority else " "
        print(f"{flag} {cap.id:<32} {cap.kind:<10} credit={credit:.3f}")
        if report and args.verbose:
            print(f"      positive:    {report.positive.detail}")
            if report.negative:
                print(f"      negative:    {report.negative.detail}")
            if report.idempotency:
                print(f"      idempotency: {report.idempotency.detail}")

    overall = total / len(caps) if caps else 0.0
    print()
    print(f"capabilities graded: {len(caps)}")
    print(f"overall score:       {overall:.4f}")

    if args.json_out:
        payload = {
            "overall": overall,
            # PROVENANCE. Two scores are only comparable if they came from the
            # same engine, run the same way. Today a stale git worktree missing
            # the de-amplification fix silently produced scores with ~5x the
            # variance (0.1966 spread vs 0.0393) and nothing in the output said
            # so -- it cost hours of chasing a phantom "volatile cluster". These
            # fields make such a run self-identifying:
            #   engineCommit -- which engine produced this
            #   engineDirty  -- uncommitted edits present (so not reproducible)
            #   mode         -- "isolated" (pristine app per capability group) vs
            #                   "shared" (all capabilities on one instance).
            #                   Scores are NOT comparable across modes.
            #   subset       -- set when --only limited the run, because then
            #                   `overall` is a subset mean, not a suite score.
            "provenance": _provenance(subset=sorted(c.id for c in caps) if getattr(args, "only", None) else None),
            "capabilities": [
                {
                    "id": cid,
                    "kind": kind,
                    "credit": credit,
                    "positive": report.positive.detail if report else None,
                    "negative": report.negative.detail if report and report.negative else None,
                    "idempotency": report.idempotency.detail if report and report.idempotency else None,
                    # oks (with positiveOk always present when report is not
                    # None, negative/idempotencyOk only when that dim ran) let
                    # a later, SEPARATE process -- isolated mode's durability
                    # restart cycle -- fold in a durability Verdict and
                    # recompute credit via oracle.credit_from_oks without
                    # having to invert the rounded `credit` float back into
                    # dimension counts.
                    "positiveOk": report.positive.ok if report else None,
                    "negativeOk": report.negative.ok if report and report.negative else None,
                    "idempotencyOk": report.idempotency.ok if report and report.idempotency else None,
                }
                for cid, kind, credit, report in rows
            ],
            # Contract conformance: a side effect of the HTTP calls the
            # capabilities above already made in THIS process -- no new
            # scenarios, no new calls. Only operations this run actually
            # exercised appear here; isolated mode's merge step (verify-isolated)
            # combines every group's contractReport into one whole-suite sweep,
            # the same pattern durability's dur-part-N.json merge already uses.
            "contractReport": {
                op_id: {
                    "calls": entry["calls"],
                    "conformant": not entry["violations"],
                    "violations": entry["violations"],
                }
                for op_id, entry in CONTRACT_REPORT.items()
            },
        }
        Path(args.json_out).write_text(json.dumps(payload, indent=2, default=str))
        print(f"wrote {args.json_out}")

    return 0


def cmd_list_groups(args: argparse.Namespace) -> int:
    """Emit the isolation grouping as JSON so the run wrapper never hardcodes it.
    Each group must share ONE app instance; groups are mutually independent and
    can each get a pristine app. Most groups have a single member -- only the
    lifecycle-drive capabilities are genuinely coupled."""
    groups: dict[str, list[str]] = {}
    for cap in sorted(discover_capabilities(), key=lambda c: c.id):
        groups.setdefault(cap.effective_group(), []).append(cap.id)
    ordered = dict(sorted(groups.items(), key=lambda kv: (len(kv[1]) == 1, kv[0])))
    print(json.dumps(ordered, indent=2))
    return 0


def cmd_durability_write(args: argparse.Namespace) -> int:
    contract = load_contract(Path(args.contract))
    admin = make_admin_session(args.base_url, contract, args.admin_user, args.admin_pass)
    caps = discover_capabilities()
    # `--only` mirrors cmd_score's: isolated mode calls this once per
    # capability-group, against that group's own pristine app, so writing
    # sentinels for every OTHER group's durability-wired capabilities here
    # would be pure waste (their sentinels would be destroyed by the next
    # group's reset before ever being verified).
    if getattr(args, "only", None):
        wanted = {c.strip() for c in args.only.split(",") if c.strip()}
        caps = [c for c in caps if c.id in wanted]
    engine = Engine(admin, _prefix_factory())
    state: dict[str, dict] = {}
    for cap in caps:
        if not cap.durability_write:
            continue
        try:
            state[cap.id] = cap.durability_write(engine)
            print(f"wrote sentinel for {cap.id}")
        except Exception as exc:
            print(f"  !! {cap.id} durability_write raised {exc!r}", file=sys.stderr)
    Path(args.statefile).write_text(json.dumps(state))
    print(f"{len(state)} durability sentinel(s) written to {args.statefile}")
    print("Now restart the application under test (start-only, do NOT reset), then run durability-verify.")
    return 0


def cmd_durability_verify(args: argparse.Namespace) -> int:
    contract = load_contract(Path(args.contract))
    admin = make_admin_session(args.base_url, contract, args.admin_user, args.admin_pass)
    if not admin.token:
        print("FATAL: could not re-authenticate after restart", file=sys.stderr)
        return 2
    caps = {c.id: c for c in discover_capabilities()}
    state = json.loads(Path(args.statefile).read_text())
    engine = Engine(admin, _prefix_factory())
    total = 0
    ok = 0
    rows = []
    for cap_id, sentinel in state.items():
        cap = caps.get(cap_id)
        if not cap or not cap.durability_verify:
            continue
        total += 1
        verdict = cap.durability_verify(engine, sentinel)
        ok += int(verdict.ok)
        rows.append((cap_id, verdict))
        print(f"{'OK ' if verdict.ok else 'FAIL'} {cap_id:<32} {verdict.detail}")
    print()
    print(f"durability: {ok}/{total} survived the restart")
    if args.json_out:
        payload = {
            "capabilities": [
                {"id": cap_id, "durabilityOk": verdict.ok, "durability": verdict.detail}
                for cap_id, verdict in rows
            ],
        }
        Path(args.json_out).write_text(json.dumps(payload, indent=2, default=str))
        print(f"wrote {args.json_out}")
    return 0 if ok == total else 1


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--admin-user", default="admin")
    parser.add_argument("--admin-pass", default="admin123")
    parser.add_argument("--contract", default=str(CONTRACT_PATH))
    sub = parser.add_subparsers(dest="command", required=True)

    p_score = sub.add_parser("score", help="run every capability's positive/negative/idempotency probes once")
    p_score.add_argument("--verbose", action="store_true")
    p_score.add_argument("--json-out", default=None)
    p_score.add_argument(
        "--only",
        default=None,
        help="comma-separated capability ids to score instead of all 52 (for isolation runs; "
             "`overall` then means the subset mean and is not comparable to a full-suite score)",
    )
    p_score.set_defaults(func=cmd_score)

    p_groups = sub.add_parser("list-groups", help="print the isolation grouping (JSON) used by the isolated run wrapper")
    p_groups.set_defaults(func=cmd_list_groups)

    p_write = sub.add_parser("durability-write", help="phase 1 of a durability sweep: write sentinels, print statefile")
    p_write.add_argument("--statefile", required=True)
    p_write.add_argument(
        "--only",
        default=None,
        help="comma-separated capability ids to write sentinels for instead of all durability-wired "
             "capabilities (for isolation runs, scoped to the current group)",
    )
    p_write.set_defaults(func=cmd_durability_write)

    p_verify = sub.add_parser("durability-verify", help="phase 2, run AFTER restarting the app under test")
    p_verify.add_argument("--statefile", required=True)
    p_verify.add_argument("--json-out", default=None)
    p_verify.set_defaults(func=cmd_durability_verify)

    args = parser.parse_args()
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
