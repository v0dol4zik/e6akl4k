#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$PROJECT_DIR/docker-compose.yml"
TARGET_IMAGE="${1:-${MUSICBOT_IMAGE:-}}"
SUDO=""

if (( EUID != 0 )); then
  command -v sudo >/dev/null 2>&1 || { echo "Нужны root или sudo." >&2; exit 1; }
  SUDO="sudo"
fi

for command in docker sqlite3; do
  command -v "$command" >/dev/null 2>&1 || { echo "$command не найден. Сначала запусти ./bootstrap.sh" >&2; exit 1; }
done
$SUDO docker compose version >/dev/null
for path in "$PROJECT_DIR/.env" "$PROJECT_DIR/cookies.txt" "$PROJECT_DIR/downloads" "$PROJECT_DIR/cache"; do
  [[ -e "$path" ]] || { echo "Не найден $path. Сначала запусти ./bootstrap.sh" >&2; exit 1; }
done

compose_image() {
  if [[ -n "$SUDO" ]]; then
    $SUDO env MUSICBOT_IMAGE="$1" docker compose --project-directory "$PROJECT_DIR" -f "$COMPOSE_FILE" "${@:2}"
  else
    MUSICBOT_IMAGE="$1" docker compose --project-directory "$PROJECT_DIR" -f "$COMPOSE_FILE" "${@:2}"
  fi
}

if [[ -z "$TARGET_IMAGE" ]]; then
  version="$(git -C "$PROJECT_DIR" rev-parse --short=12 HEAD 2>/dev/null || date -u +%Y%m%d%H%M%S)"
  if ! git -C "$PROJECT_DIR" diff --quiet --ignore-submodules -- 2>/dev/null || [[ -n "$(git -C "$PROJECT_DIR" ls-files --others --exclude-standard 2>/dev/null)" ]]; then
    version="$version-dirty-$(date -u +%Y%m%d%H%M%S)"
  fi
  TARGET_IMAGE="musicbot:$version"
  echo "Собираю локальный образ $TARGET_IMAGE"
  compose_image "$TARGET_IMAGE" build --pull
else
  if ! $SUDO docker image inspect "$TARGET_IMAGE" >/dev/null 2>&1; then
    echo "Получаю образ $TARGET_IMAGE"
    $SUDO docker pull "$TARGET_IMAGE"
  fi
fi

compose_image "$TARGET_IMAGE" config --quiet
available_kb="$(df -Pk "$PROJECT_DIR" | awk 'NR==2 {print $4}')"
(( available_kb >= 524288 )) || { echo "Для безопасного деплоя нужно минимум 512 MiB свободного места." >&2; exit 1; }

database_path="$PROJECT_DIR/cache/musicbot.db"
backup_dir="$PROJECT_DIR/backups"
state_dir="$PROJECT_DIR/.deploy"
$SUDO install -d -m 0700 "$backup_dir" "$state_dir"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
if $SUDO test -f "$database_path"; then
  $SUDO sqlite3 "$database_path" "PRAGMA quick_check" | grep -qx ok || { echo "SQLite quick_check не прошёл." >&2; exit 1; }
  $SUDO sqlite3 "$database_path" ".backup '$backup_dir/musicbot-$timestamp.db'"
  $SUDO chmod 0600 "$backup_dir/musicbot-$timestamp.db"
fi

previous_image=""
if $SUDO test -f "$state_dir/current-image"; then
  previous_image="$($SUDO cat "$state_dir/current-image")"
else
  container_id="$(compose_image "$TARGET_IMAGE" ps -q music_bot 2>/dev/null || true)"
  if [[ -n "$container_id" ]]; then
    previous_image="$($SUDO docker inspect --format '{{.Config.Image}}' "$container_id")"
  fi
fi

rollback() {
  [[ -n "$previous_image" ]] || { echo "Предыдущий образ неизвестен; rollback невозможен." >&2; return; }
  echo "Возвращаю $previous_image" >&2
  compose_image "$previous_image" up -d --no-build --remove-orphans || true
}
trap 'echo "Ошибка deploy.sh на строке $LINENO" >&2; rollback' ERR

compose_image "$TARGET_IMAGE" up -d --no-build --remove-orphans
container_id="$(compose_image "$TARGET_IMAGE" ps -q music_bot)"
[[ -n "$container_id" ]] || { echo "Контейнер не создан." >&2; exit 1; }

health="starting"
for _ in $(seq 1 30); do
  running="$($SUDO docker inspect --format '{{.State.Running}}' "$container_id")"
  health="$($SUDO docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container_id")"
  [[ "$running" == true ]] || { echo "Контейнер остановился." >&2; exit 1; }
  [[ "$health" == healthy ]] && break
  [[ "$health" == unhealthy ]] && { echo "Healthcheck завершился ошибкой." >&2; exit 1; }
  sleep 2
done
[[ "$health" == healthy ]] || { echo "Healthcheck не стал healthy: $health" >&2; exit 1; }

trap - ERR
if [[ -n "$previous_image" && "$previous_image" != "$TARGET_IMAGE" ]]; then
  printf '%s\n' "$previous_image" | $SUDO tee "$state_dir/previous-image" >/dev/null
fi
printf '%s\n' "$TARGET_IMAGE" | $SUDO tee "$state_dir/current-image" >/dev/null
$SUDO chmod 0600 "$state_dir"/*-image 2>/dev/null || true
compose_image "$TARGET_IMAGE" ps
echo "Развёрнут неизменяемый образ: $TARGET_IMAGE"
echo "Rollback: ./rollback.sh"
