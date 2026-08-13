#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$PROJECT_DIR/docker-compose.yml"
APP_UID=10001
APP_GID=10001
OWNER_UID="${SUDO_UID:-$(id -u)}"
OWNER_GID="${SUDO_GID:-$(id -g)}"
INPUT_BOT_TOKEN="${BOT_TOKEN:-}"
SUDO=""

unset BOT_TOKEN COMPOSE_PROJECT_NAME

if (( EUID != 0 )); then
  if ! command -v sudo >/dev/null 2>&1; then
    echo "Нужны права root или установленный sudo." >&2
    exit 1
  fi
  SUDO="sudo"
fi

on_error() {
  echo "Ошибка deploy.sh на строке $1." >&2
}
trap 'on_error $LINENO' ERR

if [[ ! -r /etc/os-release ]]; then
  echo "Не удалось определить ОС. Скрипт поддерживает Debian и Ubuntu с systemd." >&2
  exit 1
fi
. /etc/os-release
case "${ID:-}" in
  debian|ubuntu) ;;
  *)
    echo "Неподдерживаемая ОС: ${ID:-unknown}. Нужен Debian или Ubuntu." >&2
    exit 1
    ;;
esac
if [[ -z "${VERSION_CODENAME:-}" ]] || ! command -v systemctl >/dev/null 2>&1; then
  echo "Нужен поддерживаемый выпуск Debian/Ubuntu с systemd." >&2
  exit 1
fi

env_file="$PROJECT_DIR/.env"
existing_token=""
if [[ -f "$env_file" ]]; then
  existing_token="$($SUDO grep '^BOT_TOKEN=' "$env_file" | tail -n1 | cut -d= -f2- || true)"
  existing_token="${existing_token%$'\r'}"
  existing_token="${existing_token#\"}"
  existing_token="${existing_token%\"}"
  existing_token="${existing_token#\'}"
  existing_token="${existing_token%\'}"
fi

token="$existing_token"
if [[ -z "$token" ]]; then
  token="$INPUT_BOT_TOKEN"
fi
INPUT_BOT_TOKEN=""
if [[ -z "$token" ]]; then
  if [[ ! -t 0 ]]; then
    echo "Создай $env_file с BOT_TOKEN или передай BOT_TOKEN через окружение." >&2
    exit 1
  fi
  read -r -s -p "BOT_TOKEN от @BotFather: " token
  echo
fi
if [[ ! "$token" =~ ^[0-9]{6,}:[A-Za-z0-9_-]{20,}$ ]]; then
  echo "BOT_TOKEN не похож на токен Telegram." >&2
  exit 1
fi

if [[ -z "$existing_token" ]]; then
  if [[ -f "$env_file" ]]; then
    printf '\nBOT_TOKEN=%s\n' "$token" | $SUDO tee -a "$env_file" >/dev/null
  else
    temporary_env="$(mktemp)"
    printf 'BOT_TOKEN=%s\n' "$token" > "$temporary_env"
    $SUDO install -o "$OWNER_UID" -g "$OWNER_GID" -m 0600 "$temporary_env" "$env_file"
    rm -f "$temporary_env"
  fi
fi
token=""
$SUDO chown "$OWNER_UID:$OWNER_GID" "$env_file"
$SUDO chmod 0600 "$env_file"

$SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get update
$SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y ca-certificates curl sqlite3

