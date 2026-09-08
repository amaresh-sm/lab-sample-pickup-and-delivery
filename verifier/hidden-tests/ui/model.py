"""Grading vocabulary for the UI verifier -- a deliberate mirror of
``oracle.Verdict`` / ``oracle.Capability`` / ``oracle.CapabilityReport`` so the
UI sub-score reads the same way the API score does (positive + optional
negative/control, equal-weighted credit in [0,1]).
"""
from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Callable, Optional


@dataclass
class UiVerdict:
    """Result of exercising ONE dimension (positive or negative/control) of a
    UI capability against the rendered page."""

    ok: bool
    detail: str = ""
    evidence: dict[str, Any] = field(default_factory=dict)


@dataclass
class UiReport:
    positive: UiVerdict
    negative: Optional[UiVerdict] = None
    notes: str = ""

    def credit(self) -> float:
        dims = [self.positive]
        if self.negative is not None:
            dims.append(self.negative)
        weight = 1.0 / len(dims)
        return round(sum(weight for d in dims if d.ok), 4)


@dataclass
class UiCapability:
    id: str
    ac: str  # the ACCEPTANCE.md requirement id(s) this capability is derived from
    description: str
    run: Callable[["object"], UiReport]  # run(engine) -> UiReport
    priority: bool = False
