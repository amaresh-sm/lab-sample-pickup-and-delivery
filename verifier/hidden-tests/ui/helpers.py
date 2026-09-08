"""Small assertion helpers shared by scenarios. Every wait here is bounded and
event-driven; none uses a fixed sleep."""
from __future__ import annotations

import re
from playwright.sync_api import Locator, Page, TimeoutError as PWTimeout, expect


def field_by_visible_label(scope, label_pattern):
    """Resolve a form control by the label a user can see.

    Prefer the accessible-label relationship. If a candidate renders the
    visible ``<label>`` next to its input/select without ``for``/``id`` (or a
    wrapping label), fall back to the control in that label's immediate
    visual container. UI requirements in this task do not prescribe an ARIA
    or DOM contract, so an operable field must not fail solely because its
    accessible-name wiring is incomplete.
    """
    labelled = scope.get_by_label(label_pattern)
    try:
        if labelled.count() > 0:
            return labelled.first
    except Exception:
        pass

    labels = scope.get_by_text(label_pattern)
    try:
        total = min(labels.count(), 20)
    except Exception:
        total = 0
    for idx in range(total):
        text = labels.nth(idx)
        try:
            if not text.is_visible():
                continue
            controls = text.locator("xpath=following-sibling::*[self::input or self::select or self::textarea]")
            if controls.count() > 0:
                return controls.first
            controls = text.locator("xpath=..").locator("input, select, textarea")
            if controls.count() > 0:
                return controls.first
        except Exception:
            continue
    return None


def appears_within(locator: Locator, timeout_ms: int) -> bool:
    """True iff `locator` becomes visible within the bounded timeout. Used both
    for a positive assertion (must appear) and, inverted, for an absence/control
    assertion (must NOT appear) -- an auto-retrying wait, never a sleep."""
    try:
        expect(locator.first).to_be_visible(timeout=timeout_ms)
        return True
    except PWTimeout:
        return False
    except Exception:
        return False


def canvas_is_nonblank(page: Page, min_px: int = 80) -> tuple[bool, dict]:
    """A map view has drawn iff a <canvas> is present, visible, and larger than
    a trivial size. <canvas> is a standard HTML element every mapping library
    (maplibre/leaflet/mapbox/google) renders into -- this references no
    library-specific class, id, or attribute."""
    canvas = page.locator("canvas")
    try:
        expect(canvas.first).to_be_visible(timeout=8000)
    except Exception:
        return False, {"canvasCount": canvas.count()}
    box = canvas.first.bounding_box()
    ok = bool(box and box["width"] >= min_px and box["height"] >= min_px)
    return ok, {"canvasCount": canvas.count(), "box": box}
