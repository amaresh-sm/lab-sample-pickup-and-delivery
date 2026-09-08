"""Phase-1 deterministic UI / behavioural verifier for the LabLink app.

This package grades PRESENTATION-LAYER behaviour that the HTTP engine
(``engine/capabilities/``) cannot observe over the API: whether a role that
owns a sample actually SEES its freshness in the rendered page, and whether
the map draws from local tiles only (no external network egress). It never
asserts on CSS classes, element ids, ``data-testid`` attributes, DOM paths, or
any framework internal -- only on visible text, accessible role/name, rendered
data values, and the network the browser actually makes. A correct app in any
framework (golden is Vue; a candidate may be React) must pass unchanged.

It is deliberately a SEPARATE package from ``capabilities/`` with its own
scorer (``run_ui.py``) and its own reward file (``ui-reward.json``) so the UI
sub-score stays legible next to the API score rather than being averaged into
it. It reuses the HTTP engine's ``client.World`` to seed known state over HTTP
BEFORE opening any page -- same determinism discipline as the API engine.
"""
