# Environment Contract

## Purpose

The shared image gives the coding agent the infrastructure and development tools needed to build the task. It does not include an application implementation, domain seed data, private evaluation assets, or expected outputs.

The agent can reach public package registries at runtime. The platform may apply its own registry proxy or allowlist. The bundled map remains local, so map rendering does not depend on a third-party map service.

## Installed Tooling

The image supports Linux AMD64 and ARM64. Its pinned base-image manifests publish both architectures. The following versions were validated on ARM64, and the Dockerfile resolves successfully for both platforms:

| Tool | Version |
|---|---|
| Go | `1.25.12` |
| Node.js / npm | `22.23.1` / `10.9.8` |
| PostgreSQL | `16.14` |
| MongoDB / mongosh | `8.0.26` / `2.9.2` |
| Redis | `7.0.15` |
| Python / pytest | `3.12.3` / `9.1.1` |
| Playwright | `1.61.1`, with bundled Chromium |
| Docker CLI / Compose | `29.1.3` / `2.40.3` |

The image also includes Git, curl, jq, certificates, compilers, archive tools, process utilities, and the pinned Python packages used by the environment self-tests. Base images and major toolchains are pinned in `environment/Dockerfile`. Versions supplied by Ubuntu packages are the versions reported by the validated build.

The working directory is an initially empty `/workspace`, writable by the non-root `agent` user with UID and GID `1000`.

## Package Registries and Reference Manifests

Go and npm use the registry configuration supplied by the runtime. The image does not set offline package-manager flags. The agent may download dependencies needed for its implementation.

The image exposes public reference manifests at:

- `/opt/arena/dependencies/backend/go.mod`
- `/opt/arena/dependencies/frontend/package.json`

These files document a tested dependency set. They are not an application scaffold or an offline-cache guarantee.

## Available Services

The container entrypoint starts PostgreSQL, MongoDB, and Redis and waits for all three services to become ready. It then runs the container command, which defaults to `sleep infinity`. The Docker health check continues to verify the services while the container is running.

The startup script is idempotent and can also be run manually:

```bash
bash /opt/arena/bin/start-services.sh
```

It starts and checks:

| Service | Address | Agent environment variable |
|---|---|---|
| PostgreSQL | `127.0.0.1:5432` | `DATABASE_URL` |
| MongoDB | `127.0.0.1:27017` | `MONGODB_URI` |
| Redis | `127.0.0.1:6379` | `REDIS_URL` |

The local database names and credentials are image configuration, not production secrets. They are supplied through environment variables so the agent does not need to discover them.

Repeated calls keep existing service data and do not create duplicate service processes.

The image also provides:

```bash
/opt/arena/bin/healthcheck.sh
/opt/arena/bin/reset-services.sh
```

`healthcheck.sh` exits with zero only when all three services accept a real operation. It retries for up to 30 seconds; a listening port alone is not sufficient. `reset-services.sh` clears PostgreSQL, MongoDB, and Redis task data, leaves the services healthy, and preserves files under `/workspace`.

## Browser and Local Map

Playwright and Chromium are installed in the image. `PLAYWRIGHT_BROWSERS_PATH` points to `/ms-playwright`, so the agent can run browser tests through `run_command`, for example:

```bash
npx playwright test
```

The public Bangalore basemap is installed at `/opt/arena/map-assets`. A frontend can expose that directory at `/map-assets` and load `/map-assets/style.json` with MapLibre GL JS and the PMTiles protocol.

| Map property | Value |
|---|---|
| Bounds | `77.45,12.80,77.85,13.20` |
| Center | `77.5946,12.9716` |
| Zoom range | `0` to `14` |
| Data | OpenStreetMap contributors, ODbL 1.0 |

The UI must display `© OpenStreetMap contributors`. The PMTiles archive is a visualization basemap, not a routing engine. Application routing and serviceability must be calculated from operational nodes, coordinates, capacity, time windows, and task rules. The local archive makes development reproducible and does not imply that the simplified basemap provides a complete road graph or live traffic data.

## Process Model

The gym starts one agent container from `environment/Dockerfile`. The image includes the Docker CLI and Docker Compose, but it does not run a Docker daemon and must not be given the host Docker socket. The agent can author and statically validate a Compose definition with `docker compose config`.

PostgreSQL, MongoDB, and Redis run directly inside the gym container and start automatically. Application processes can run directly from `/workspace` and use these bundled services. Running `docker compose up` inside this container requires a Docker-enabled outer runtime; the default gym container cannot launch nested containers.

The task defines a small application contract for building, starting, resetting, and describing the completed application. Internal architecture, API paths, payload shapes, UI structure, realtime transport, and datastore responsibilities remain implementation choices. The supplied service scripts manage PostgreSQL, MongoDB, and Redis; the submitted application manages its own build, startup, reset, migrations, and initial administrator setup.

## Isolation

- Run the agent as a non-root user.
- Grant only the limited service-start permissions required by the setup scripts.
- Give the agent the task instruction, this environment documentation, development tools, reference dependency manifests, package registries, and public map assets.
- Do not place private evaluation logic, private fixtures, expected results, credentials, reference implementations, or model traces in the image.
- Do not expose database ports outside the container unless the evaluation runner requires it.
- Do not mount the host Docker socket. The Docker CLI and Compose plugin are present only so the agent can author and validate a Compose file with `docker compose config`.

## Validation

### Image build

```bash
docker build \
  -t lab-sample-pickup-delivery-gym:local \
  -f environment/Dockerfile .
```

The base images are pinned by digest. Add `--pull=false` when intentionally checking a rebuild against locally cached base images. If your Docker installation uses a non-default context, pass it with `--context` or set `DOCKER_CONTEXT` for the smoke suite.

### Complete smoke suite

```bash
IMAGE_TAG=lab-sample-pickup-delivery-gym:local \
environment/test/smoke.sh
```

The runner starts a container with bridge networking, 4 CPUs, an 8 GB memory limit, and no host Docker socket. It verifies identity, tools, contracts, registry downloads, fresh Go compilation, fresh Vue production build, local browser map rendering, OSM attribution, service health, idempotent startup, datastore reset, workspace preservation, and cleanup. The browser test rejects all requests outside its local Vite origin even while package-manager registry access is available.

The validation files under `environment/test` are not copied into the image. The smoke runner mounts them read-only at `/opt/arena/test` for the duration of the test container.
