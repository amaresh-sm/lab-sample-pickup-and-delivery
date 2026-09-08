"""AC-75: durable state survives an ordinary application restart.

Write over HTTP, restart the application PROCESS ONLY -- `verification/start.sh`
is run again while MongoDB keeps running and `verification/reset.sh` is never
touched after the write -- then read the same data back over HTTP.

This exercises the bespoke chunked-snapshot + compare-and-swap head protocol the
store commits with, which is exactly where an "it was only in memory" bug would
hide: every write below lands in a different part of one logical commit
(collections, users, network, request, sample, journey, assignments, custody,
audit, realtime outbox, session, idempotency record).
"""
from __future__ import annotations

import json
import os
import signal
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from api_client import Client, World, WORKSPACE_ROOT, rfc3339  # noqa: E402

# Values that are recomputed from the wall clock on every read, so they are
# expected to move between two reads and are compared separately.
CLOCK_DERIVED_KEYS = {
    "locationStale",
    "location_stale",
    "lastUpdateAgeSeconds",
    "locationAgeSeconds",
    "generatedAt",
}


def free_port() -> int:
    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return int(probe.getsockname()[1])


def strip_clock_derived(value):
    if isinstance(value, dict):
        return {
            key: strip_clock_derived(item)
            for key, item in value.items()
            if key not in CLOCK_DERIVED_KEYS
        }
    if isinstance(value, list):
        return [strip_clock_derived(item) for item in value]
    return value


class Application:
    """One `verification/start.sh` process."""

    def __init__(self, port: int, log_path: Path, mongodb_uri: str):
        self.port = port
        self.base_url = f"http://127.0.0.1:{port}"
        self.log_path = log_path
        environment = dict(os.environ)
        environment["APP_ADDR"] = f"127.0.0.1:{port}"
        environment["MONGODB_URI"] = mongodb_uri
        self.log = log_path.open("wb")
        self.process = subprocess.Popen(
            ["bash", "verification/start.sh"],
            cwd=str(WORKSPACE_ROOT),
            env=environment,
            stdout=self.log,
            stderr=subprocess.STDOUT,
        )

    @property
    def pid(self) -> int:
        return self.process.pid

    def wait_ready(self, timeout: float = 45.0) -> None:
        deadline = time.time() + timeout
        while time.time() < deadline:
            if self.process.poll() is not None:
                raise RuntimeError(
                    f"application exited with {self.process.returncode}\n"
                    + self.log_path.read_text(errors="replace")[-2000:]
                )
            try:
                with urllib.request.urlopen(self.base_url + "/ready", timeout=3) as response:
                    if response.status == 200:
                        return
            except (urllib.error.URLError, OSError):
                pass
            time.sleep(0.2)
        raise RuntimeError("application never became ready")

    def stop(self) -> None:
        if self.process.poll() is None:
            self.process.send_signal(signal.SIGTERM)
            try:
                self.process.wait(timeout=20)
            except subprocess.TimeoutExpired:
                self.process.kill()
                self.process.wait(timeout=10)
        self.log.close()

    def is_running(self) -> bool:
        return self.process.poll() is None


def describe(state: dict) -> str:
    return "status={} sealed={} holder={} planVersion={} sample_audit_total={}".format(
        state["sample_state"]["state"]["status"],
        state["lifecycle"]["sealed"],
        state["lifecycle"].get("currentHolderId"),
        state["plan"]["planVersion"],
        state["audit"]["total"],
    )


def snapshot(client: Client, sample_id: str, journey_id: str) -> dict:
    return {
        "sample_state": client.call("read_sample_state", path_value=sample_id),
        "lifecycle": client.call("read_sample_lifecycle", {"sampleId": sample_id}),
        "plan": client.call("read_journey_plan", {"journeyId": journey_id}),
        "audit": client.call(
            "search_audit_trail",
            {"entityType": "sample", "entityId": sample_id, "order": "oldest_first", "limit": 500},
        ),
    }


