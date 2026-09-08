# Commands cheat-sheet

Three root commands drive the whole loop. All output lands in `runs/<label>-<UTC>/`.

## Generate a candidate solution

```bash
./generate --provider codex   --model gpt-5.6-terra --reasoning medium --label terra-med
./generate --provider claude  --model claude-opus-4-8 --reasoning high
./generate --provider portkey --label pk            # model/route come from verifier.env (PORTKEY_CONFIG)
```

- `--provider`  `claude` (subscription creds ~/.claude, or ANTHROPIC_API_KEY) | `codex` (ChatGPT OAuth ~/.codex/auth.json) | `portkey` (OPENAI_API_KEY token + PORTKEY_CONFIG in verifier.env)
- `--model`, `--reasoning`, `--label` are optional; the label auto-derives from provider+model+reasoning.
- Output: `runs/<id>/solution/workspace/` (built app + `verification/`), plus `run-metadata.json` recording provider, model, reasoning, **durationSeconds**, exitCode, and best-effort **tokens**.

Local dev uses `claude`/`codex` (no API key). Packaged/vendor runs use `portkey` (no proprietary-model dependency).

## Verify an already-built solution

```bash
./verify --workspace runs/<id>/solution/workspace
```

Boots the candidate in a throwaway container (starts its datastores, runs the manifest's `build` → `reset` → `start`, waits for `/ready`), then runs the pinned-contract engine against it. Writes `verifier-logs/reward.json` and `verify-metadata.json` (records the verifier's **durationSeconds** + overall score).

## Do both, end to end

```bash
./evaluate --provider codex --model gpt-5.6-terra --reasoning medium
```

Generates then verifies into one `runs/<id>/` folder and prints a combined summary (generation duration+tokens, verifier duration, overall score).

## Run the engine directly (against an already-running app)

```bash
python3 verifier/qc-pinned-contracts/engine/run.py \
  --base-url http://127.0.0.1:PORT --admin-user admin --admin-pass admin123 \
  score --json-out reward.json
```

The engine is HTTP-only and deterministic — it talks to the app over the pinned contract
(`tasks/lab-sample-pickup-and-delivery/openapi.contract.json`); there is no LLM in the scoring path.

## Build the images (once)

```bash
docker build -t lab-sample-pickup-delivery-gym:local -f environment/Dockerfile .
docker build --build-arg BASE_IMAGE=lab-sample-pickup-delivery-gym:local \
  --build-arg CODEX_VERSION=0.144.6 \
  -t lab-sample-pickup-delivery-gym:codex-local \
  -f verifier/amazon-test/Dockerfile.codex .
```

`./generate` builds these automatically on first run if they're missing.
