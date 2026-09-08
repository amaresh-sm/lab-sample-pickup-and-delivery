"""app-setup/openapi.json well-formedness: is the CANDIDATE's own generated
document even a valid JSON object that conforms to the OpenAPI 3.0/3.1 spec
structure -- independent of, and prior to, whether its CONTENT matches the
live app or the pinned contract.

instruction.md requires: "`openapi.json` must contain a valid JSON object
describing the application API using OpenAPI 3.0 or 3.1. Do not use YAML
syntax in this file." Nothing in this engine's other 45 operationId-keyed
capabilities (see engine/capabilities/*.py, one file per pinned-contract
domain) touches a candidate's filesystem at all -- they speak ONLY HTTP,
by design (see engine/README.md). This is the one deliberate exception:
grading whether a STATIC DOCUMENT is even well-formed cannot be done by
calling an HTTP operation, because a malformed/absent `openapi.json` is
exactly the failure mode content-matching checks (AC-76 "files present",
AC-77 "spec <-> route-registry cross-check", AC-100 "generated openapi.json
matches the pinned openapi.contract.json") never isolate on their own -- a
content-diff against a document that failed to parse is meaningless, and a
naive content-matcher that never validated JSON-ness/spec-shape first can
be fooled by a document that happens to string-match in the right places
while being invalid, YAML-tainted JSON or missing required OpenAPI fields.
This capability is deliberately narrow and runs BEFORE any such
content-matching check would: JSON-parse, then structural spec-shape only.

Path resolution: this engine has no existing "read a candidate's own file"
capability to copy from (grep engine/capabilities/*.py for `Path(`/`open(` --
there is none prior to this file). The `WORKSPACE` env var is this REPO's
own existing convention for "where does the candidate app live" (see
verifier/capability/capability_grader.py: `WORKSPACE = os.environ.get(
"WORKSPACE", "/workspace")`, `json.load(open(WORKSPACE +
"/app-setup/openapi.json"))`, and verifier/capability/run.py's identical
`workspace / "verification" / "openapi.json"`) -- reused here verbatim
rather than inventing a new env var or CLI flag.
"""
from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any

from oracle import Capability, CapabilityReport, Verdict

_OPENAPI_VERSION_PREFIXES = ("3.0.", "3.1.")


def _candidate_workspace() -> Path:
    """Same env var / default this repo already uses elsewhere (see module
    docstring) for "where the candidate app under test lives"."""
    return Path(os.environ.get("WORKSPACE", "/workspace"))


def _candidate_openapi_path() -> Path:
    return _candidate_workspace() / "app-setup" / "openapi.json"


def _partial_structural_openapi_check(spec: dict[str, Any]) -> list[str]:
    """PARTIAL fallback structural check -- top-level required fields/types
    only (openapi version string, info object, paths object). Used ONLY when
    `openapi_spec_validator` is not importable in the running environment
    (see `_run_openapi_json_well_formed` below). This is NOT full OpenAPI 3.0/
    3.1 spec conformance -- it does not validate nested schema objects, $ref
    resolution, parameter/response shapes, or anything `openapi-spec-validator`
    itself would catch. It exists so this capability still gives a real,
    narrowly-scoped signal instead of silently no-opping when the real
    validator library happens to be absent from this runtime.
    """
    problems: list[str] = []

    version = spec.get("openapi")
    if not isinstance(version, str) or not version.startswith(_OPENAPI_VERSION_PREFIXES):
        problems.append(f"'openapi' must be a 3.0.x or 3.1.x version string, got {version!r}")

    info = spec.get("info")
    if not isinstance(info, dict):
        problems.append(f"'info' must be an object, got {type(info).__name__}")
    else:
        if not isinstance(info.get("title"), str) or not info.get("title"):
            problems.append("'info.title' must be a non-empty string")
        if not isinstance(info.get("version"), str) or not info.get("version"):
            problems.append("'info.version' must be a non-empty string")

    paths = spec.get("paths")
    if not isinstance(paths, dict):
        problems.append(f"'paths' must be an object, got {type(paths).__name__}")

    return problems


