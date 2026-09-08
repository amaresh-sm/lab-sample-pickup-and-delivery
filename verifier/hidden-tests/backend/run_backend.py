#!/usr/bin/env python3
"""Compatibility entry point for the migrated pinned-contract backend suite.

The detailed capability implementation remains under ``legacy-engine`` so the
migration changes packaging and orchestration without rewriting the behavioral
oracle or weakening its HTTP-only boundary.
"""

from __future__ import annotations

import runpy
from pathlib import Path


if __name__ == "__main__":
    runpy.run_path(str(Path(__file__).resolve().parent / "legacy-engine/run.py"), run_name="__main__")
