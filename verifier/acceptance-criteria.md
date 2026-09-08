# Lab Sample Pickup and Delivery acceptance criteria

The private verifier evaluates the candidate only through the public HTTP and browser surfaces.
The frozen API contract is the authoritative request/response boundary. The backend suite covers
the requirements catalogued in `verifier/qc-pinned-contracts/ACCEPTANCE.md`, including role access,
serviceability, planning, custody, capacity, fairness, disruption recovery, audit, and durability.
The UI suite covers the 16 presentation scenarios declared in `tasks/public/contracts/ui.contract.json`.
