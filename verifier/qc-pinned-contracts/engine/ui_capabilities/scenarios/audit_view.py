"""AC-70/AC-71/AC-73 -- an admin AUDIT-LOG view renders audit entries with
their accountability fields AND supports searching / filtering the trail.

Presentation-layer, framework-agnostic:
  * we seed a driven journey over HTTP (``seed_driven_journey`` generates many
    audited actions -- sign-ins, offers, collection, custody, ...), so the audit
    trail has real history;
  * POSITIVE (three parts, all required): the ADMIN navigates to the audit view
    and (1) >= 1 audit entry renders, (2) the audit field vocabulary is present
    (>= 3 of user/actor, role, action, outcome/result, time), and (3) SEARCH
    works -- a term drawn from a rendered entry, typed into the search/filter
    field and submitted, FILTERS the list (entries do not increase, >= 1 remains,
    and the searched term is still present);
  * CONTROL: a plain requester does NOT get the global audit view.

The field vocabulary is a DOMAIN vocabulary (the accountability facts of an
audit record: who/as-what-role/did-what/on-what/when/with-what-outcome) rather
than golden's exact copy, so a differently-worded correct audit view still
scores. The search TERM is derived from whatever the candidate actually rendered
(the longest word of a visible entry), never a golden-specific string, so the
filter assertion cannot be golden-biased. Patterns carry a LEADING word boundary
only where used in ``filter(has_text=...)`` (Playwright silently fails on a regex
ending in ``\\b``).
"""
from __future__ import annotations

import re

from model import UiCapability, UiReport, UiVerdict
from seed import seed_driven_journey
from tracking import count, settle

_AUDIT_NAV = (r"\baudit\b", r"\blog\b", r"\bactivity\b", r"\bhistory\b")
_SEARCH_FIELD_NAME = re.compile(r"search|filter|query", re.I)
_SEARCH_SUBMIT_NAME = re.compile(r"search|filter|apply|find|audit", re.I)

# Audit field vocabulary (accountability facts). Each is one category; >= 3
# distinct categories must be present.
_FIELD_VOCAB: dict[str, "re.Pattern[str]"] = {
    "actor": re.compile(r"\buser\b|\bactor", re.I),
    "role": re.compile(r"\brole", re.I),
    "action": re.compile(r"\baction", re.I),
    "outcome": re.compile(r"\boutcome|\bresult|\bapplied\b|\brefused\b|\bsuccess", re.I),
    "time": re.compile(r"\btime|\bwhen\b|\boccurred|\bat\b", re.I),
}
_WORD = re.compile(r"[A-Za-z]{4,}")


def _audit_region(page):
    region = page.get_by_role("region", name=re.compile(r"audit|log|activity", re.I))
    if count(region) > 0:
        return region.first
    return page


def _entries(scope):
    items = scope.get_by_role("listitem")
    if count(items) > 0:
        return items
    # An audit trail is legitimately a data table. Keep only body/data rows
    # (rows containing cells), excluding a column-header-only row.
    rows = scope.get_by_role("row")
    try:
        return rows.filter(has=scope.get_by_role("cell"))
    except Exception:
        return rows


def _search_field(page):
    box = page.get_by_role("searchbox")
    if count(box) > 0:
        return box.first
    box = page.get_by_role("textbox", name=_SEARCH_FIELD_NAME)
    if count(box) > 0:
        return box.first
    # Placeholder text is visible field vocabulary even when the candidate
    # omitted an explicit accessible-name relationship.
    box = page.get_by_placeholder(_SEARCH_FIELD_NAME)
    if count(box) > 0:
        return box.first
    return None


def _submit_search(page, field) -> dict:
    evidence: dict = {}
    btn = page.get_by_role("button", name=_SEARCH_SUBMIT_NAME)
    if count(btn) > 0:
        try:
            with page.expect_response(
                lambda response: "search-audit-trail" in response.url,
                timeout=5000,
            ) as response_info:
                btn.first.click()
            response = response_info.value
            evidence["status"] = response.status
            try:
                payload = response.json()
                data = payload.get("data") if isinstance(payload, dict) else None
                if isinstance(data, dict):
                    evidence["total"] = data.get("total")
                    evidence["returned"] = data.get("returned")
            except Exception:
                pass
            settle(page)
            return evidence
        except Exception:
            # The click already happened before an expect_response timeout;
            # never click a write-capable look-alike control a second time.
            settle(page)
            return evidence
    try:
        field.press("Enter")
        settle(page)
    except Exception:
        pass
    return evidence


def _entry_count(scope) -> int:
    try:
        return _entries(scope).count()
    except Exception:
        return 0


