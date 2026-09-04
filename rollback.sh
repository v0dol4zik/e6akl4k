#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
SUDO=""
if (( EUID != 0 )); then
  command -v sudo >/dev/null 2>&1 || { echo "Нужны root или sudo." >&2; exit 1; }
  SUDO="sudo"
fi
state_dir="$PROJECT_DIR/.deploy"
[[ -s "$state_dir/previous-image" ]] || { echo "Нет сохранённого предыдущего образа." >&2; exit 1; }
previous="$($SUDO cat "$state_dir/previous-image")"
current="$($SUDO cat "$state_dir/current-image" 2>/dev/null || true)"
if [[ -n "$SUDO" ]]; then
  $SUDO env MUSICBOT_IMAGE="$previous" docker compose --project-directory "$PROJECT_DIR" -f "$PROJECT_DIR/docker-compose.yml" up -d --no-build --remove-orphans
else
  MUSICBOT_IMAGE="$previous" docker compose --project-directory "$PROJECT_DIR" -f "$PROJECT_DIR/docker-compose.yml" up -d --no-build --remove-orphans
fi
container_id="$(if [[ -n "$SUDO" ]]; then $SUDO env MUSICBOT_IMAGE="$previous" docker compose --project-directory "$PROJECT_DIR" -f "$PROJECT_DIR/docker-compose.yml" ps -q music_bot; else MUSICBOT_IMAGE="$previous" docker compose --project-directory "$PROJECT_DIR" -f "$PROJECT_DIR/docker-compose.yml" ps -q music_bot; fi)"
[[ -n "$container_id" ]] || { echo "Rollback-контейнер не создан." >&2; exit 1; }
health="starting"
for _ in $(seq 1 30); do
  health="$($SUDO docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container_id")"
  [[ "$health" == healthy ]] && break
  [[ "$health" == unhealthy ]] && { echo "Rollback-образ не прошёл healthcheck." >&2; exit 1; }
  sleep 2
done
[[ "$health" == healthy ]] || { echo "Rollback healthcheck не стал healthy: $health" >&2; exit 1; }
printf '%s\n' "$previous" | $SUDO tee "$state_dir/current-image" >/dev/null
if [[ -n "$current" ]]; then printf '%s\n' "$current" | $SUDO tee "$state_dir/previous-image" >/dev/null; fi
echo "Выполнен rollback на $previous"