harden_ssh_if_safe() {
  local sshd_bin="/usr/sbin/sshd"
  local ssh_config_dir="/etc/ssh/sshd_config.d"
  local ssh_config="$ssh_config_dir/00-key-only.conf"
  local login_user="${SUDO_USER:-$(id -un)}"
  local login_home=""
  local authorized_keys=""
  local candidate=""
  local previous=""
  local had_previous=false
  local effective=""

  if ! $SUDO test -x "$sshd_bin" || ! $SUDO test -d "$ssh_config_dir"; then
    echo "SSH hardening пропущен: OpenSSH server или sshd_config.d не найдены."
    return
  fi
  login_home="$(getent passwd "$login_user" | cut -d: -f6)"
  authorized_keys="$login_home/.ssh/authorized_keys"
  if [[ -z "$login_home" ]] || ! $SUDO test -s "$authorized_keys"; then
    echo "SSH hardening пропущен: у $login_user нет authorized_keys; парольный вход не отключаю, чтобы не потерять доступ."
    return
  fi

  candidate="$(mktemp)"
  previous="$(mktemp)"
  printf '%s\n' \
    'PasswordAuthentication no' \
    'KbdInteractiveAuthentication no' \
    'ChallengeResponseAuthentication no' \
    'PubkeyAuthentication yes' > "$candidate"
  if $SUDO test -f "$ssh_config"; then
    $SUDO cp "$ssh_config" "$previous"
    had_previous=true
  fi
  $SUDO install -o root -g root -m 0644 "$candidate" "$ssh_config"
  rm -f "$candidate"

  effective="$($SUDO "$sshd_bin" -T 2>/dev/null || true)"
  if ! $SUDO "$sshd_bin" -t \
    || ! grep -qx 'passwordauthentication no' <<< "$effective" \
    || ! grep -qx 'kbdinteractiveauthentication no' <<< "$effective" \
    || ! grep -qx 'pubkeyauthentication yes' <<< "$effective"; then
    if [[ "$had_previous" == true ]]; then
      $SUDO install -o root -g root -m 0644 "$previous" "$ssh_config"
    else
      $SUDO rm -f "$ssh_config"
    fi
    rm -f "$previous"
    echo "SSH hardening не применён: итоговая конфигурация sshd не прошла проверку." >&2
    return 1
  fi
  rm -f "$previous"

  if ! $SUDO systemctl reload ssh 2>/dev/null; then
    $SUDO systemctl reload sshd
  fi
  echo "SSH защищён: парольный вход отключён, public-key вход включён."
}

harden_ssh_if_safe

install_docker() {
  docker_arch="$(dpkg --print-architecture)"
  key_file="$(mktemp)"
  curl -fsSL "https://download.docker.com/linux/$ID/gpg" -o "$key_file"
  $SUDO install -d -m 0755 /etc/apt/keyrings
  $SUDO install -m 0644 "$key_file" /etc/apt/keyrings/docker.asc
  rm -f "$key_file"
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' \
    "$docker_arch" "$ID" "$VERSION_CODENAME" \
    | $SUDO tee /etc/apt/sources.list.d/docker.list >/dev/null
  $SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get update
  $SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
}

if ! command -v docker >/dev/null 2>&1; then
  install_docker
fi
$SUDO systemctl enable --now docker
if ! $SUDO docker info >/dev/null 2>&1; then
  echo "Docker daemon не запустился." >&2
  exit 1
fi
if ! $SUDO docker compose version >/dev/null 2>&1; then
  if ! $SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y docker-compose-plugin \
    && ! $SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y docker-compose-v2; then
    echo "Не удалось установить Docker Compose v2." >&2
    exit 1
  fi
fi
$SUDO docker compose version >/dev/null

cookies_file="$PROJECT_DIR/cookies.txt"
cookies_source="${COOKIES_FILE:-}"
if [[ -z "$cookies_source" && ! -f "$cookies_file" && -t 0 ]]; then
  read -r -p "Путь к cookies.txt (Enter, чтобы продолжить без cookies): " cookies_source
fi
if [[ -n "$cookies_source" ]]; then
  if [[ ! -f "$cookies_source" ]]; then
    echo "Файл cookies не найден: $cookies_source" >&2
    exit 1
  fi
  if [[ "$(realpath "$cookies_source")" != "$(realpath -m "$cookies_file")" ]]; then
    $SUDO install -m 0600 "$cookies_source" "$cookies_file"
  fi
elif [[ ! -f "$cookies_file" ]]; then
  temporary_cookies="$(mktemp)"
  printf '# Netscape HTTP Cookie File\n' > "$temporary_cookies"
  $SUDO install -m 0600 "$temporary_cookies" "$cookies_file"
  rm -f "$temporary_cookies"
fi

$SUDO mkdir -p "$PROJECT_DIR/downloads" "$PROJECT_DIR/cache"
$SUDO chown -R "$APP_UID:$APP_GID" "$PROJECT_DIR/downloads" "$PROJECT_DIR/cache" "$cookies_file"
$SUDO chmod 0700 "$PROJECT_DIR/downloads" "$PROJECT_DIR/cache"
$SUDO chmod 0600 "$cookies_file"

