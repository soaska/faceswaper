#!/usr/bin/env bash
set -Eeuo pipefail

readonly remote_host=${1:-}
readonly remote_target=${2:-/opt/faceswaper-codex-test}

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

ssh -o BatchMode=yes -o ConnectTimeout=15 "$remote_host" bash -s -- "$remote_target" <<'REMOTE'
set -Eeuo pipefail
umask 077

readonly target=$1
readonly api=http://127.0.0.1:18080
cd -- "$target"

if docker compose version >/dev/null 2>&1; then
  compose=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  compose=(docker-compose)
else
  echo "Не найден Docker Compose" >&2
  exit 1
fi

command -v curl >/dev/null
command -v jq >/dev/null
set -a
source .env.codex-generated
set +a
compose_files=(-f compose.yaml -f compose.test.yaml)

user_id=
circle_job=
face_job=
token=

cleanup() {
  set +e
  if [[ -n "$token" && -n "$user_id" ]]; then
    operations=$(curl --silent \
      -H "Authorization: Bearer $token" \
      "$api/api/collections/user_operations/records?filter=user%3D%22${user_id}%22&perPage=100")
    while IFS= read -r operation_id; do
      [[ -n "$operation_id" ]] && curl --silent --output /dev/null \
        -X DELETE -H "Authorization: Bearer $token" \
        "$api/api/collections/user_operations/records/$operation_id"
    done < <(printf '%s' "$operations" | jq -r '.items[]?.id')
    for record in \
      "circle_jobs:$circle_job" \
      "face_jobs:$face_job"; do
      collection=${record%%:*}
      record_id=${record#*:}
      [[ -n "$record_id" ]] && curl --silent --output /dev/null \
        -X DELETE -H "Authorization: Bearer $token" \
        "$api/api/collections/$collection/records/$record_id"
    done
    curl --silent --output /dev/null -X DELETE \
      -H "Authorization: Bearer $token" \
      "$api/api/collections/users/records/$user_id"
  fi
  "${compose[@]}" "${compose_files[@]}" up -d job-manager </dev/null >/dev/null
}
trap cleanup EXIT

"${compose[@]}" "${compose_files[@]}" stop job-manager </dev/null >/dev/null

pb_container=$("${compose[@]}" "${compose_files[@]}" ps -q pocketbase)
auth_response=$(docker exec "$pb_container" sh -ceu '
  payload=$(printf "{\"identity\":\"%s\",\"password\":\"%s\"}" "$PB_ADMIN_EMAIL" "$PB_ADMIN_PASSWORD")
  curl --fail --silent -H "Content-Type: application/json" -d "$payload" \
    http://127.0.0.1:8080/api/admins/auth-with-password
')
token=$(printf '%s' "$auth_response" | jq -er '.token')

auth=(-H "Authorization: Bearer $token")
json=(-H "Content-Type: application/json")

user_response=$(curl --fail --silent "${auth[@]}" "${json[@]}" \
  -d '{"tgid":9000000001,"username":"settlement-smoke","circle_count":0,"face_replace_count":0,"coins":200}' \
  "$api/api/collections/users/records")
user_id=$(printf '%s' "$user_response" | jq -er '.id')

circle_response=$(curl --fail --silent "${auth[@]}" \
  -F "owner=$user_id" -F 'status=queued' -F 'request_key=smoke:circle' \
  -F 'input_media=@/etc/hosts;filename=input.mp4' \
  "$api/api/collections/circle_jobs/records")
circle_job=$(printf '%s' "$circle_response" | jq -er '.id')

claim=$(jq -nc --arg collection circle_jobs --arg worker settlement-smoke \
  '{collection:$collection,worker_id:$worker}')
curl --fail --silent "${auth[@]}" "${json[@]}" -d "$claim" \
  "$api/api/faceswaper/jobs/claim" | jq -e --arg id "$circle_job" '.task.id == $id' >/dev/null

start_circle=$(jq -nc --arg id "$circle_job" \
  '{collection:"circle_jobs",task_id:$id,worker_id:"settlement-smoke",action:"start_sending",price:1}')
for _ in 1 2; do
  curl --fail --silent "${auth[@]}" "${json[@]}" -d "$start_circle" \
    "$api/api/faceswaper/jobs/settle" | jq -e '.ok and .price == 1 and .balance == 199' >/dev/null
done

fail_circle=$(jq -nc --arg id "$circle_job" \
  '{collection:"circle_jobs",task_id:$id,worker_id:"settlement-smoke",action:"fail",error:"тестовая ошибка доставки"}')
for _ in 1 2; do
  curl --fail --silent "${auth[@]}" "${json[@]}" -d "$fail_circle" \
    "$api/api/faceswaper/jobs/settle" | jq -e '.ok' >/dev/null
done

curl --fail --silent "${auth[@]}" "$api/api/collections/users/records/$user_id" \
  | jq -e '.coins == 200 and .circle_count == 0 and .face_replace_count == 0' >/dev/null
curl --fail --silent "${auth[@]}" "$api/api/collections/circle_jobs/records/$circle_job" \
  | jq -e '.status == "error: тестовая ошибка доставки" and .claimed_by == ""' >/dev/null

face_response=$(curl --fail --silent "${auth[@]}" \
  -F "owner=$user_id" -F 'status=queued' -F 'request_key=smoke:face' \
  -F 'input_media=@/etc/hosts;filename=target.jpg' \
  -F 'input_face=@/etc/hosts;filename=source.jpg' \
  "$api/api/collections/face_jobs/records")
face_job=$(printf '%s' "$face_response" | jq -er '.id')

claim=$(jq -nc --arg collection face_jobs --arg worker settlement-smoke \
  '{collection:$collection,worker_id:$worker}')
curl --fail --silent "${auth[@]}" "${json[@]}" -d "$claim" \
  "$api/api/faceswaper/jobs/claim" | jq -e --arg id "$face_job" '.task.id == $id' >/dev/null

start_face=$(jq -nc --arg id "$face_job" \
  '{collection:"face_jobs",task_id:$id,worker_id:"settlement-smoke",action:"start_sending",price:3,duration:12,threads:1}')
for _ in 1 2; do
  curl --fail --silent "${auth[@]}" "${json[@]}" -d "$start_face" \
    "$api/api/faceswaper/jobs/settle" | jq -e '.ok and .price == 3 and .balance == 197' >/dev/null
done

complete_face=$(jq -nc --arg id "$face_job" \
  '{collection:"face_jobs",task_id:$id,worker_id:"settlement-smoke",action:"complete"}')
for _ in 1 2; do
  curl --fail --silent "${auth[@]}" "${json[@]}" -d "$complete_face" \
    "$api/api/faceswaper/jobs/settle" | jq -e '.ok' >/dev/null
done

curl --fail --silent "${auth[@]}" "$api/api/collections/users/records/$user_id" \
  | jq -e '.coins == 197 and .circle_count == 0 and .face_replace_count == 1' >/dev/null
curl --fail --silent "${auth[@]}" "$api/api/collections/face_jobs/records/$face_job" \
  | jq -e '.status == "completed" and .price == 3 and .duration == 12 and .threads == 1 and .claimed_by == ""' >/dev/null

echo "Atomic settlement smoke-test пройден: charge/retry/refund/complete/counter"
REMOTE
