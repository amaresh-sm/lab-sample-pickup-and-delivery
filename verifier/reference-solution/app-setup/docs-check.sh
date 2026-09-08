#!/usr/bin/env bash
# AC-82: build / run / reset / test are documented -- accurately, and checkably.
#
# `bash -n` only proves the scripts parse. This check compares what README.md
# claims against what the repository actually does:
#
#   * every manifest command (build/start/reset) appears verbatim in README
#   * every app-setup/*.sh entry point is documented, and every script README
#     names exists
#   * the frontend and backend test commands README gives really work that way
#   * the manifest's frontend/api/ready URLs are the ones README prints
#   * every operator-tunable environment variable the backend reads is
#     documented, with the default and range the Go source actually enforces
set -euo pipefail

WORKSPACE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$WORKSPACE_ROOT"

# Every documented entry point has to parse, not just the four the smoke check
# covers.
for script in app-setup/*.sh; do
  bash -n "$script" || {
    echo "AC-82 FAILED: $script is not valid bash" >&2
    exit 1
  }
done
python3 -m py_compile app-setup/*.py

python3 - <<'PY'
import json
import re
import shlex
import sys
from pathlib import Path

root = Path.cwd()
readme = (root / "README.md").read_text()
manifest = json.loads((root / "app-setup" / "manifest.json").read_text())
failures: list[str] = []


def check(condition, message):
    if not condition:
        failures.append(message)


# --------------------------------------------------------------- README commands
# Every fenced block, with backslash continuations joined, so a command written
# across several lines is compared as the single command it is.
blocks = re.findall(r"```(?:bash|sh|shell)?\n(.*?)```", readme, re.S)
check(bool(blocks), "README.md contains no fenced command blocks")
documented_commands: list[str] = []
for block in blocks:
    joined = re.sub(r"\\\n\s*", " ", block)
    for line in joined.splitlines():
        line = line.split("#", 1)[0].strip()
        if line:
            documented_commands.append(re.sub(r"\s+", " ", line))


def without_environment_prefix(command: str) -> str:
    """Drop leading VAR=value assignments so `A=1 bash x.sh` matches `bash x.sh`."""
    try:
        parts = shlex.split(command)
    except ValueError:
        return command
    while parts and re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*=.*", parts[0]):
        parts.pop(0)
    return " ".join(parts)


bare_commands = {without_environment_prefix(command) for command in documented_commands}

for name in ("build", "reset", "start"):
    argv = manifest["commands"][name]
    wanted = " ".join(argv)
    check(
        wanted in bare_commands,
        f"README.md does not document the manifest {name} command {wanted!r}",
    )
    script = next((part for part in argv if part.endswith(".sh")), "")
    check(
        bool(script) and (root / script).is_file(),
        f"the manifest {name} command references a missing script: {argv}",
    )

# ------------------------------------------------- scripts documented vs on disk
mentioned_scripts = set(re.findall(r"app-setup/[A-Za-z0-9._-]+\.sh", readme))
for script in sorted(mentioned_scripts):
    check((root / script).is_file(), f"README.md documents {script}, which does not exist")
on_disk = {f"app-setup/{path.name}" for path in (root / "app-setup").glob("*.sh")}
undocumented = sorted(on_disk - mentioned_scripts)
check(
    not undocumented,
    "app-setup scripts that README.md never mentions: " + ", ".join(undocumented),
)

# ------------------------------------------------------------------------- URLs
for name in ("frontend", "api", "ready"):
    url = manifest["urls"][name]
    check(url in readme, f"README.md does not state the manifest {name} URL {url}")

# ------------------------------------------------------------ test instructions
build_script = (root / "app-setup" / "build.sh").read_text()
check(
    "go test ./..." in build_script,
    "README.md claims the manifest build runs all Go tests, but app-setup/build.sh does not",
)
package = json.loads((root / "frontend" / "package.json").read_text())
npm_scripts = package.get("scripts", {})
npm_commands = [
    command for command in bare_commands if command.startswith("npm ") or command.startswith("npx ")
]
check(bool(npm_commands), "README.md documents no frontend test command")
for command in npm_commands:
    parts = command.split()
    if parts[0] == "npm" and len(parts) >= 2 and parts[1] not in {"ci", "install", "run"}:
        check(
            parts[1] in npm_scripts,
            f"README.md documents `{command}` but frontend/package.json has no {parts[1]!r} script",
        )
    if parts[0] == "npm" and len(parts) >= 3 and parts[1] == "run":
        check(
            parts[2] in npm_scripts,
            f"README.md documents `{command}` but frontend/package.json has no {parts[2]!r} script",
        )
go_test_commands = [command for command in bare_commands if command.startswith("go test")]
check(bool(go_test_commands), "README.md documents no backend test command")

# --------------------------------------------------- environment documentation
backend = root / "backend"
sources = sorted(
    path for path in backend.rglob("*.go") if not path.name.endswith("_test.go")
)
read_variables: dict[str, Path] = {}
for path in sources:
    for name in re.findall(r'os\.Getenv\("([A-Z0-9_]+)"\)', path.read_text()):
        read_variables.setdefault(name, path)
    for name in re.findall(r'getenv\("([A-Z0-9_]+)"', path.read_text()):
        read_variables.setdefault(name, path)
check(bool(read_variables), "no environment variables were found in the Go sources")

start_script = (root / "app-setup" / "start.sh").read_text()
# Variables start.sh sets itself are deployment wiring, not operator knobs.
wired_by_start = set(re.findall(r"^\s*export\s+([A-Z0-9_]+)=", start_script, re.M))
operator_variables = sorted(set(read_variables) - wired_by_start)
for name in operator_variables:
    check(
        name in readme,
        f"{name} is read by {read_variables[name].relative_to(root)} but README.md never documents it",
    )

# Documented defaults and ranges must be the ones the Go source enforces.
flat_readme = re.sub(r"\s+", " ", readme)
documented_ranges = re.findall(
    r"`([A-Z0-9_]+)` — [^`]*default `(\d+)`, range `(\d+)\.\.(\d+)`", flat_readme
)
check(
    len(documented_ranges) >= 3,
    f"README.md documents {len(documented_ranges)} tunable timers with a default and range, want at least 3",
)
constant_re = re.compile(r"^\s*([A-Za-z][A-Za-z0-9_]*)\s*=\s*(\d+)\s*$", re.M)
for name, default, low, high in documented_ranges:
    path = read_variables.get(name)
    if path is None:
        failures.append(f"README.md documents {name}, which no Go source reads")
        continue
    text = path.read_text()
    lines = text.splitlines()
    index = next(i for i, line in enumerate(lines) if f'"{name}"' in line)
    start = next((i for i in range(index, -1, -1) if lines[i].startswith("func ")), 0)
    end = next((i for i in range(index, len(lines)) if lines[i] == "}"), len(lines))
    body = "\n".join(lines[start : end + 1])
    check(
        re.search(rf"value >= {low} && value <= {high}", body) is not None,
        f"README.md says {name} accepts {low}..{high}; "
        f"{path.relative_to(root)} enforces {re.findall(r'value >= (\\d+) && value <= (\\d+)', body)}",
    )
    constants = dict(constant_re.findall(text))
    literals = set(re.findall(r"\b(\d+)\b", body))
    for identifier in re.findall(r"\b([A-Za-z][A-Za-z0-9_]*)\b", body):
        if identifier in constants:
            literals.add(constants[identifier])
    check(
        default in literals,
        f"README.md says {name} defaults to {default}, which does not appear in "
        f"{path.relative_to(root)}'s {name} handling",
    )

if failures:
    print("AC-82 FAILED: README.md does not match the repository:", file=sys.stderr)
    for failure in failures:
        print("  -", failure, file=sys.stderr)
    raise SystemExit(1)

print(
    f"AC-82 PASSED: {len(documented_commands)} documented command lines, "
    f"{len(on_disk)} app-setup scripts, {len(operator_variables)} operator variables "
    f"and {len(documented_ranges)} tunable timers all match the repository"
)
PY
