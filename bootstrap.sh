#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
APP_UID=10001
APP_GID=10001
OWNER_UID="${SUDO_UID:-$(id -u)}"
OWNER_GID="${SUDO_GID:-$(id -g)}"
SUDO=""
if (( EUID != 0 )); then
  command -v sudo >/dev/null 2>&1 || { echo "Нужны root или sudo." >&2; exit 1; }
  SUDO="sudo"
fi

[[ -r /etc/os-release ]] || { echo "Не удалось определить ОС." >&2; exit 1; }
. /etc/os-release
[[ "${ID:-}" == debian || "${ID:-}" == ubuntu ]] || { echo "Поддерживаются Debian и Ubuntu." >&2; exit 1; }
[[ -n "${VERSION_CODENAME:-}" ]] || { echo "В /etc/os-release отсутствует VERSION_CODENAME." >&2; exit 1; }

$SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get update
$SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y ca-certificates curl sqlite3
if ! command -v docker >/dev/null 2>&1; then
  architecture="$(dpkg --print-architecture)"
  key_file="$(mktemp)"
  curl -fsSL "https://download.docker.com/linux/$ID/gpg" -o "$key_file"
  $SUDO install -d -m 0755 /etc/apt/keyrings
  $SUDO install -m 0644 "$key_file" /etc/apt/keyrings/docker.asc
  rm -f "$key_file"
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/%s %s stable\n' "$architecture" "$ID" "$VERSION_CODENAME" | $SUDO tee /etc/apt/sources.list.d/docker.list >/dev/null
  $SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get update
  $SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
fi
$SUDO systemctl enable --now docker
$SUDO docker info >/dev/null
if ! $SUDO docker compose version >/dev/null 2>&1; then
  $SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y docker-compose-plugin \
    || $SUDO env DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=a apt-get install -y docker-compose-v2
fi
$SUDO docker compose version >/dev/null

harden_ssh_if_safe() {
  local login_user="${SUDO_USER:-$(id -un)}"
  local login_home authorized_keys candidate config previous effective
  config="/etc/ssh/sshd_config.d/00-key-only.conf"
  [[ -x /usr/sbin/sshd && -d /etc/ssh/sshd_config.d ]] || { echo "SSH hardening пропущен: sshd_config.d недоступен."; return; }
  login_home="$(getent passwd "$login_user" | cut -d: -f6)"
  authorized_keys="$login_home/.ssh/authorized_keys"
  $SUDO test -s "$authorized_keys" || { echo "SSH hardening пропущен: у $login_user нет authorized_keys."; return; }
  candidate="$(mktemp)"
  previous="$(mktemp)"
  printf '%s\n' 'PasswordAuthentication no' 'KbdInteractiveAuthentication no' 'ChallengeResponseAuthentication no' 'PubkeyAuthentication yes' > "$candidate"
  if $SUDO test -f "$config"; then $SUDO cp "$config" "$previous"; fi
  $SUDO install -o root -g root -m 0644 "$candidate" "$config"
  rm -f "$candidate"
  effective="$($SUDO /usr/sbin/sshd -T 2>/dev/null || true)"
  if ! $SUDO /usr/sbin/sshd -t || ! grep -qx 'passwordauthentication no' <<< "$effective" || ! grep -qx 'pubkeyauthentication yes' <<< "$effective"; then
    if [[ -s "$previous" ]]; then $SUDO install -o root -g root -m 0644 "$previous" "$config"; else $SUDO rm -f "$config"; fi
    rm -f "$previous"
    echo "SSH hardening отменён: итоговая конфигурация не прошла проверку." >&2
    return 1
  fi
  rm -f "$previous"
  $SUDO systemctl reload ssh 2>/dev/null || $SUDO systemctl reload sshd
  echo "SSH переведён на вход по ключу."
}

harden_ssh_if_safe

env_file="$PROJECT_DIR/.env"
if [[ ! -f "$env_file" ]]; then
  token="${BOT_TOKEN:-}"
  if [[ -z "$token" && -t 0 ]]; then
    read -r -s -p "BOT_TOKEN от @BotFather: " token
    echo
  fi
  [[ "$token" =~ ^[0-9]{6,}:[A-Za-z0-9_-]{20,}$ ]] || { echo "Передай корректный BOT_TOKEN." >&2; exit 1; }
  temporary="$(mktemp)"
  printf 'BOT_TOKEN=%s\n' "$token" > "$temporary"
  $SUDO install -o "$OWNER_UID" -g "$OWNER_GID" -m 0600 "$temporary" "$env_file"
  rm -f "$temporary"
fi

cookies_file="$PROJECT_DIR/cookies.txt"
if [[ ! -f "$cookies_file" ]]; then
  if [[ -n "${COOKIES_FILE:-}" ]]; then
    [[ -f "$COOKIES_FILE" ]] || { echo "Не найден COOKIES_FILE=$COOKIES_FILE" >&2; exit 1; }
    $SUDO install -m 0600 "$COOKIES_FILE" "$cookies_file"
  else
    temporary="$(mktemp)"
    printf '# Netscape HTTP Cookie File\n' > "$temporary"
    $SUDO install -m 0600 "$temporary" "$cookies_file"
    rm -f "$temporary"
  fi
fi

$SUDO install -d -o "$APP_UID" -g "$APP_GID" -m 0700 "$PROJECT_DIR/downloads" "$PROJECT_DIR/cache"
$SUDO chown "$APP_UID:$APP_GID" "$cookies_file"
$SUDO chmod 0600 "$env_file" "$cookies_file"
echo "Bootstrap завершён. Теперь запусти ./deploy.sh [registry/image:tag]"