def main() -> int:
    mongodb_uri = os.environ.get("MONGODB_URI", "")
    if not mongodb_uri:
        print("MONGODB_URI is required", file=sys.stderr)
        return 2
    admin_username = os.environ.get("ADMIN_USERNAME", "")
    admin_password = os.environ.get("ADMIN_PASSWORD", "")
    if not admin_username or not admin_password:
        print("ADMIN_USERNAME and ADMIN_PASSWORD are required", file=sys.stderr)
        return 2

    # Application logs go to a scratch directory so a verification run never
    # changes the workspace it is verifying.
    work_dir = Path(tempfile.mkdtemp(prefix="durability-check-"))
    print(f"application logs: {work_dir}")
    port = free_port()
    failures: list[str] = []

    if os.environ.get("DURABILITY_SKIP_RESET", "") != "1":
        # Reset happens ONCE, before anything is written, purely so worker
        # fairness is deterministic. It is never run again after the write.
        print("step 0: verification/reset.sh (before any write)")
        subprocess.run(["bash", "verification/reset.sh"], cwd=str(WORKSPACE_ROOT), check=True)

    print(f"step 1: start the application (verification/start.sh) on port {port}")
    first = Application(port, work_dir / "app-before-restart.log", mongodb_uri)
    first.wait_ready()
    print(f"        instance A pid={first.pid} ready")

    try:
        admin = Client(first.base_url).login(admin_username, admin_password)
        durable_token = admin.token
        world = World(admin, f"dur{int(time.time())}").build()
        # Keys are unique per run so the check is repeatable without a reset.
        collect_key = f"{world.prefix}-collect"
        created = world.create_request(f"{world.prefix}-request")
        sample_id, journey_id = created["sampleId"], created["journeyId"]
        plan = admin.call("read_journey_plan", {"journeyId": journey_id})
        collection_assignment = plan["collectionAssignmentId"]
        collector_alias = world.find_offered(collection_assignment, ["collector", "collector2"])
        collector = world.session(collector_alias)
        collector.call(
            "accept_assignment",
            {"assignmentId": collection_assignment, "idempotencyKey": f"{world.prefix}-accept"},
        )
        collector.call(
            "collect_and_seal_sample",
            {
                "sampleId": sample_id,
                "assignmentId": collection_assignment,
                "collectedAt": world.at(minutes=1),
                "containerType": "sealed-tube",
                "observedTemperatureC": 5,
                "idempotencyKey": collect_key,
            },
        )
        collector_token = collector.token
        print(f"step 2: wrote sample {sample_id} (journey {journey_id}) over HTTP")
        before = snapshot(admin, sample_id, journey_id)
        print("        before restart: " + describe(before))
    except Exception:
        first.stop()
        raise

    print("step 3: stop the application process (SIGTERM), MongoDB keeps running")
    first.stop()
    if first.is_running():
        failures.append("instance A did not stop")
    print(f"        instance A pid={first.pid} exited rc={first.process.returncode}")

    print("step 4: run verification/start.sh again -- NO reset, same database")
    second = Application(port, work_dir / "app-after-restart.log", mongodb_uri)
    try:
        second.wait_ready()
        print(f"        instance B pid={second.pid} ready")
        if second.pid == first.pid:
            failures.append("the application was not actually restarted")

        # The session token minted by instance A is durable state too: if it
        # still authenticates, sessions survived the restart.
        restarted_admin = Client(second.base_url, durable_token)
        status, _ = restarted_admin.raw("read_operations_state", {})
        if status != 200:
            failures.append(
                f"the session token issued before the restart no longer works (HTTP {status})"
            )
            restarted_admin = Client(second.base_url).login(admin_username, admin_password)

        after = snapshot(restarted_admin, sample_id, journey_id)
        print("        after restart:  " + describe(after))

        for name in ("sample_state", "lifecycle", "plan"):
            want = strip_clock_derived(before[name])
            got = strip_clock_derived(after[name])
            if want != got:
                failures.append(
                    f"{name} changed across the restart:\n"
                    f"    before={json.dumps(want, sort_keys=True)[:600]}\n"
                    f"    after ={json.dumps(got, sort_keys=True)[:600]}"
                )

        if before["audit"]["total"] != after["audit"]["total"]:
            failures.append(
                "audit history for the sample changed across the restart: "
                f"{before['audit']['total']} -> {after['audit']['total']}"
            )
        before_actions = [entry["action"] for entry in before["audit"]["entries"]]
        after_actions = [entry["action"] for entry in after["audit"]["entries"]]
        if before_actions != after_actions:
            failures.append(
                f"audit chain changed across the restart: {before_actions} -> {after_actions}"
            )
        if "sample_collected_and_sealed" not in after_actions:
            failures.append(f"the sealing action did not survive the restart: {after_actions}")
        if after["lifecycle"]["sealed"] is not True:
            failures.append("the sample is no longer sealed after the restart")
        if not after["lifecycle"].get("currentHolderId"):
            failures.append("custody (currentHolderId) did not survive the restart")

        # The idempotency record must survive too: replaying the pre-restart key
        # has to return the original response instead of acting twice.
        replay = Client(second.base_url, collector_token).call(
            "collect_and_seal_sample",
            {
                "sampleId": sample_id,
                "assignmentId": collection_assignment,
                "collectedAt": world.at(minutes=1),
                "containerType": "sealed-tube",
                "observedTemperatureC": 5,
                "idempotencyKey": collect_key,
            },
        )
        if replay.get("sampleId") != sample_id or replay.get("status") != "sealed":
            failures.append(f"idempotent replay after the restart returned {replay}")
        recheck = restarted_admin.call(
            "search_audit_trail",
            {"entityType": "sample", "entityId": sample_id, "limit": 500},
        )
        if recheck["total"] != after["audit"]["total"]:
            failures.append(
                "replaying the pre-restart idempotency key added history: "
                f"{after['audit']['total']} -> {recheck['total']}"
            )
        print(
            "step 5: replayed the pre-restart idempotency key -> "
            f"same response, audit_total still {recheck['total']}"
        )
    finally:
        second.stop()

    if failures:
        print("\nAC-75 FAILED:")
        for failure in failures:
            print("  -", failure)
        return 1
    print(
        "\nAC-75 PASSED: sample, journey plan, custody, audit chain, session and "
        "idempotency record all survived an ordinary restart"
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
