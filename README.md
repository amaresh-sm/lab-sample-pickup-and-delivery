# ASTRA-RL Lab Sample Pickup and Delivery

This repository is the ASTRA-format migration of the lab sample pickup and delivery task. It
keeps candidate-visible material under `tasks/` and evaluator-private material under `verifier/`.

## Task package

```text
tasks/
├── task.toml
├── instruction.md
└── public/contracts/
    ├── openapi.contract.json
    └── ui.contract.json

verifier/
├── reference-solution/
├── hidden-tests/backend/
├── hidden-tests/ui/
├── mutants/
├── acceptance-criteria.yml
├── scoring.yml
└── proof-of-work/
```

The candidate receives only `tasks/instruction.md` and `tasks/public/`. The private verifier grades
the running application through the frozen HTTP contract and browser UI; it does not inspect
candidate databases or source code.

## Validate and evaluate

```bash
python3 scripts/validate_task.py tasks
python3 -m unittest discover -s harness_checks
python3 -m astra_harness.generate --task tasks --provider codex --model <model> --reasoning medium
python3 -m astra_harness.verify --task tasks --candidate runs/<task>/<candidate>/candidate \
  --run runs/<task>/<candidate> --verifier-command "python3 /input/verifier/run.py"
python3 -m astra_harness.benchmark --runs-root runs
```

Build the images first with `python3 scripts/build_images.py`. The runtime image is based on the
original environment and includes Go, Node, MongoDB, PostgreSQL, Redis, Playwright, and local map
assets. The ASTRA harness adds isolated candidate-generation and verifier boundaries around it.

## Scoring

`verifier/score_adapter.py` converts the pinned-contract reward into ASTRA's normalized criteria
ledger. The published score is 2% API artifact, 8% runtime contract, 54% backend behavior, 7% UI
behavior, 1% UI contract audit, and 28% reliability. Reliability includes durability across
restart, idempotency replay, and concurrent safety.

The legacy capability engine remains under `verifier/hidden-tests/backend/legacy-engine/`; its
HTTP-only behavioral probes are invoked by the ASTRA task-scoped `verifier/run.py` adapter.
