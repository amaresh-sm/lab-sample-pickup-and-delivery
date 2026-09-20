"""Provider-specific command builders for candidate generation."""

from __future__ import annotations

import json
import shlex
from dataclasses import dataclass


@dataclass(frozen=True)
class ProviderCommand:
    """A command and the credential variables it may receive."""

    command: str
    credential_env: tuple[str, ...]


def build_provider_command(provider: str, model: str, reasoning: str) -> ProviderCommand:
    """Translate the common provider request into a CLI command."""
    model_arg = shlex.quote(model)
    if provider == "codex":
        return ProviderCommand(
            f"codex exec --json --color never --ephemeral --ignore-user-config --ignore-rules "
            f"--skip-git-repo-check --dangerously-bypass-approvals-and-sandbox --cd /workspace "
            f"-m {model_arg} -c model_reasoning_effort={shlex.quote(reasoning)} -",
            ("CODEX_API_KEY", "OPENAI_API_KEY"),
        )
    if provider == "openai-compatible":
        return ProviderCommand(
            f"codex exec --json --color never --ephemeral --ignore-user-config --ignore-rules "
            f"--skip-git-repo-check --dangerously-bypass-approvals-and-sandbox --cd /workspace "
            f"-m {model_arg} -c model_reasoning_effort={shlex.quote(reasoning)} -",
            ("OPENAI_API_KEY", "OPENAI_BASE_URL", "PORTKEY_API_KEY"),
        )
    if provider == "claude-code":
        return ProviderCommand(
            f"claude -p --model {model_arg} --effort {shlex.quote(reasoning)} "
            "--dangerously-skip-permissions --output-format stream-json --verbose",
            ("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL"),
        )
    if provider == "openhands":
        return ProviderCommand(
            "hackerrank-openhands run "
            "--workspace /workspace "
            "--instruction-file /workspace/INSTRUCTION.md "
            f"--model {model_arg} --reasoning {shlex.quote(reasoning)} "
            "--output /tmp/openhands-run --redact",
            ("ASTRA_GATEWAY_API_KEY", "ASTRA_GATEWAY_BASE_URL", "LLM_API_KEY", "LLM_BASE_URL"),
        )
    if provider == "pi-dev":
        # Pi reads custom OpenAI-compatible providers from models.json.  The
        # values are environment-variable names, so credentials never enter
        # the image or the generated command line.
        model_config = json.dumps(
            {
                "providers": {
                    "hackerrank": {
                        "api": "openai-completions",
                        "apiKey": "ASTRA_GATEWAY_API_KEY",
                        "models": [
                            {
                                "id": model,
                                "name": model,
                                "reasoning": True,
                                "input": ["text"],
                                "contextWindow": 200000,
                                "maxTokens": 120000,
                                "compat": {
                                    "supportsDeveloperRole": False,
                                    "supportsReasoningEffort": True,
                                },
                            }
                        ],
                    }
                }
            },
            separators=(",", ":"),
        )
        write_config = (
            "const fs=require('fs');"
            f"const config={model_config};"
            "if(!process.env.ASTRA_GATEWAY_BASE_URL)throw new Error('ASTRA_GATEWAY_BASE_URL is required');"
            "config.providers.hackerrank.baseUrl=process.env.ASTRA_GATEWAY_BASE_URL;"
            "fs.writeFileSync('/home/agent/.pi/agent/models.json',JSON.stringify(config));"
        )
        return ProviderCommand(
            f"mkdir -p /home/agent/.pi/agent && node -e {shlex.quote(write_config)} && "
            f"pi --model hackerrank/{shlex.quote(model)} --thinking {shlex.quote(reasoning)} "
            "-p @/workspace/INSTRUCTION.md",
            ("ASTRA_GATEWAY_API_KEY", "ASTRA_GATEWAY_BASE_URL"),
        )
    raise ValueError("provider must be codex, openai-compatible, claude-code, openhands, or pi-dev")