compose() {
  $SUDO docker compose --project-directory "$PROJECT_DIR" -f "$COMPOSE_FILE" "$@"
}

compose_with_image() {
  if [[ -n "$SUDO" ]]; then
    $SUDO env MUSICBOT_IMAGE="$1" docker compose --project-directory "$PROJECT_DIR" -f "$COMPOSE_FILE" "${@:2}"
  else
    MUSICBOT_IMAGE="$1" docker compose --project-directory "$PROJECT_DIR" -f "$COMPOSE_FILE" "${@:2}"
  fi
}

compose config --quiet
backup_dir="$PROJECT_DIR/backups"
$SUDO mkdir -p "$backup_dir"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
database_path="$PROJECT_DIR/cache/musicbot.db"
database_backup="$backup_dir/musicbot-$timestamp.db"
env_backup="$backup_dir/env-$timestamp"
cookies_backup="$backup_dir/cookies-$timestamp.txt"
backup_files=("$env_backup" "$cookies_backup")
if $SUDO test -f "$database_path"; then
	$SUDO sqlite3 "$database_path" ".backup '$database_backup'"
	backup_files+=("$database_backup")
fi
$SUDO cp --reflink=auto --preserve=mode,timestamps "$env_file" "$env_backup"
$SUDO cp --reflink=auto --preserve=mode,timestamps "$cookies_file" "$cookies_backup"
$SUDO chmod 0600 "${backup_files[@]}"
$SUDO chmod 0700 "$backup_dir"

previous_image="$(compose images -q music_bot 2>/dev/null | head -n1 || true)"
rollback_image=""
if [[ -n "$previous_image" ]]; then
	rollback_image="musicbot:rollback-$timestamp"
	$SUDO docker image tag "$previous_image" "$rollback_image"
fi

rollback() {
	if [[ -z "$rollback_image" ]]; then
		echo "Предыдущий образ отсутствует — автоматический rollback невозможен." >&2
		return
	fi
	echo "Возвращаю предыдущий Docker-образ $rollback_image…" >&2
	compose_with_image "$rollback_image" up -d --no-build --remove-orphans || true
}

if ! compose build; then
	echo "Новый образ не собрался; запущенный контейнер не изменён." >&2
	exit 1
fi
if ! compose up -d --no-build --remove-orphans; then
	rollback
	exit 1
fi
sleep 8
container_id="$(compose ps -q music_bot)"
if [[ -z "$container_id" ]] \
  || [[ "$($SUDO docker inspect --format '{{.State.Running}}' "$container_id")" != "true" ]]; then
  echo "Контейнер не смог стабильно запуститься. Проверь: sudo docker compose -f '$COMPOSE_FILE' logs --tail=100" >&2
	rollback
  exit 1
fi
restart_count="$($SUDO docker inspect --format '{{.RestartCount}}' "$container_id")"
sleep 5
if [[ "$($SUDO docker inspect --format '{{.State.Running}}' "$container_id")" != "true" ]] \
  || (( $($SUDO docker inspect --format '{{.RestartCount}}' "$container_id") > restart_count )); then
  echo "Контейнер перезапускается из-за ошибки. Проверь: sudo docker compose -f '$COMPOSE_FILE' logs --tail=100" >&2
	rollback
  exit 1
fi
health_status="starting"
for _ in $(seq 1 20); do
  health_status="$($SUDO docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$container_id")"
  if [[ "$health_status" == "healthy" || "$health_status" == "none" ]]; then
    break
  fi
  if [[ "$health_status" == "unhealthy" ]]; then
    echo "Контейнер не прошёл healthcheck. Проверь: sudo docker compose -f '$COMPOSE_FILE' logs --tail=100" >&2
	rollback
    exit 1
  fi
  sleep 3
done
if [[ "$health_status" != "healthy" ]]; then
  echo "Healthcheck не успел перейти в healthy (статус: $health_status)." >&2
	rollback
  exit 1
fi
compose ps

echo "Бот развернут. Логи: sudo docker compose -f '$COMPOSE_FILE' logs -f"
echo "Резервные копии: $backup_dir"
