#!/usr/bin/env bash
set -Eeuo pipefail

readonly default_target=/opt/faceswaper-codex-test
readonly remote_host=${1:-}
readonly remote_target=${2:-$default_target}

if [[ -z "$remote_host" ]]; then
  echo "Использование: $0 <ssh-host> [директория-в-/opt]" >&2
  exit 2
fi

case "$remote_target" in
  /opt/faceswaper-*) ;;
  *)
    echo "Тестовая директория должна соответствовать /opt/faceswaper-*" >&2
    exit 2
    ;;
esac

readonly script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
readonly project_dir=$(cd -- "$script_dir/.." && pwd)
readonly -a ssh_options=(-o BatchMode=yes -o ConnectTimeout=15)

if [[ ! -f "$project_dir/.env" ]]; then
  echo "Не найден $project_dir/.env" >&2
  exit 1
fi

ssh "${ssh_options[@]}" "$remote_host" mkdir -p -- "$remote_target"
rsync -az --delete --info=stats1 \
  -e "ssh -o BatchMode=yes -o ConnectTimeout=15" \
  --exclude=.git \
  --exclude=.env \
  --exclude=.venv/ \
  --exclude=.kilocode/ \
  --exclude=.DS_Store \
  --exclude=.pytest_cache/ \
  --exclude=__pycache__/ \
  --exclude='*.pyc' \
  --exclude=data/ \
  --exclude=temp/ \
  "$project_dir/" "$remote_host:$remote_target/"
rsync -az -e "ssh -o BatchMode=yes -o ConnectTimeout=15" \
  "$project_dir/.env" "$remote_host:$remote_target/.env"

ssh "${ssh_options[@]}" "$remote_host" bash -s -- "$remote_target" <<'REMOTE'
set -Eeuo pipefail

readonly target=$1
readonly project_name=faceswaper-codex-test
cd -- "$target"

if docker compose version >/dev/null 2>&1; then
  compose=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  compose=(docker-compose)
elif podman compose version >/dev/null 2>&1; then
  compose=(podman compose)
else
  echo "Не найден Docker Compose или Podman Compose" >&2
  exit 1
fi

mkdir -p data/pocketbase data/telegram-bot-api data/face-model-cache
mkdir -p temp/face-media temp/job-manager

readonly fresh_database=$([[ -f data/pocketbase/data.db ]] && echo 0 || echo 1)
readonly generated_env=.env.codex-generated
{
  printf 'COMPOSE_PROJECT_NAME=%s\n' "$project_name"
  printf 'TELEGRAM_API_PORT=18081\n'
  printf 'TELEGRAM_STAT_PORT=18082\n'
  printf 'POCKETBASE_PORT=18080\n'
  printf 'FACE_SWAP_PORT=17860\n'
} >"$generated_env"

if ! grep -Eq '^[[:space:]]*FACE_SWAP_API_KEY[[:space:]]*=[[:space:]]*[^[:space:]]+' .env; then
  if ! command -v openssl >/dev/null 2>&1; then
    echo "FACE_SWAP_API_KEY отсутствует, а openssl недоступен" >&2
    exit 1
  fi
  printf '\nFACE_SWAP_API_KEY=%s\n' "$(openssl rand -hex 32)" >>.env
fi

set -a
# Compose reads both files; the generated file deliberately overrides only
# project identity and loopback ports, never user credentials.
source "$generated_env"
set +a

compose_files=(-f compose.yaml -f compose.test.yaml)
"${compose[@]}" "${compose_files[@]}" up -d --build pocketbase

for attempt in {1..60}; do
  if "${compose[@]}" "${compose_files[@]}" exec -T pocketbase \
    curl --fail --silent http://127.0.0.1:8080/api/health >/dev/null; then
    break
  fi
  if [[ $attempt -eq 60 ]]; then
    echo "PocketBase не прошёл healthcheck" >&2
    exit 1
  fi
  sleep 2
done

if [[ $fresh_database -eq 1 ]]; then
  "${compose[@]}" "${compose_files[@]}" exec -T pocketbase sh -ceu \
    'exec /pb/pocketbase admin create "$PB_ADMIN_EMAIL" "$PB_ADMIN_PASSWORD" --dir=/pb/pb_data'
fi

# The Telegram bot is intentionally absent. See the telegram-e2e profile in
# compose.test.yaml before starting it against any real token.
"${compose[@]}" "${compose_files[@]}" up -d --build \
  telegram-bot-api face-swap-component job-manager

for attempt in {1..180}; do
  if curl --fail --silent http://127.0.0.1:17860/health >/dev/null; then
    break
  fi
  if [[ $attempt -eq 180 ]]; then
    echo "Face Swap Component не загрузил модель и CUDA runtime" >&2
    "${compose[@]}" "${compose_files[@]}" logs --tail=200 face-swap-component
    exit 1
  fi
  sleep 5
done

curl --fail --silent http://127.0.0.1:18080/api/health >/dev/null
curl --fail --silent http://127.0.0.1:17860/health
printf '\n'
"${compose[@]}" "${compose_files[@]}" ps
REMOTE