def _discriminating_term(scope, baseline: int) -> str:
    """Choose a rendered word that occurs in some, but not all, audit rows.

    This lets the post-search assertion prove the list actually narrows. A
    ubiquitous word from the first row (for example the active role appearing
    on every seeded action) cannot demonstrate filtering.
    """
    frequencies: dict[str, int] = {}
    entries = _entries(scope)
    for idx in range(min(baseline, 50)):
        try:
            words = {word.lower() for word in _WORD.findall(entries.nth(idx).inner_text())}
        except Exception:
            continue
        for word in words:
            frequencies[word] = frequencies.get(word, 0) + 1
    candidates = [word for word, freq in frequencies.items() if 0 < freq < baseline]
    return max(candidates, key=lambda word: (len(word), -frequencies[word]), default="")


def _run(engine) -> UiReport:
    seed = seed_driven_journey(
        engine.base_url, engine.contract_path, engine.admin_user, engine.admin_pass
    )
    if not seed.ok:
        return UiReport(positive=UiVerdict(False, seed.detail), notes="seed failure")

    # -------- POSITIVE: admin audit view renders entries + fields + search -----
    ctx, _ = engine.new_context()
    try:
        page = ctx.new_page()
        if not engine.login(page, engine.admin_user, engine.admin_pass):
            positive = UiVerdict(False, "admin could not sign in through the public login form")
        else:
            for nav in _AUDIT_NAV:
                if engine.navigate(page, nav, timeout_ms=2500):
                    settle(page)
                    break
            region = _audit_region(page)
            field = _search_field(page)

            # Some audit views render nothing until a search is run; a broad
            # (empty) submit populates the trail. Harmless where the list is
            # already shown.
            if field is not None:
                try:
                    field.fill("")
                except Exception:
                    pass
                _submit_search(page, field)
                region = _audit_region(page)

            try:
                _entries(region).first.wait_for(state="visible", timeout=6000)
            except Exception:
                pass
            baseline = _entry_count(region)
            entries_ok = baseline >= 1

            vocab = [name for name, pat in _FIELD_VOCAB.items()
                     if count(region.get_by_text(pat)) > 0]
            vocab_ok = len(vocab) >= 3

            # SEARCH: derive a discriminating term from rendered entries and
            # confirm the list narrows while the term stays present.
            search_ok = False
            term = ""
            after = baseline
            if entries_ok and field is not None:
                term = _discriminating_term(region, baseline)
                if term:
                    try:
                        field.fill(term)
                    except Exception:
                        pass
                    response_evidence = _submit_search(page, field)
                    region = _audit_region(page)
                    try:
                        _entries(region).first.wait_for(state="visible", timeout=4000)
                    except Exception:
                        pass
                    after = _entry_count(region)
                    term_present = count(region.get_by_text(re.compile(re.escape(term), re.I))) > 0
                    search_ok = after < baseline and after >= 1 and term_present
                else:
                    response_evidence = {}
            else:
                response_evidence = {}

            positive = UiVerdict(
                ok=entries_ok and vocab_ok and search_ok,
                detail=(
                    f"audit entries={baseline} (>=1:{entries_ok}); field-vocab={sorted(vocab)} "
                    f"(>=3:{vocab_ok}); search term={term!r} filtered {baseline}->{after} "
                    f"response={response_evidence} (ok:{search_ok})"
                ),
                evidence={
                    "baseline": baseline,
                    "vocab": sorted(vocab),
                    "term": term,
                    "after": after,
                    "response": response_evidence,
                },
            )
    finally:
        ctx.close()

    # -------- CONTROL: a plain requester does NOT get the global audit view ----
    ctx2, _ = engine.new_context()
    try:
        page = ctx2.new_page()
        if not engine.login(page, seed.owner_username, seed.owner_password):
            negative = UiVerdict(False, "requester could not sign in; control inconclusive")
        else:
            reached = False
            for nav in _AUDIT_NAV:
                if engine.navigate(page, nav, timeout_ms=1500):
                    settle(page)
                    reached = True
                    break
            region = _audit_region(page)
            field = _search_field(page)
            if field is not None:
                try:
                    field.fill("")
                except Exception:
                    pass
                _submit_search(page, field)
                region = _audit_region(page)
            leaked = reached and field is not None and _entry_count(region) >= 1
            negative = UiVerdict(
                ok=not leaked,
                detail=(
                    "requester can read the global audit trail (LEAK)"
                    if leaked
                    else "requester is not shown the global audit trail (correctly withheld)"
                ),
            )
    finally:
        ctx2.close()

    return UiReport(positive=positive, negative=negative, notes="AC-70/AC-71/AC-73 audit view renders + searches")


CAPABILITY = UiCapability(
    id="audit_view_renders_and_searches",
    ac="AC-70/AC-71/AC-73",
    description="Admin audit view renders audit entries with their fields and supports searching/filtering.",
    run=_run,
    priority=True,
)
