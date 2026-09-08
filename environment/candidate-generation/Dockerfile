# The source environment image already contains the language toolchains used by
# this task; this image adds only the provider CLIs used by the ASTRA generator.
ARG BASE_IMAGE=astra-candidate-runtime:latest
FROM ${BASE_IMAGE}

USER root
ARG CODEX_VERSION=0.144.6
ARG CLAUDE_CODE_VERSION=latest
RUN npm install --global --ignore-scripts --no-audit --no-fund "@openai/codex@${CODEX_VERSION}" \
    && npm install --global --no-audit --no-fund "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
    && install -d -m 0700 -o agent -g agent /home/agent/.codex /home/agent/.claude
USER agent
WORKDIR /workspace
