# Task-specific proof of work

This directory is intentionally scoped to `lab-sample-pickup-and-delivery`. Do not copy evidence
from another task or retain ignored local Docker output here.

Before packaging, record at least two passing reference runs, two frontier-model candidate runs,
and a complete mutant matrix with a passing detection run for every mapped mutant. Keep only the
reviewed metadata and score ledgers in Git; transient logs and browser reports remain ignored.

The readiness commands validate the evidence against this repository's `verifier/`, contracts,
scoring rules, and task identifier.

Candidate workspaces and their verification reports are generated under
`benchmarking-candidates/<run-id>/candidate/`. The proof-of-work directory is reserved for private reference
and mutant evidence under `reference-runs/` and `mutant-runs/`. Do not move candidate workspaces
into proof-of-work or use proof-of-work as the generation root.
