# The verifier needs the same Python/Playwright/websocket dependencies as the
# legacy HTTP and UI engines, but it receives the candidate only read-only.
ARG BASE_IMAGE=astra-candidate-runtime:latest
FROM ${BASE_IMAGE}

USER root
RUN python3 -m pip install --no-cache-dir --break-system-packages \
      playwright==1.61.0 \
      openapi-spec-validator==0.7.2 \
      PyYAML==6.0.3
COPY environment/verifier/entrypoint.sh /usr/local/bin/astra-verify
RUN chmod 0555 /usr/local/bin/astra-verify
ENTRYPOINT ["/usr/local/bin/astra-verify"]
