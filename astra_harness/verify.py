"""Run a task's private verifier against one candidate in Docker."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import tomllib
import time
from datetime import datetime, timezone
from pathlib import Path

from .redaction import redact_tree


DEFAULT_VERIFIER_ENV = {
    "ADMIN_USERNAME": "admin",
    "ADMIN_PASSWORD": "admin123",
    # Keep the long-running qualification probe bounded in local and CI runs.
    # These values remain overridable through an env file or the CLI.
    "UNASSIGNED_SEARCH_TIMEOUT_SECONDS": "60",
    "ASSIGNMENT_OFFER_TTL_SECONDS": "120",
    "MAINTENANCE_INTERVAL_MILLISECONDS": "1000",
    "ENGINE_MAINTENANCE_INTERVAL_SECONDS": "1",
}
SELECTED_ENV_KEYS = tuple(DEFAULT_VERIFIER_ENV)


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Verify a candidate in an isolated container")
    parser.add_argument("--task", type=Path, required=True)
    parser.add_argument("--candidate", type=Path, required=True)
    parser.add_argument("--run", type=Path, required=True, help="Directory where reports and metadata are written")
    parser.add_argument("--verifier-command", required=True)
    parser.add_argument("--image", default="astra-verifier:latest")
    parser.add_argument("--runtime-image", default="astra-candidate-runtime:latest")
    parser.add_argument("--env-file", type=Path, default=None)
    parser.add_argument(
        "--candidate-env-file",
        type=Path,
        default=None,
        help="private environment file injected into the candidate runtime",
    )
    parser.add_argument("--admin-user", default=None, help="administrator username (default: admin)")
    parser.add_argument("--admin-pass", default=None, help="administrator password (default: admin123)")
    parser.add_argument("--unassigned-search-timeout-seconds", type=int, default=None)
    parser.add_argument("--assignment-offer-ttl-seconds", type=int, default=None)
    parser.add_argument("--maintenance-interval-milliseconds", type=int, default=None)
    parser.add_argument("--timeout-seconds", type=int, default=14_400)
    return parser.parse_args()


def read_env_file(path: Path | None) -> dict[str, str]:
    """Read simple KEY=VALUE entries without expanding or exposing other secrets."""

    if path is None or not path.is_file():
        return {}
    values: dict[str, str] = {}
    for raw_line in path.read_text(encoding="utf-8").splitlines():
        line = raw_line.strip()
        if not line or line.startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        key = key.strip()
        if key not in SELECTED_ENV_KEYS:
            continue
        value = value.strip()
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {"'", '"'}:
            value = value[1:-1]
        values[key] = value
    return values


def verifier_environment(args: argparse.Namespace) -> dict[str, str]:
    """Build the bounded environment shared by the app and private verifier."""

    values = dict(DEFAULT_VERIFIER_ENV)
    values.update({key: os.environ[key] for key in SELECTED_ENV_KEYS if key in os.environ})
    values.update(read_env_file(args.env_file))
    values.update(read_env_file(args.candidate_env_file))
    overrides = {
        "ADMIN_USERNAME": args.admin_user,
        "ADMIN_PASSWORD": args.admin_pass,
        "UNASSIGNED_SEARCH_TIMEOUT_SECONDS": args.unassigned_search_timeout_seconds,
        "ASSIGNMENT_OFFER_TTL_SECONDS": args.assignment_offer_ttl_seconds,
        "MAINTENANCE_INTERVAL_MILLISECONDS": args.maintenance_interval_milliseconds,
    }
    values.update({key: str(value) for key, value in overrides.items() if value is not None})
    return values


def docker_environment_args(values: dict[str, str]) -> list[str]:
    """Render environment values using Docker's required ``KEY=VALUE`` form."""

    return [argument for key, value in values.items() for argument in ("--env", f"{key}={value}")]


def docker_exec(
    runtime_name: str, command: list[str], *, cwd: str = "/workspace", stdout: object | None = None, timeout: float | None = None
) -> subprocess.CompletedProcess[bytes]:
    """Run an untrusted manifest command only inside the candidate runtime."""

    return subprocess.run(
        ["docker", "exec", "--workdir", cwd, runtime_name, *command],
        stdout=stdout,
        stderr=subprocess.STDOUT if stdout is not None else None,
        check=False,
        timeout=timeout,
    )


