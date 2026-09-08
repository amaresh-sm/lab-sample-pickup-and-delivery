# syntax=docker/dockerfile:1.7

FROM golang:1.25.12-bookworm@sha256:ea341baa9bd5ba6784f6d7161ace70544349a6242d54d34a0fbfd2c4d51c9d58 AS go-toolchain
FROM node:22.23.1-bookworm@sha256:5647be709086c696ff32edaaf1c70cd26d1da6ab2b39c32f3c7b4c4a31957e37 AS node-toolchain
FROM mcr.microsoft.com/playwright:v1.61.1-noble@sha256:5b8f294aff9041b7191c34a4bab3ac270157a28774d4b0660e9743297b697e48 AS browser-toolchain
FROM mongo:8.0.26-noble@sha256:b49841837cd7688885d7479d14a71733bacae4c99faaae615622384eaee045a0 AS runtime

USER root

ARG DEBIAN_FRONTEND=noninteractive

RUN printf '#!/bin/sh\nexit 101\n' > /usr/sbin/policy-rc.d \
    && chmod 0755 /usr/sbin/policy-rc.d \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        build-essential \
        ca-certificates \
        curl \
        docker-compose-v2 \
        docker.io \
        git \
        jq \
        netcat-openbsd \
        postgresql \
        postgresql-client \
        procps \
        psmisc \
        python3 \
        python3-pip \
        python3-venv \
        redis-server \
        redis-tools \
        sudo \
        tini \
        unzip \
        xz-utils \
    && rm -f /usr/sbin/policy-rc.d \
    && rm -rf /var/lib/apt/lists/*

COPY --from=go-toolchain /usr/local/go/ /usr/local/go/
COPY --from=node-toolchain /usr/local/ /usr/local/
COPY --from=browser-toolchain /ms-playwright/ /ms-playwright/

ENV PATH="/opt/arena/venv/bin:/usr/local/go/bin:/usr/local/bin:${PATH}" \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1

RUN npm install --global --ignore-scripts --no-audit --no-fund @playwright/test@1.61.1 \
    && playwright install-deps chromium \
    && rm -rf /var/lib/apt/lists/*

RUN python3 -m venv /opt/arena/venv \
    && /opt/arena/venv/bin/pip install --no-cache-dir \
        pytest==9.1.1 \
        requests==2.34.2 \
        websockets==16.1 \
        psutil==7.2.2

COPY environment/dependencies/ /opt/arena/dependencies/

RUN existing_group="$(getent group 1000 | cut -d: -f1)" \
    && if [ -n "$existing_group" ]; then \
        groupmod --new-name agent "$existing_group"; \
    else \
        groupadd --gid 1000 agent; \
    fi \
    && existing_user="$(getent passwd 1000 | cut -d: -f1)" \
    && if [ -n "$existing_user" ]; then \
        usermod --login agent --home /home/agent --move-home "$existing_user"; \
    else \
        useradd --uid 1000 --gid 1000 --create-home --shell /bin/bash agent; \
    fi \
    && usermod --groups '' agent \
    && install -d -m 0755 /opt/arena/bin /opt/arena/map-assets \
    && install -d -m 0755 /var/lib/arena /var/log/arena /run/arena \
    && install -d -m 0750 -o mongodb -g mongodb /var/lib/arena/mongodb \
    && install -d -m 0750 -o redis -g redis /var/lib/arena/redis \
    && install -d -m 0755 -o mongodb -g mongodb /var/log/arena/mongodb /run/arena/mongodb \
    && install -d -m 0755 -o redis -g redis /var/log/arena/redis /run/arena/redis \
    && install -d -m 0755 -o agent -g agent /workspace /home/agent/.cache/go-build \
    && printf '%s\n' \
        'agent ALL=(root) NOPASSWD: /opt/arena/bin/start-services.sh, /opt/arena/bin/reset-services.sh' \
        > /etc/sudoers.d/arena-agent \
    && chmod 0440 /etc/sudoers.d/arena-agent \
    && visudo -cf /etc/sudoers.d/arena-agent

COPY --chown=root:root environment/assets/map/ /opt/arena/map-assets/
COPY --chown=root:root environment/bin/ /opt/arena/bin/

RUN chmod -R a+rX /opt/arena/map-assets \
    && chmod 0755 \
        /opt/arena/bin/entrypoint.sh \
        /opt/arena/bin/start-services.sh \
        /opt/arena/bin/healthcheck.sh \
        /opt/arena/bin/reset-services.sh

ENV HOME=/home/agent \
    XDG_CACHE_HOME=/home/agent/.cache \
    GOCACHE=/home/agent/.cache/go-build \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    DATABASE_URL="postgresql://arena:arena@127.0.0.1:5432/arena?sslmode=disable" \
    MONGODB_URI="mongodb://127.0.0.1:27017/arena" \
    REDIS_URL="redis://127.0.0.1:6379/0" \
    OFFLINE_MAP_ASSET_DIR=/opt/arena/map-assets \
    OFFLINE_MAP_BOUNDS="77.45,12.80,77.85,13.20" \
    OFFLINE_MAP_CENTER="77.5946,12.9716"

WORKDIR /workspace

USER agent

HEALTHCHECK --interval=10s --timeout=5s --start-period=90s --retries=3 \
    CMD ["/opt/arena/bin/healthcheck.sh"]

ENTRYPOINT ["/usr/bin/tini", "--", "/opt/arena/bin/entrypoint.sh"]
CMD ["sleep", "infinity"]
