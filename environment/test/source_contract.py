#!/usr/bin/env python3
"""Validate the static contract for the shared RL gym environment."""

import json
from pathlib import Path


SCRIPT_PATH = Path(__file__).resolve()
REPOSITORY_ROOT = SCRIPT_PATH.parents[2]
REPOSITORY_ENVIRONMENT = REPOSITORY_ROOT / "environment"
if REPOSITORY_ENVIRONMENT.is_dir():
    ROOT = REPOSITORY_ROOT
    ENVIRONMENT = REPOSITORY_ENVIRONMENT
else:
    ROOT = SCRIPT_PATH.parents[1]
    ENVIRONMENT = ROOT

DOCKERFILE = ENVIRONMENT / "Dockerfile"
TEST_DIR = ENVIRONMENT / "test"
BROWSER_FIXTURE_DIR = TEST_DIR / "browser-fixture"
SCRIPT_PATHS = {
    "entrypoint": ENVIRONMENT / "bin" / "entrypoint.sh",
    "start": ENVIRONMENT / "bin" / "start-services.sh",
    "health": ENVIRONMENT / "bin" / "healthcheck.sh",
    "reset": ENVIRONMENT / "bin" / "reset-services.sh",
}


def require(condition: bool, message: str) -> None:
    """Raise an assertion with a concise contract failure message."""
    if not condition:
        raise AssertionError(message)


def read_required(path: Path) -> str:
    """Read a required file after reporting an environment-relative path."""
    relative_path = path.relative_to(ROOT)
    require(path.is_file(), f"missing required file: {relative_path}")
    return path.read_text(encoding="utf-8")


def validate_dockerfile(content: str) -> None:
    """Validate base images, packages, user boundary, and copied inputs."""
    required_stages = (
        "FROM golang:1.25.12-bookworm@sha256:",
        "FROM node:22.23.1-bookworm@sha256:",
        "FROM mcr.microsoft.com/playwright:v1.61.1-noble@sha256:",
        "FROM mongo:8.0.26-noble@sha256:",
    )
    for stage in required_stages:
        require(stage in content, f"Dockerfile missing official image stage: {stage}")

    required_packages = (
        "docker-compose-v2",
        "docker.io",
        "postgresql",
        "postgresql-client",
        "python3-venv",
        "redis-server",
        "redis-tools",
        "sudo",
        "tini",
    )
    for package in required_packages:
        require(package in content, f"Dockerfile missing package: {package}")

    require("agent:x:1000:1000" not in content, "agent must be created with account tools")
    require("--uid 1000" in content, "Dockerfile must create agent UID 1000")
    require("--gid 1000" in content, "Dockerfile must create agent GID 1000")
    require("WORKDIR /workspace" in content, "Dockerfile must use /workspace")
    require("\nUSER agent\n" in content, "Dockerfile final runtime user must be agent")
    require("COPY --from=browser-toolchain /ms-playwright/ /ms-playwright/" in content, "Dockerfile must copy bundled Chromium")
    require("PLAYWRIGHT_BROWSERS_PATH=/ms-playwright" in content, "Dockerfile must expose bundled Chromium")
    require("GOPROXY=off" not in content, "Dockerfile must allow runtime Go downloads")
    require("GOSUMDB=off" not in content, "Dockerfile must keep Go checksum verification enabled")
    require("NPM_CONFIG_OFFLINE" not in content, "Dockerfile must allow runtime npm downloads")
    require("OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets" in content, "Dockerfile must expose offline map assets")
    require("COPY --chown=root:root environment/assets/map/ /opt/arena/map-assets/" in content, "Dockerfile must copy only the public map bundle")
    require("COPY --chown=root:root environment/test/" not in content, "Dockerfile must not copy repository-only environment tests")
    require("elasticsearch" not in content.lower(), "Dockerfile must not include Elasticsearch in the environment image")
    require("ENTRYPOINT [\"/usr/bin/tini\", \"--\", \"/opt/arena/bin/entrypoint.sh\"]" in content, "Dockerfile must start services through the entrypoint")
    require("HEALTHCHECK" in content, "Dockerfile must define a container health check")
    require("COPY ." not in content, "Dockerfile must not copy the repository root")

    copy_lines = [
        line.strip().lower()
        for line in content.splitlines()
        if line.lstrip().lower().startswith(("copy ", "add "))
    ]
    forbidden_inputs = (
        "tasks/",
        "credential",
        "expected",
        "fixture",
        "golden",
        "grader",
        "seed",
        "solution",
        "trace",
        "verifier",
    )
    for line in copy_lines:
        for forbidden in forbidden_inputs:
            require(forbidden not in line, f"Dockerfile copies forbidden input: {line}")


