# Portkey integration — wired, UNVERIFIED until access lands

Status: **staged and env-gated.** Default behavior is unchanged (Codex/ChatGPT for generation, Codex for
the mapper). Nothing routes through Portkey until you set the env vars below. **It has not been tested
against a live Portkey provider** — every call 400s today because our workspace key has no AI-Provider
attached. Verify with `portkey-probe.sh` the moment you get a slug.

## What was changed (reusing the OpenAI-compatible format)

Portkey speaks the same wire format as OpenAI (`/chat/completions`, `/responses`). The only additions are
Portkey's **routing headers**. All changes are additive and dormant unless activated:

1. **Mapper** — `verifier/mapper/hybrid_mapper.py` (`responses_adapter`): when `PORTKEY_PROVIDER` (or
   `PORTKEY_CONFIG`) is set, it sends `x-portkey-api-key` + `x-portkey-provider` instead of
   `Authorization: Bearer`. Otherwise identical to before (OpenAI-direct).
2. **Generation** — `generate-agent-solution`: `GEN_PROVIDER=portkey` injects a Codex `model_providers.portkey`
   config via `-c` overrides (base_url + `wire_api="chat"` + `http_headers` with the slug; secret stays in
   `env_key` → Authorization). `GEN_MODEL` picks the model. Default (`GEN_PROVIDER` unset) = today's Codex path.

## Blocker (must happen first, in Portkey — needs Org Admin)

Our key `kEo…3DG` (Shared Team Workspace, HackerRank-Dev) has **no AI Provider attached** and you lack
rights to add one (Model Catalog / LLM Integrations greyed out). An **Org Admin** must, in
**Model Catalog → AI Providers → Add Provider**, add a provider (OpenAI, or Groq/OpenRouter for open-source)
with its downstream key, and give you the **slug** (e.g. `openai-prod`).

## Single env file: `verifier.env`

Everything reads from `verifier.env` now (no separate gen.env). Set these there:

```bash
OPENAI_API_KEY=<portkey-token>              # the token (used by mapper AND generation)
OPENAI_BASE_URL=https://api.portkey.ai/v1   # the gateway endpoint
PORTKEY_CONFIG=pc-...                        # the config id (routing) — or PORTKEY_PROVIDER=<slug>
GEN_PROVIDER=portkey                         # toggle: route generation through Portkey
GEN_MODEL=<model>                            # a model the config serves
# CODEX_MAPPER_DISABLED=1                     # (optional) force the mapper onto the Portkey path too
```

## Activation (once you have a config id)

```bash
# 1) PROBE FIRST — reads verifier.env, confirms it returns before any full run
./portkey-probe.sh <model>                  # needs HTTP 200 + PONG

# 2) Generate a solution through Portkey (GEN_PROVIDER=portkey is set in verifier.env)
./generate-agent-solution

# 3) Mapper via Portkey (optional) — set CODEX_MAPPER_DISABLED=1 in verifier.env, then run-verifier
```

The flag `GEN_PROVIDER` is the toggle: unset → default Codex/ChatGPT generation (verifier.env's
OPENAI_API_KEY is ignored for auth); `portkey` → routes through Portkey with the values above.

## Tweak points if the probe fails (all one-liners)

- **400 "provider/config required"** → slug missing/wrong. Check `PORTKEY_PROVIDER`.
- **401 "incorrect API key: undefined"** → the AI Provider has no downstream credential; admin must add it.
- **model rejected** → `gpt-5.6-terra`/`gpt-5.6-sol` don't exist on Portkey; use a real model (`GEN_MODEL`).
- **`model_reasoning_effort` rejected by an OSS model** → drop the `--config "model_reasoning_effort=..."`
  line in `generate-agent-solution` for that model.
- **auth via header not Bearer** → if Portkey needs `x-portkey-api-key` (not Authorization) for the Codex
  path, add it to `http_headers` in the `codex_extra` block (note: puts the token in argv — acceptable
  only in a trusted shell).

## Reality caveat

This gym is long-horizon (500 steps, 4h, full-stack). Frontier models handle it; **most open-source models
will produce much weaker solutions.** The plumbing is done — model capability is the real variable.