def _run_openapi_json_well_formed(engine) -> CapabilityReport:  # noqa: ARG001 -- engine unused: pure filesystem check
    path = _candidate_openapi_path()

    if not path.exists():
        return CapabilityReport(
            positive=Verdict(ok=False, detail=f"{path} does not exist (instruction.md requires app-setup/openapi.json)")
        )

    try:
        raw = path.read_text(encoding="utf-8")
    except OSError as exc:
        return CapabilityReport(positive=Verdict(ok=False, detail=f"could not read {path}: {exc.__class__.__name__}: {exc}"))

    try:
        spec = json.loads(raw)
    except json.JSONDecodeError as exc:
        return CapabilityReport(
            positive=Verdict(
                ok=False,
                detail=(
                    f"{path} is not valid JSON (line {exc.lineno}, column {exc.colno}: {exc.msg}). "
                    "instruction.md requires openapi.json to be a valid JSON object -- no YAML syntax."
                ),
            )
        )

    if not isinstance(spec, dict):
        return CapabilityReport(
            positive=Verdict(ok=False, detail=f"{path} parsed to a {type(spec).__name__}, not a JSON object")
        )

    try:
        from openapi_spec_validator import validate as _validate_openapi_spec  # type: ignore
    except ImportError:
        _validate_openapi_spec = None

    if _validate_openapi_spec is not None:
        # Real validator available -- use it rather than hand-rolling spec
        # conformance. It still requires the top-level `openapi` field to be
        # a supported version string itself, but be explicit about the
        # 3.0/3.1 requirement (instruction.md's own wording) in the failure
        # message rather than relying solely on the library's own wording.
        version = spec.get("openapi")
        if not isinstance(version, str) or not version.startswith(_OPENAPI_VERSION_PREFIXES):
            return CapabilityReport(
                positive=Verdict(ok=False, detail=f"'openapi' must be a 3.0.x or 3.1.x version string, got {version!r}")
            )
        try:
            _validate_openapi_spec(spec)
        except Exception as exc:  # library raises several version-specific exception types
            return CapabilityReport(positive=Verdict(ok=False, detail=f"openapi-spec-validator rejected {path}: {exc}"))
        detail = f"{path} is valid JSON and passes openapi-spec-validator's OpenAPI {version} structural validation"
    else:
        # PARTIAL CHECK pending a real validator library in this runtime --
        # see `_partial_structural_openapi_check`'s docstring above.
        problems = _partial_structural_openapi_check(spec)
        if problems:
            return CapabilityReport(
                positive=Verdict(
                    ok=False,
                    detail=(
                        f"{path} failed the partial structural OpenAPI check "
                        f"(openapi-spec-validator not importable in this runtime): " + "; ".join(problems)
                    ),
                )
            )
        detail = (
            f"{path} is valid JSON and passes a PARTIAL structural OpenAPI 3.0/3.1 check "
            "(openapi-spec-validator not importable in this runtime -- only top-level "
            "openapi/info/paths fields and types were checked, not full spec conformance)"
        )

    return CapabilityReport(positive=Verdict(ok=True, detail=detail))


CAPABILITIES = [
    Capability(
        "openapi_json_well_formed",
        "artifact",
        "instruction.md: the candidate's app-setup/openapi.json must be a valid JSON object "
        "conforming to the OpenAPI 3.0/3.1 spec structure (required top-level openapi/info/paths "
        "fields, correct types) -- independent of, and checked BEFORE, whether its CONTENT matches "
        "the live app or the pinned openapi.contract.json (see AC-76/AC-77/AC-100, which cover "
        "content-matching separately). Not one of the 45 pinned-contract HTTP operations -- this is "
        "the one capability in this engine that reads a candidate's own file rather than calling its "
        "API (see module docstring for why, and for the WORKSPACE path-resolution mechanism reused "
        "here from verifier/capability/capability_grader.py).",
        _run_openapi_json_well_formed,
    ),
]
