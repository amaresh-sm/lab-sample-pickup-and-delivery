"""READ-ONLY diagnostic: drive a candidate's live UI and dump ground truth
about why the map / freshness scenarios did or did not find what they expect.
Changes nothing; scores nothing. Delete after use."""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

UI_DIR = Path(__file__).resolve().parent
ENGINE_DIR = UI_DIR.parent
SCEN_DIR = UI_DIR / "scenarios"
for _p in (str(ENGINE_DIR), str(UI_DIR), str(SCEN_DIR)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

from playwright.sync_api import sync_playwright  # noqa: E402
from driver import UiEngine  # noqa: E402
from helpers import canvas_is_nonblank  # noqa: E402

MAP_ASSET = re.compile(r"(map[-_/]?asset|tile|pmtiles|\.pbf|glyph|sprite|style\.json|/map)", re.I)


def names(page, role):
    out = []
    try:
        loc = page.get_by_role(role)
        n = loc.count()
        for i in range(min(n, 40)):
            try:
                nm = loc.nth(i).get_attribute("aria-label") or loc.nth(i).inner_text(timeout=800)
            except Exception:
                nm = "?"
            nm = " ".join((nm or "").split())[:60]
            if nm:
                out.append(nm)
    except Exception as e:
        out.append(f"<err {e!r}>")
    return out


def dump_page(tag, page, gate):
    print(f"\n===== [{tag}] url={page.url}")
    rendered, cv = canvas_is_nonblank(page)
    print(f"  canvas: rendered={rendered} {cv}")
    map_assets = [u for u in gate.all_requests if MAP_ASSET.search(u)]
    print(f"  local map-assets fetched: {len(map_assets)} {map_assets[:6]}")
    print(f"  external egress attempts: {len(gate.external_attempts)} {gate.external_attempts[:6]}")
    print(f"  LINKS   : {names(page, 'link')}")
    print(f"  BUTTONS : {names(page, 'button')}")
    print(f"  TEXTBOX : {names(page, 'textbox')}")
    print(f"  HEADINGS: {names(page, 'heading')}")
    print(f"  TABS    : {names(page, 'tab')}")
    print(f"  MENUITEM: {names(page, 'menuitem')}")
    for r in ("navigation", "list"):
        try:
            print(f"  {r} count: {page.get_by_role(r).count()}")
        except Exception:
            pass
    try:
        txt = " ".join(page.locator("body").inner_text(timeout=2000).split())
        print(f"  BODY[:500]: {txt[:500]}")
    except Exception:
        pass
    try:
        body = page.locator("body").inner_text(timeout=2000)
    except Exception:
        body = ""
    hits = sorted(set(re.findall(r"\b(fresh\w*|stale\w*|current|updated|last updat\w*)\b", body, re.I)))
    print(f"  freshness-ish words on page: {hits}")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--frontend-url", required=True)
    ap.add_argument("--base-url", required=True)
    ap.add_argument("--contract", required=True)
    ap.add_argument("--admin-user", default="admin")
    ap.add_argument("--admin-pass", default="admin123")
    a = ap.parse_args()

    with sync_playwright() as pw:
        eng = UiEngine(playwright=pw, frontend_url=a.frontend_url, base_url=a.base_url,
                       contract_path=a.contract, admin_user=a.admin_user,
                       admin_pass=a.admin_pass, timeout_ms=8000)
        try:
            ctx, gate = eng.new_context()
            gate.watch(ctx.new_page().context.pages[0]) if hasattr(gate, "watch") else None
            page = ctx.pages[0] if ctx.pages else ctx.new_page()
            if hasattr(gate, "watch"):
                gate.watch(page)
            ok = eng.login(page, a.admin_user, a.admin_pass)
            print(f"LOGIN admin = {ok}")
            dump_page("after-login (landing)", page, gate)

            # Sol gates the app behind a workspace-entry button; click it first.
            if eng.navigate(page, r"open .*workspace|admin workspace|enter|continue|open admin"):
                try:
                    page.wait_for_load_state("networkidle", timeout=6000)
                except Exception:
                    pass
                dump_page("after clicking 'Open admin workspace'", page, gate)

            for rgx in ("live operations|operations|map|network", "dashboard|overview|home",
                        "requests?|samples?|track"):
                # fresh context each nav so we see where the map actually lives
                if eng.navigate(page, rgx):
                    try:
                        page.wait_for_load_state("networkidle", timeout=5000)
                    except Exception:
                        pass
                    dump_page(f"after nav '{rgx}'", page, gate)
                else:
                    print(f"\n===== nav '{rgx}' : NO matching link/button")
            ctx.close()
        finally:
            eng.close()


if __name__ == "__main__":
    main()