def load_candidate_manifest(candidate: Path) -> tuple[dict[str, list[str]], str, str | None]:
    """Read the public lifecycle handoff without importing verifier material."""

    path = candidate / "app-setup" / "manifest.json"
    if not path.is_file():
        raise SystemExit(f"candidate manifest is missing: {path}")
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        raise SystemExit(f"invalid candidate manifest: {exc}") from exc
    commands = raw.get("commands") if isinstance(raw, dict) else None
    if not isinstance(commands, dict):
        raise SystemExit("manifest must contain a commands object")
    selected: dict[str, list[str]] = {}
    for name in ("build", "reset", "start"):
        value = commands.get(name)
        if not isinstance(value, list) or not value or not all(isinstance(item, str) for item in value):
            raise SystemExit(f"manifest commands.{name} must be a non-empty argument array")
        selected[name] = value
    urls = raw.get("urls") if isinstance(raw, dict) else None
    ready_url = urls.get("ready") if isinstance(urls, dict) else None
    if ready_url is not None and not isinstance(ready_url, str):
        raise SystemExit("manifest urls.ready must be a string when provided")
    return selected, str(path.relative_to(candidate)), ready_url


def stop_process(process: subprocess.Popen[bytes] | None) -> None:
    if process is None or process.poll() is not None:
        return
    process.terminate()
    try:
        process.wait(timeout=10)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=10)


def wait_for_candidate_ready(runtime_name: str, ready_url: str, process: subprocess.Popen[bytes], timeout: int = 120) -> None:
    """Wait for the declared application readiness endpoint before acknowledging a restart."""

    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise RuntimeError("candidate process exited before its readiness endpoint became healthy")
        probe = docker_exec(
            runtime_name,
            ["curl", "--fail", "--silent", "--show-error", "--max-time", "2", ready_url],
            stdout=subprocess.DEVNULL,
            timeout=5,
        )
        if probe.returncode == 0:
            return
        time.sleep(0.25)
    raise RuntimeError(f"candidate readiness endpoint did not become healthy: {ready_url}")


def start_runtime_candidate(
    runtime_name: str, command: list[str], log_path: Path, ready_url: str | None = None
) -> subprocess.Popen[bytes]:
    """Start the foreground candidate command and retain its in-container PID."""

    process = subprocess.Popen(
        [
            "docker", "exec", "--workdir", "/workspace", runtime_name,
            "sh", "-c", 'echo "$$" > /tmp/astra-candidate.pid; exec "$@"',
            "astra-candidate-start", *command,
        ],
        stdout=log_path.open("ab"),
        stderr=subprocess.STDOUT,
    )
    if ready_url:
        wait_for_candidate_ready(runtime_name, ready_url, process)
    return process


def stop_runtime_candidate(runtime_name: str, process: subprocess.Popen[bytes] | None) -> None:
    """Stop precisely the candidate app rather than every process in its container."""

    subprocess.run(
        [
            "docker", "exec", runtime_name, "sh", "-c",
            'if [ -r /tmp/astra-candidate.pid ]; then kill -TERM "$(cat /tmp/astra-candidate.pid)" 2>/dev/null || true; rm -f /tmp/astra-candidate.pid; fi',
        ],
        check=False,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
    )
    stop_process(process)


