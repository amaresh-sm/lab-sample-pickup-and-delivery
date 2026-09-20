# ASTRA-RL Lab Sample Pickup and Delivery

This repository contains one greenfield ASTRA task. Candidate-visible files live under `tasks/`; the
private reference implementation, hidden tests, mutants, scoring rules, and evidence live under
`verifier/`.

## Canonical layout

```text
tasks/
├── task.toml
├── instruction.md
└── public/
    ├── README.md
    └── contracts/
        ├── openapi.contract.json
        └── ui.contract.json

verifier/
├── reference-solution/app-setup/
├── hidden-tests/
├── mutants/
├── acceptance-criteria.yml
├── scoring.yml
└── proof-of-work/
```

The generation container receives only `tasks/instruction.md` and `tasks/public/`. A completed
candidate must create `app-setup/manifest.json`, `build.sh`, `reset.sh`, `start.sh`, and its
`openapi.json` artifact in the candidate workspace.

## Validate, generate, verify, score

```bash
python3 scripts/validate_task.py tasks
python3 -m unittest discover -s harness_checks -q
python3 scripts/build_images.py

./generate-candidate-solution --provider codex --model <model> --reasoning medium
python3 -m astra_harness.verify \
  --task tasks \
  --candidate benchmarking-candidates/<run-id>/candidate \
  --run benchmarking-candidates/<run-id> \
  --verifier-command "python3 /input/verifier/run.py"
python3 -m astra_harness.score --task tasks --run benchmarking-candidates/<run-id>
python3 -m astra_harness.benchmark
```

`./generate`, `./verify`, `./evaluate`, and `./benchmark` remain small convenience aliases, but
all of them invoke the canonical ASTRA modules, use `benchmarking-candidates/<run-id>/`, and keep
the `candidate/app-setup/` layout inside each run.

The verifier supplies the administrator credentials and bounded operational timers to both the
candidate runtime and the private verifier. Override them with the verifier CLI options or a
private env file; never commit credentials.

Candidate generation supports `codex`, `openai-compatible`, `claude-code`, `openhands`, and `pi-dev`.
The Pi Dev provider uses the official `@mariozechner/pi-coding-agent` CLI with a dedicated image
and routes the configured model through the HackerRank Gateway's OpenAI-compatible endpoint. The
OpenHands provider uses the HackerRank Gateway package from
`--openhands-gateway-repo /path/to/hackerrank-openhands-gateway` (or
`OPENHANDS_GATEWAY_REPO`). The harness fingerprints that source and rebuilds the generation image
when it changes, so an existing image cannot silently use an older gateway package.

## Readiness and packaging

```bash
python3 readiness_checks/check_package.py
python3 readiness_checks/check_acceptance.py
python3 readiness_checks/check_report_privacy.py
python3 readiness_checks/check_scoring.py
python3 readiness_checks/check_determinism.py
python3 readiness_checks/check_proof_work.py
python3 readiness_checks/check_reference.py
python3 readiness_checks/check_mutants.py
python3 scripts/package_task.py --output dist/task-package.tar.gz
```

Proof-of-work evidence is task-specific and must be generated from this task's verifier. It is not
copied from another task or accepted from ignored local run artifacts.
