#!/usr/bin/env bash
set -Eeuo pipefail

sudo -- /opt/arena/bin/start-services.sh
exec "$@"
