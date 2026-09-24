#!/usr/bin/env bash
# Stop the whole stack. Data (mysql/redis/minio/qdrant dirs, kafka volume) is kept.
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
docker compose --env-file .env --profile app down
