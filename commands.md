# ASTRA task commands

The canonical task package is `tasks/`, and each candidate run is stored at
`candidates/<run-id>/`. The generated candidate workspace lives at
`candidates/<run-id>/candidate/`.

## Validate and build images

```bash
python3 scripts/validate_task.py tasks
python3 -m unittest discover -s harness_checks -q
python3 scripts/build_images.py
```

## Generate a candidate

```bash
python3 -m astra_harness.generate \
  --task tasks --provider codex --model <model> --reasoning medium
```

The candidate sees only `INSTRUCTION.md` and `contracts/` from the public task package. Provider
credentials belong in a private `--env-file`; they are never committed. Supported providers are
`codex`, `openai-compatible`, `claude-code`, and `openhands`. For `openhands`, pass
`--openhands-gateway-repo /path/to/hackerrank-openhands-gateway` when the image is built.

## Verify and score a candidate

```bash
python3 -m astra_harness.verify \
  --task tasks \
  --candidate candidates/<run-id>/candidate \
  --run candidates/<run-id> \
  --verifier-command "python3 /input/verifier/run.py"
python3 -m astra_harness.score --task tasks --run candidates/<run-id>
```

The verifier starts the candidate from `candidate/app-setup/manifest.json`, injects
`ADMIN_USERNAME`/`ADMIN_PASSWORD` and bounded timer values into both containers, and writes
`reports/criteria.json` plus `reports/score.json`.

## End-to-end convenience command

```bash
./evaluate --provider codex --model <model> --reasoning medium
```

The root `generate`, `verify`, and `benchmark` commands are compatibility aliases for the ASTRA
modules. Use the module commands above in new automation.

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
