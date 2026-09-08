"""authenticate_user, select_active_role, create_user, update_user_access, read_user.

Ground truth mined from golden-mongo's roles_integration_test.go and admin.go:
  - error envelope is flat: {"error": "<msg>", "code": "<code>"}
  - selecting a role you don't hold -> 403 role_not_held (AC-6 companion check)
  - switching the active role never adds/removes roles on the account (AC-7)
  - the SELECTED role governs authorization, not the union of held roles (AC-6):
    an administrator+requester account acting as requester gets 403 on
    administrator-only ops (no blanket admin bypass by held roles).
"""
from __future__ import annotations

from oracle import Capability, CapabilityReport, Verdict


def _run_authenticate_user(engine) -> CapabilityReport:
    world = engine.new_world("auth")
    created_user = world.user("plain", ["requester"])
    username = world.users["plain"]["username"]

    status, payload = engine.admin.raw("authenticate_user", {"username": username, "password": "Engine-Pass-w0rd1!"})
    data = engine.admin.data(status, payload)
    # VB2.6: "requester" in roles is trivially true for ANY fixed/canned role
    # list a stateless login handler might return (e.g. the full role
    # universe), regardless of what the account was actually granted -- so
    # require the EXACT granted set, and require the returned userId to be
    # the SAME account create_user just minted for this exact username (a
    # login that fabricates an unrelated identity independent of who asked
    # cannot pass this).
    positive = Verdict(
        ok=status == 200
        and bool(data.get("sessionToken"))
        and data.get("userId") == created_user.get("userId")
        and sorted(data.get("roles", [])) == ["requester"],
        detail=f"status={status} userId={data.get('userId')} (expected {created_user.get('userId')}) roles={data.get('roles')} activeRole={data.get('activeRole')!r}",
    )

    bad_status, bad_payload = engine.admin.raw("authenticate_user", {"username": username, "password": "wrong-password"})
    negative = Verdict(
        ok=bad_status == 401 and engine.admin.code(bad_payload) == "invalid_credentials",
        detail=f"status={bad_status} code={engine.admin.code(bad_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


def _run_select_active_role(engine) -> CapabilityReport:
    world = engine.new_world("role")
    world.user("multi", ["dispatcher", "requester"])
    sess = world.session("multi")

    before_status, before_payload = sess.raw("read_user", path_param=sess.user_id)
    before_roles = sess.data(before_status, before_payload).get("roles", [])

    # AC-6: selecting "requester" must NOT let a dispatcher-only op through.
    st1, _ = sess.select_role("requester")
    st_denied, payload_denied = sess.raw("read_operations_state", {"filters": {}})
    denied_as_requester = st_denied == 403 and sess.code(payload_denied) == "forbidden"

    # Selecting "dispatcher" must permit it.
    st2, _ = sess.select_role("dispatcher")
    st_allowed, _ = sess.raw("read_operations_state", {"filters": {}})
    allowed_as_dispatcher = st_allowed == 200

    # AC-7: switching role must not change the account's roles.
    after_status, after_payload = sess.raw("read_user", path_param=sess.user_id)
    after_roles = sess.data(after_status, after_payload).get("roles", [])

    positive = Verdict(
        ok=st1 == 200 and st2 == 200 and denied_as_requester and allowed_as_dispatcher and sorted(before_roles) == sorted(after_roles),
        detail=(
            f"select requester->{st1} select dispatcher->{st2} "
            f"denied_as_requester={denied_as_requester} allowed_as_dispatcher={allowed_as_dispatcher} "
            f"roles unchanged={sorted(before_roles) == sorted(after_roles)}"
        ),
        evidence={"before_roles": before_roles, "after_roles": after_roles},
    )

    bad_status, bad_payload = sess.raw("select_active_role", {"role": "laboratory_team"})
    negative = Verdict(
        ok=bad_status == 403 and sess.code(bad_payload) == "role_not_held",
        detail=f"status={bad_status} code={sess.code(bad_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


def _run_create_user(engine) -> CapabilityReport:
    world = engine.new_world("cu")
    created = engine.admin.call(
        "create_user",
        {
            "username": world.key("newuser"),
            "initialPassword": "Engine-Pass-w0rd1!",
            "email": "cu@engine.test",
            "fullName": "Capability CreateUser",
            "phone": "555-0101",
            "roles": ["requester"],
            "qualifications": [],
            "active": True,
        },
    )
    # VB2.6: read_user's direct-read echoes the requested path-parameter id
    # back verbatim on ANY id (see anchors/hollow/server.py's h_read_user) --
    # matching on that same id is not independent evidence of persistence.
    # Instead, require an INDEPENDENT read (a different call than the one
    # that wrote it) to come back with the username we set at creation, a
    # value the read handler cannot derive from the id alone without having
    # actually stored it.
    read_status, read_payload = engine.admin.raw("read_user", path_param=created["userId"])
    read_data = engine.admin.data(read_status, read_payload)
    username_confirmed = read_status == 200 and read_data.get("username") == created.get("username")
    positive = Verdict(
        ok=username_confirmed and created.get("active") is True,
        detail=f"independent read_user username={read_data.get('username')!r} (expected {created.get('username')!r})",
        evidence={"userId": created.get("userId")},
    )

    dupe_status, dupe_payload = engine.admin.raw(
        "create_user",
        {
            "username": created["username"],
            "initialPassword": "Engine-Pass-w0rd1!",
            "roles": ["requester"],
            "active": True,
        },
    )
    negative = Verdict(
        ok=dupe_status == 409 and engine.admin.code(dupe_payload) == "username_exists",
        detail=f"status={dupe_status} code={engine.admin.code(dupe_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


def _run_update_user_access(engine) -> CapabilityReport:
    world = engine.new_world("uua")
    world.user("target", ["requester"])
    target_id = world.user_id("target")

    updated = engine.admin.call(
        "update_user_access",
        {"userId": target_id, "roles": ["dispatcher"], "qualifications": [], "active": True},
    )
    # VB2.6: sentinel="dispatcher" is a role NAME, not an id -- but it is
    # also a literal member of any fixed/canned role-universe list a
    # stateless read_user might always return (see anchors/hollow/server.py's
    # h_read_user, whose "roles" is hardcoded to the full role list), so a
    # bare substring match on "dispatcher" is satisfied whether or not this
    # account was ever actually updated. Require an INDEPENDENT read_user
    # call to show EXACTLY the new role set -- not a superset that happens to
    # contain "dispatcher" -- proving the original "requester" role is
    # genuinely gone, not merely appended to.
    read_status, read_payload = engine.admin.raw("read_user", path_param=target_id)
    read_data = engine.admin.data(read_status, read_payload)
    roles_confirmed = read_status == 200 and sorted(read_data.get("roles", [])) == ["dispatcher"]
    positive = Verdict(
        ok=roles_confirmed and updated.get("roles") == ["dispatcher"],
        detail=f"independent read_user roles={read_data.get('roles')} (expected exactly ['dispatcher']); update response roles={updated.get('roles')}",
    )

    world.user("nonadmin", ["requester"])
    other_sess = world.session("nonadmin")
    bad_status, bad_payload = other_sess.raw(
        "update_user_access", {"userId": target_id, "roles": ["administrator"], "active": True}
    )
    negative = Verdict(
        ok=bad_status == 403 and other_sess.code(bad_payload) == "forbidden",
        detail=f"status={bad_status} code={other_sess.code(bad_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


def _run_read_user(engine) -> CapabilityReport:
    world = engine.new_world("ru")
    world.user("owner", ["requester"])
    world.user("other", ["requester"])
    owner_id = world.user_id("owner")
    owner_username = world.users["owner"]["username"]

    status, payload = engine.admin.raw("read_user", path_param=owner_id)
    data = engine.admin.data(status, payload)
    # VB2.6: userId==owner_id is a trivial path-param echo (any GET-by-id
    # handler returns the id it was asked for, real or fabricated -- see
    # anchors/hollow/server.py's h_read_user). username is NOT derivable
    # from the path parameter alone; requiring it match what create_user
    # actually set is the genuine persistence signal.
    positive = Verdict(
        ok=status == 200 and data.get("userId") == owner_id and data.get("username") == owner_username and "roles" in data,
        detail=f"status={status} userId={data.get('userId')} username={data.get('username')!r} (expected {owner_username!r})",
    )

    other_sess = world.session("other")
    bad_status, bad_payload = other_sess.raw("read_user", path_param=owner_id)
    negative = Verdict(
        ok=bad_status in (403, 404),
        detail=f"cross-account read_user -> status={bad_status} code={other_sess.code(bad_payload)!r}",
    )
    return CapabilityReport(positive=positive, negative=negative)


# --------------------------------------------------------------- durability
# DUR3: `authenticate_user`/`select_active_role` are session/ephemeral state
# (DURABILITY.md's N/A list), not wired here. create_user/update_user_access
# both already go through engine.admin, and both have a real read_user
# endpoint to check against -- no observe()/audit-trail fallback needed.


def _durability_write_create_user(engine) -> dict:
    world = engine.new_world("cudur")
    created = engine.admin.call(
        "create_user",
        {
            "username": world.key("newuserdur"),
            "initialPassword": "Engine-Pass-w0rd1!",
            "email": "cudur@engine.test",
            "fullName": "Durability CreateUser",
            "phone": "555-0102",
            "roles": ["requester"],
            "qualifications": [],
            "active": True,
        },
    )
    return {"user_id": created.get("userId"), "username": created.get("username")}


def _durability_verify_create_user(engine, sentinel: dict) -> Verdict:
    user_id, username = sentinel.get("user_id"), sentinel.get("username")
    if not (user_id and username):
        return Verdict(ok=False, detail="no user_id/username recorded at write time")
    status, payload = engine.admin.raw("read_user", path_param=user_id)
    data = engine.admin.data(status, payload)
    ok = status == 200 and data.get("username") == username
    return Verdict(ok=ok, detail=f"post-restart read_user status={status} username={data.get('username')!r} (expected {username!r})")


def _durability_write_update_user_access(engine) -> dict:
    world = engine.new_world("uuadur")
    world.user("targetdur", ["requester"])
    target_id = world.user_id("targetdur")
    engine.admin.call("update_user_access", {"userId": target_id, "roles": ["dispatcher"], "qualifications": [], "active": True})
    return {"user_id": target_id}


def _durability_verify_update_user_access(engine, sentinel: dict) -> Verdict:
    user_id = sentinel.get("user_id")
    if not user_id:
        return Verdict(ok=False, detail="no user_id recorded at write time")
    status, payload = engine.admin.raw("read_user", path_param=user_id)
    data = engine.admin.data(status, payload)
    ok = status == 200 and sorted(data.get("roles", []) or []) == ["dispatcher"]
    return Verdict(ok=ok, detail=f"post-restart read_user status={status} roles={data.get('roles')} (expected exactly ['dispatcher'])")


CAPABILITIES = [
    Capability("authenticate_user", "write", "Login issues a session bound to the account's roles.", _run_authenticate_user),
    Capability(
        "select_active_role",
        "write",
        "Selected role (not the union of held roles) governs authorization; switching never mutates held roles.",
        _run_select_active_role,
        priority=True,
    ),
    Capability(
        "create_user", "write", "Administrator provisions an account with roles/qualifications.", _run_create_user,
        durability_write=_durability_write_create_user, durability_verify=_durability_verify_create_user,
    ),
    Capability(
        "update_user_access", "write", "Administrator changes an existing account's roles/active flag.", _run_update_user_access,
        durability_write=_durability_write_update_user_access, durability_verify=_durability_verify_update_user_access,
    ),
    Capability("read_user", "read", "Read one user's profile; RBAC restricts cross-account reads.", _run_read_user),
]