def wait_for_runtime_database(runtime_name: str, timeout: int) -> None:
    """Wait for every runtime datastore before manifest commands run.

    The runtime entrypoint starts PostgreSQL, MongoDB, and Redis together.
    Waiting only for PostgreSQL allowed a manifest build command to race the
    entrypoint while the other services were still starting.
    """

    deadline = time.monotonic() + min(timeout, 60)
    while time.monotonic() < deadline:
        probe = subprocess.run(
            [
                "docker", "exec", runtime_name, "/opt/arena/bin/healthcheck.sh",
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )
        if probe.returncode == 0:
            return
        time.sleep(0.25)
    raise RuntimeError("candidate runtime database did not become ready")


def wait_for_restart_request(
    verifier_process: subprocess.Popen[bytes], *, control_dir: Path, runtime_name: str,
    reset_command: list[str], start_command: list[str], ready_url: str | None, logs: Path,
    candidate_process: subprocess.Popen[bytes], timeout: int,
) -> subprocess.Popen[bytes]:
    """Perform only verifier-requested restarts, without sharing its files with the app."""

    handled: set[str] = set()
    deadline = time.monotonic() + timeout
    control_timeout = int(os.environ.get("ASTRA_CAPABILITY_TIMEOUT_SECONDS", "90"))
    while verifier_process.poll() is None:
        if time.monotonic() > deadline:
            verifier_process.kill()
            raise TimeoutError("verifier timed out")
        for request in sorted(control_dir.glob("*.json")):
            label = request.stem.removeprefix("restart-")
            action = "restart"
            if request.name.startswith("reset-start-"):
                label = request.stem.removeprefix("reset-start-")
                action = "reset-start"
            if label in handled:
                continue
            stop_runtime_candidate(runtime_name, candidate_process)
            ack = control_dir / f"{('reset-start' if action == 'reset-start' else 'restart')}-{label}.ack"
            try:
                if action == "reset-start":
                    reset_result = docker_exec(runtime_name, reset_command, stdout=subprocess.DEVNULL, timeout=control_timeout)
                    if reset_result.returncode != 0:
                        raise RuntimeError(f"candidate reset command failed during {label}")
                candidate_process = start_runtime_candidate(runtime_name, start_command, logs / "candidate.start.log", ready_url)
            except (OSError, RuntimeError, subprocess.TimeoutExpired) as exc:
                candidate_process = None
                ack.write_text(f"blocked: {exc}\n", encoding="utf-8")
                handled.add(label)
                continue
            ack.write_text("ok\n", encoding="utf-8")
            handled.add(label)
        time.sleep(0.2)
    return candidate_process


def locate_verifier(task_dir: Path) -> Path:
    """Resolve the task-private verifier without exposing it to candidates."""
    nested = task_dir / "verifier"
    if nested.is_dir():
        return nested
    if task_dir.name == "tasks":
        flat_verifier = task_dir.parent / "verifier"
        if flat_verifier.is_dir():
            return flat_verifier
    repository_verifier = task_dir.parents[1] / "verifier" / task_dir.name
    if repository_verifier.is_dir():
        return repository_verifier
    raise SystemExit(f"private verifier is missing for task: {task_dir}")


def task_identifier(task_dir: Path) -> str:
    """Return the stable task id from task.toml for flat task packages."""
    try:
        metadata = tomllib.loads((task_dir / "task.toml").read_text(encoding="utf-8"))
    except (OSError, tomllib.TOMLDecodeError):
        return task_dir.name
    value = metadata.get("id")
    return value if isinstance(value, str) and value else task_dir.name


def run(args: argparse.Namespace) -> int:
    task_dir = args.task.resolve()
    candidate = args.candidate.resolve()
    verifier = locate_verifier(task_dir).resolve()
    public = (task_dir / "public").resolve()
    run_dir = args.run.resolve()
    if not candidate.is_dir():
        raise SystemExit(f"candidate directory does not exist: {candidate}")
    if not verifier.is_dir():
        raise SystemExit(f"task must contain private verifier/: {verifier}")
    run_dir.mkdir(parents=True, exist_ok=True)
    logs = run_dir / "logs"
    logs.mkdir(exist_ok=True)
    reports = run_dir / "reports"
    reports.mkdir(exist_ok=True)

    task_id = task_identifier(task_dir)
    name = f"astra-verify-{task_id}-{run_dir.name}".replace("_", "-")
    runtime_name = f"{name}-runtime"
    commands, manifest_path, ready_url = load_candidate_manifest(candidate)
    shared_env = verifier_environment(args)
    runtime_args = [
        "docker", "run", "--detach", "--rm", "--name", runtime_name,
        "--cap-drop", "NET_RAW",
        # The shared candidate runtime starts PostgreSQL, MongoDB, and Redis
        # through its narrowly-scoped sudoers entries.  Docker's
        # no-new-privileges flag would make that documented startup path fail
        # before the candidate process is launched.
        "--mount", f"type=bind,src={candidate},dst=/workspace",
    ]
    if args.candidate_env_file:
        runtime_args += ["--env-file", str(args.candidate_env_file.resolve())]
    runtime_args += docker_environment_args(shared_env)
    runtime_args += [args.runtime_image]
    docker_args = [
        "docker", "run", "--rm", "--name", name,
        "--network", f"container:{runtime_name}",
        "--mount", f"type=bind,src={candidate},dst=/input/candidate,readonly",
        "--mount", f"type=bind,src={verifier},dst=/input/verifier,readonly",
    ]
    if public.is_dir():
        # The verifier needs frozen public contracts as grader-owned input.
        # Mounting the task copy separately prevents a candidate from changing
        # its own exported handoff and thereby changing the comparison target.
        docker_args += ["--mount", f"type=bind,src={public},dst=/input/public,readonly"]
    docker_args += [
        "--mount", f"type=bind,src={reports},dst=/output",
        "--env", f"VERIFIER_COMMAND={args.verifier_command} --candidate /input/candidate --output /output --external-lifecycle",
        "--env", "ASTRA_EXTERNAL_LIFECYCLE=1",
        "--env", "ASTRA_CONTROL_DIR=/output/control",
        "--env", "ASTRA_PUBLIC_ROOT=/input/public",
        args.image,
    ]
    if args.env_file:
        docker_args[2:2] = ["--env-file", str(args.env_file.resolve())]
    docker_args[2:2] += docker_environment_args(shared_env)

    metadata_path = run_dir / "verification.json"
    metadata: dict[str, object] = {
        "task_id": task_id,
        "candidate_id": candidate.parent.name if candidate.name == "candidate" else candidate.name,
        "workspace": "<candidate-workspace>",
        "image": args.image,
        "runtime_image": args.runtime_image,
        "manifest": manifest_path,
        "started_at": utc_now(),
        "status": "running",
    }
    metadata_path.write_text(json.dumps(metadata, indent=2) + "\n")
    started = time.monotonic()
    runtime_started = False
    candidate_process: subprocess.Popen[bytes] | None = None
    try:
        subprocess.run(runtime_args, check=True, stdout=subprocess.DEVNULL)
        runtime_started = True
        wait_for_runtime_database(runtime_name, args.timeout_seconds)
        with (logs / "candidate.build.log").open("wb") as log:
            build_result = docker_exec(runtime_name, commands["build"], stdout=log, timeout=args.timeout_seconds)
        if build_result.returncode != 0:
            raise RuntimeError("candidate build command failed")
        with (logs / "candidate.reset.log").open("wb") as log:
            reset_result = docker_exec(runtime_name, commands["reset"], stdout=log, timeout=args.timeout_seconds)
        if reset_result.returncode != 0:
            raise RuntimeError("candidate reset command failed")
        candidate_process = start_runtime_candidate(runtime_name, commands["start"], logs / "candidate.start.log", ready_url)
        control_dir = reports / "control"
        control_dir.mkdir(exist_ok=True)
        result = subprocess.Popen(
            docker_args,
            stdout=(logs / "verifier.stdout.log").open("w"),
            stderr=(logs / "verifier.stderr.log").open("w"),
            env={key: value for key, value in os.environ.items() if key != "VERIFIER_COMMAND"},
        )
        # The task verifier asks for a restart through a host-only output
        # handshake. Candidate code never receives that directory.
        candidate_process = wait_for_restart_request(
            result,
            control_dir=control_dir,
            runtime_name=runtime_name,
            reset_command=commands["reset"],
            start_command=commands["start"],
            ready_url=ready_url,
            logs=logs,
            candidate_process=candidate_process,
            timeout=args.timeout_seconds,
        )
        metadata["status"] = "passed" if result.returncode == 0 else "failed"
        metadata["exit_code"] = result.returncode
    except subprocess.TimeoutExpired:
        metadata["status"] = "timed_out"
        metadata["exit_code"] = 124
    except OSError as exc:
        metadata["status"] = "failed"
        metadata["error"] = str(exc)
        metadata["exit_code"] = 127
    except (RuntimeError, subprocess.CalledProcessError, TimeoutError) as exc:
        metadata["status"] = "failed"
        metadata["error"] = str(exc)
        metadata["exit_code"] = 1
    finally:
        if runtime_started:
            stop_runtime_candidate(runtime_name, candidate_process)
        else:
            stop_process(candidate_process)
        if runtime_started:
            subprocess.run(["docker", "rm", "--force", runtime_name], check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    metadata["finished_at"] = utc_now()
    metadata["duration_seconds"] = round(time.monotonic() - started, 3)
    metadata_path.write_text(json.dumps(metadata, indent=2) + "\n")
    redact_tree(
        run_dir,
        {
            candidate: "<candidate-workspace>",
            task_dir: "<task-package>",
            verifier: "<private-verifier>",
            run_dir: "<run-directory>",
        },
    )
    print(run_dir)
    return 0 if metadata["status"] == "passed" else 1


def main() -> None:
    raise SystemExit(run(parse_args()))


if __name__ == "__main__":
    main()