def validate_scripts(scripts: dict[str, str]) -> None:
    """Validate lifecycle safety, network binding, and reset coverage."""
    for name, content in scripts.items():
        require(content.startswith("#!/usr/bin/env bash\n"), f"{name} script must use Bash")
        require("set -Eeuo pipefail" in content, f"{name} script must use strict Bash mode")

    start = scripts["start"]
    require("127.0.0.1" in start, "service startup must use loopback")
    require("--bind_ip 127.0.0.1" in start, "MongoDB must bind to loopback")
    require("--bind 127.0.0.1" in start, "Redis must bind to loopback")

    entrypoint = scripts["entrypoint"]
    require("start-services.sh" in entrypoint, "entrypoint must start infrastructure")
    require('exec "$@"' in entrypoint, "entrypoint must hand off to the container command")

    reset = scripts["reset"]
    reset_markers = {
        "PostgreSQL": "dropdb",
        "MongoDB": "dropDatabase",
        "Redis": "FLUSHDB",
    }
    for service, marker in reset_markers.items():
        require(marker in reset, f"reset script does not reset {service}")


def validate_smoke_files(compose: str, smoke: str) -> None:
    """Validate the repeatable networked container smoke-test boundary."""
    require("services:" in compose, "Compose smoke fixture must define services")
    require("DOCKER_CONTEXT" in smoke, "smoke runner must accept DOCKER_CONTEXT")
    require("IMAGE_TAG" in smoke, "smoke runner must accept IMAGE_TAG")
    for required_argument in ("--network bridge", "--cpus 4", "--memory 8g"):
        require(required_argument in smoke, f"smoke runner missing {required_argument}")
    require("--network none" not in smoke, "smoke runner must allow package-registry access")
    require("trap cleanup EXIT" in smoke, "smoke runner must always clean up its container")
    require("exec --interactive" in smoke, "smoke runner must forward assertion scripts over stdin")
    require("type=bind" in smoke and "target=/opt/arena/test" in smoke, "smoke runner must mount repository-only tests")
    require("container health: PASS" in smoke, "smoke runner must verify automatic startup")
    for forbidden_root in (
        "/opt/arena/app",
        "/opt/arena/expected",
        "/opt/arena/grader",
        "/opt/arena/seed",
        "/opt/arena/solution",
        "/opt/arena/trace",
        "/opt/arena/verifier",
    ):
        require(forbidden_root in smoke, f"smoke runner must reject hidden root: {forbidden_root}")


def validate_browser_fixture(files: dict[str, str], smoke: str) -> None:
    """Validate registry downloads plus local MapLibre and PMTiles rendering."""
    package = json.loads(files["package"])
    require(package.get("scripts", {}).get("build") == "vite build", "browser fixture must build with Vite")
    require(package.get("scripts", {}).get("test") == "playwright test", "browser fixture must test with Playwright")
    for dependency in ("maplibre-gl", "pmtiles", "vue"):
        require(dependency in package.get("dependencies", {}), f"browser fixture missing dependency: {dependency}")

    main = files["main"]
    for marker in ("new Protocol", "pmtiles://", "sample-point", "data-map-ready"):
        require(marker in main, f"browser fixture main is missing: {marker}")

    config = files["config"]
    for marker in ("127.0.0.1", "4173", "--use-gl=swiftshader", "retain-on-failure"):
        require(marker in config, f"Playwright config is missing: {marker}")

    test = files["test"]
    for marker in ("http://127.0.0.1:4173", "data:", "blob:", "maplibregl-canvas", "data-map-ready", "OpenStreetMap contributors"):
        require(marker in test, f"browser map test is missing: {marker}")

    require("--offline" not in smoke, "smoke runner must not force offline package installation")
    require("GOPROXY=off" not in smoke, "smoke runner must allow Go module downloads")
    for marker in ("npm install --ignore-scripts", "frontend build: PASS", "Go modules: PASS", "local browser map: PASS"):
        require(marker in smoke, f"smoke runner is missing package or map check: {marker}")


def main() -> None:
    """Run all source-level environment contract checks."""
    if DOCKERFILE.is_file():
        validate_dockerfile(read_required(DOCKERFILE))
    scripts = {name: read_required(path) for name, path in SCRIPT_PATHS.items()}
    validate_scripts(scripts)
    compose = read_required(TEST_DIR / "compose-smoke.yml")
    smoke = read_required(TEST_DIR / "smoke.sh")
    validate_smoke_files(compose, smoke)
    browser_files = {
        "package": read_required(BROWSER_FIXTURE_DIR / "package.json"),
        "index": read_required(BROWSER_FIXTURE_DIR / "index.html"),
        "main": read_required(BROWSER_FIXTURE_DIR / "src" / "main.ts"),
        "config": read_required(BROWSER_FIXTURE_DIR / "playwright.config.ts"),
        "test": read_required(BROWSER_FIXTURE_DIR / "tests" / "map.spec.ts"),
    }
    validate_browser_fixture(browser_files, smoke)
    print("environment source contract: PASS")


if __name__ == "__main__":
    main()
