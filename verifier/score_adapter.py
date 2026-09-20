#!/usr/bin/env python3
"""Bridge the legacy HTTP/UI evidence into ASTRA's normalized criteria ledger."""

from __future__ import annotations

import argparse
import json
from pathlib import Path
from typing import Any

CORE = {
    "create_collection_request", "accept_assignment", "decline_assignment", "collect_and_seal_sample",
    "record_handling_checkpoint", "confirm_laboratory_receipt", "record_worker_location",
    "update_assignment_progress", "edit_active_plan", "ingest_presealed_sample", "authenticate_user",
    "create_user", "read_user", "select_active_role", "update_user_access", "create_location",
    "update_location", "create_connection", "set_location_status", "set_location_function_status",
    "set_connection_status", "create_sample_type", "update_sample_type", "configure_laboratory_acceptance",
    "configure_capacity", "set_worker_availability", "check_serviceability", "read_serviceability_snapshot",
    "frost_cooled_route_enforcement", "merge_journeys", "split_journey", "initiate_handoff", "confirm_handoff",
    "fairness_offer_distribution", "fairness_recency_disparity", "fairness_workload_disparity",
    "report_disruption", "reroute_journey", "cancel_journey",
}
SUPPORTING = {
    "stream_realtime_events", "search_audit_trail", "read_ui_bootstrap", "read_operations_state",
    "openapi_json_well_formed", "system_readiness", "named_sample_types_present_after_reset", "read_sample_state",
    "read_sample_lifecycle", "read_journey_plan", "list_role_work", "qualified_collector_exhaustion_exception",
    "record_laboratory_exception",
}
CONCURRENCY = {
    "concurrent_assignment_race_safety", "concurrent_plan_mutation_race_safety",
    "concurrent_capacity_exhaustion_race_safety",
}
IDEMPOTENCY = {
    "accept_assignment", "decline_assignment", "cancel_journey", "reroute_journey", "split_journey",
    "merge_journeys", "confirm_handoff", "collect_and_seal_sample", "record_handling_checkpoint",
    "initiate_handoff", "update_assignment_progress", "confirm_laboratory_receipt", "edit_active_plan",
    "report_disruption", "ingest_presealed_sample", "record_laboratory_exception", "create_collection_request",
}
DURABILITY_TOTAL = 32


def load(path: Path) -> dict[str, Any]:
    if not path.is_file():
        return {}
    value = json.loads(path.read_text(encoding="utf-8"))
    return value if isinstance(value, dict) else {}


def functional_credit(cap: dict[str, Any]) -> float:
    values = [cap.get("positiveOk")]
    if cap.get("negativeOk") is not None:
        values.append(cap["negativeOk"])
    values = [value for value in values if value is not None]
    return sum(bool(value) for value in values) / len(values) if values else 0.0


def backend_scores(reward: dict[str, Any]) -> tuple[float, float, float]:
    caps = reward.get("capabilities") or []
    core = [cap for cap in caps if cap.get("id") not in CONCURRENCY and (cap.get("id") in CORE or cap.get("id") not in SUPPORTING)]
    supporting = [cap for cap in caps if cap.get("id") not in CONCURRENCY and cap.get("id") in SUPPORTING]
    core_mean = sum(functional_credit(cap) for cap in core) / len(core) if core else 0.0
    supporting_mean = sum(functional_credit(cap) for cap in supporting) / len(supporting) if supporting else 0.0
    functional = 0.8 * core_mean + 0.2 * supporting_mean
    durability = sum(cap.get("durabilityOk") is True for cap in caps) / DURABILITY_TOTAL
    idempotency = sum(cap.get("id") in IDEMPOTENCY and cap.get("idempotencyOk") is True for cap in caps) / len(IDEMPOTENCY)
    concurrency = sum(float(cap.get("credit") or 0.0) for cap in caps if cap.get("id") in CONCURRENCY) / len(CONCURRENCY)
    reliability = (durability + idempotency + concurrency) / 3
    contract = float((reward.get("contract") or {}).get("score") or 0.0)
    return functional, reliability, contract


