#!/usr/bin/env bash
# One-click start of the whole stack (infra + Go API + Python agent + web) in Docker.
#   ./scripts/start.sh            start (creates .env on first run)
#   ./scripts/stop.sh             stop (data is kept)
set -euo pipefail
cd "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

command -v docker >/dev/null || { echo "Docker is required: https://www.docker.com/products/docker-desktop" >&2; exit 1; }
docker info >/dev/null 2>&1 || { echo "Docker is not running. Start Docker Desktop and retry." >&2; exit 1; }

rand() { python3 -c 'import secrets; print(secrets.token_urlsafe(18).replace("-","x").replace("_","y"))' 2>/dev/null || openssl rand -hex 12; }
setenv() { # setenv KEY VALUE  (in-place in .env)
  if grep -q "^$1=" .env; then sed -i.bak "s|^$1=.*|$1=$2|" .env && rm -f .env.bak; else echo "$1=$2" >> .env; fi
}
getenv() { { grep "^$1=" .env || true; } | head -1 | cut -d= -f2-; }

if [[ ! -f .env ]]; then
  cp .env.example .env
  for k in DB_PASSWORD MYSQL_ROOT_PASSWORD REDIS_PASSWORD MINIO_SECRET_KEY QDRANT_API_KEY INTERNAL_TOKEN; do setenv "$k" "$(rand)"; done
  # Reasoning models are slow; the stock 120s agent budget often runs out on the first try.
  setenv AGENT_MAX_DURATION_MS 300000
  setenv AGENT_MAX_ESTIMATED_TOKENS 100000
  # Follow-up questions rerun the agent loop synchronously; keep the HTTP wait above the agent budget.
  setenv AI_INTERACTIVE_TIMEOUT_MS 330000
  echo "Created .env with random passwords."
fi

key="$(getenv SILICONFLOW_API_KEY)"
if [[ -z "$key" ]]; then
  key="${SILICONFLOW_API_KEY:-}"
  if [[ -z "$key" ]]; then
    read -r -s -p "SiliconFlow API key (https://cloud.siliconflow.cn): " key; echo
  fi
  [[ -n "$key" ]] || { echo "SILICONFLOW_API_KEY is required." >&2; exit 1; }
  setenv SILICONFLOW_API_KEY "$key"
fi

# Pick a free host port for each published service (only the web port matters to you).
# Ports already published by this project's own containers count as free, so re-running keeps the same ports.
project="$(basename "$PWD" | tr '[:upper:]' '[:lower:]' | tr -c 'a-z0-9_-\n' '-')"
own_ports=" $(docker ps --filter "label=com.docker.compose.project=${project}" --format '{{.Ports}}' \
  | { grep -oE '127\.0\.0\.1:[0-9]+(-[0-9]+)?' || true; } | cut -d: -f2 \
  | awk -F- '{ last = ($2 == "" ? $1 : $2); for (p = $1; p <= last; p++) printf "%s ", p }') "
taken=" "
port_busy() {
  [[ "$taken" == *" $1 "* ]] && return 0
  [[ "$own_ports" == *" $1 "* ]] && return 1
  lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
}
pick() { # pick VAR DEFAULT  (honours a value already exported or saved in .env)
  local saved; saved="$(getenv "$1")"
  local p="${!1:-${saved:-$2}}"
  while port_busy "$p"; do p=$((p+1)); done
  taken+="$p "; export "$1=$p"; setenv "$1" "$p"
}
pick WEB_PORT 8080
pick MYSQL_HOST_PORT 3307; pick REDIS_HOST_PORT 6379; pick QDRANT_HOST_PORT 6333
pick MINIO_HOST_PORT 9000; pick MINIO_CONSOLE_HOST_PORT 9001
pick KAFKA_HOST_PORT 9094
pick JAEGER_UI_HOST_PORT 16686; pick JAEGER_OTLP_HOST_PORT 4317; pick PROMETHEUS_HOST_PORT 19090

export CORS_ALLOWED_ORIGINS="http://localhost:${WEB_PORT},http://127.0.0.1:${WEB_PORT}"

docker compose --env-file .env --profile app up -d --build --wait --wait-timeout 900

url="http://localhost:${WEB_PORT}"
echo
echo "Vparser is ready:  $url"
echo "Traces (Jaeger):     http://localhost:${JAEGER_UI_HOST_PORT}"
echo "Metrics (Prometheus): http://localhost:${PROMETHEUS_HOST_PORT}"
echo "Register an account, upload a video (docs/samples/binary-tree-demo.mp4), then start an analysis."
if command -v open >/dev/null; then open "$url"
elif command -v xdg-open >/dev/null; then xdg-open "$url"
fi