def outcome(score: Any, source: str) -> dict[str, Any]:
    if not isinstance(score, (int, float)) or isinstance(score, bool):
        return {"status": "blocked", "score": 0.0, "source": source}
    value = float(score)
    if not 0.0 <= value <= 1.0:
        raise SystemExit(f"{source} is outside [0, 1]: {value}")
    return {"status": "pass" if value == 1.0 else ("fail" if value == 0.0 else "partial"), "score": value, "source": source}


def artifact_score(candidate: Path, public_contract: Path) -> float:
    candidates = [candidate / "app-setup/openapi.json"]

    def structural(value: Any, path: tuple[str, ...] = ()) -> Any:
        """Ignore prose-only OpenAPI fields while preserving the API shape."""
        if isinstance(value, dict):
            return {
                key: structural(item, path + (key,))
                for key, item in value.items()
                if key not in {"description", "summary", "tags"}
                and not (path == ("info",) and key == "title")
            }
        if isinstance(value, list):
            return [structural(item, path) for item in value]
        return value

    try:
        actual = next(path for path in candidates if path.is_file())
        return float(
            structural(json.loads(actual.read_text(encoding="utf-8")))
            == structural(json.loads(public_contract.read_text(encoding="utf-8")))
        )
    except (StopIteration, OSError, json.JSONDecodeError):
        return 0.0


def ui_audit_score(ui_contract: Path, ui_reward: dict[str, Any]) -> float | None:
    if ui_reward.get("status") == "blocked":
        return None
    try:
        contract = load(ui_contract)
    except json.JSONDecodeError:
        return 0.0
    expected = {str(item.get("id")) for item in ui_reward.get("capabilities", []) if isinstance(item, dict)}
    declared = set(contract.get("scenarios") or [])
    return 1.0 if expected and expected <= declared else 0.0


def run(args: argparse.Namespace) -> int:
    backend = load(args.backend)
    ui = load(args.ui)
    # Keep the shared ASTRA harness test fixture compatible with the generic
    # component-shaped evidence used by the reference template, while real
    # pickup/delivery runs use the legacy capability reward below.
    if not hasattr(args, "candidate"):
        criteria = {
            "api-contract-artifact": outcome(backend.get("contractArtifactParityScore"), "backend:contractArtifactParityScore"),
            "runtime-contract": outcome(backend.get("runtimeContractConformanceScore"), "backend:runtimeContractConformanceScore"),
            "backend-behavior": outcome(backend.get("backendFunctionalScore"), "backend:backendFunctionalScore"),
            "ui-behavior": outcome(ui.get("overall"), "ui:overall"),
            "ui-contract-audit": outcome((ui.get("contract_audit") or {}).get("score"), "ui:contract_audit.score"),
            "reliability": outcome(backend.get("backendNonFunctionalScore"), "backend:backendNonFunctionalScore"),
        }
        report = {"schema_version": 1, "criteria": criteria, "evidence": {"backend": str(args.backend), "ui": str(args.ui)}}
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
        return 0
    functional, reliability, contract = backend_scores(backend)
    ui_score = ui.get("overall")
    criteria = {
        "api-contract-artifact": outcome(artifact_score(args.candidate, args.public_contract), "candidate:openapi-artifact"),
        "runtime-contract": outcome(contract, "backend:contract.score"),
        "backend-behavior": outcome(functional, "backend:functional"),
        "ui-behavior": outcome(ui_score, "ui:overall"),
        "ui-contract-audit": outcome(ui_audit_score(args.ui_contract, ui), "ui:contract-audit"),
        "reliability": outcome(reliability, "backend:reliability"),
    }
    report = {"schema_version": 1, "criteria": criteria, "evidence": {"backend": str(args.backend), "ui": str(args.ui)}}
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(report, indent=2))
    return 0


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--backend", type=Path, required=True)
    parser.add_argument("--ui", type=Path, required=True)
    parser.add_argument("--candidate", type=Path, required=True)
    parser.add_argument("--public-contract", type=Path, required=True)
    parser.add_argument("--ui-contract", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    raise SystemExit(run(parser.parse_args()))


if __name__ == "__main__":
    main()
