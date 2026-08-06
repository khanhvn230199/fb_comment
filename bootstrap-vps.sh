#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

REPO_URL="${REPO_URL:-https://github.com/khanhvn230199/fb_comment.git}"
APP_DIR="${APP_DIR:-/root/fb_comment}"
APP_IMAGE_DEFAULT="${APP_IMAGE_DEFAULT:-ghcr.io/khanhvn230199/fb_comment:latest}"

log() {
  printf '\n[bootstrap] %s\n' "$*"
}

warn() {
  printf '\n[bootstrap] WARN: %s\n' "$*" >&2
}

die() {
  printf '\n[bootstrap] ERROR: %s\n' "$*" >&2
  exit 1
}

require_root() {
  if [[ "$(id -u)" -ne 0 ]]; then
    die "Run this script as root on the VPS."
  fi
}

install_dependencies() {
  log "Checking required system packages"
  if ! command -v apt-get >/dev/null 2>&1; then
    die "This script currently expects an Ubuntu/Debian VPS with apt-get."
  fi

  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y ca-certificates curl git openssl

  if ! command -v docker >/dev/null 2>&1; then
    log "Installing Docker Engine"
    curl -fsSL https://get.docker.com | sh
  fi

  if ! docker compose version >/dev/null 2>&1; then
    log "Installing Docker Compose plugin"
    apt-get install -y docker-compose-plugin
  fi

  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now docker
  fi
}

clone_or_update_repo() {
  log "Preparing application directory at ${APP_DIR}"
  mkdir -p "$(dirname "$APP_DIR")"

  if [[ -d "$APP_DIR/.git" ]]; then
    log "Updating existing repository"
    git -C "$APP_DIR" fetch origin main
    git -C "$APP_DIR" reset --hard origin/main
    git -C "$APP_DIR" clean -fd
  elif [[ -d "$APP_DIR" ]]; then
    warn "${APP_DIR} exists but is not a git repository; replacing it"
    rm -rf "$APP_DIR"
    git clone "$REPO_URL" "$APP_DIR"
  else
    git clone "$REPO_URL" "$APP_DIR"
  fi
}

get_env_value() {
  local key="$1"
  if [[ ! -f "$APP_DIR/.env" ]]; then
    return 0
  fi

  awk -F= -v key="$key" '
    $1 == key {
      sub(/^[^=]*=/, "")
      print
      exit
    }
  ' "$APP_DIR/.env"
}

set_env_value() {
  local key="$1"
  local value="$2"
  local tmp_file
  tmp_file="$(mktemp)"

  awk -v key="$key" -v value="$value" '
    BEGIN {
      updated = 0
    }
    $0 ~ "^" key "=" {
      print key "=" value
      updated = 1
      next
    }
    {
      print
    }
    END {
      if (!updated) {
        print key "=" value
      }
    }
  ' "$APP_DIR/.env" > "$tmp_file"
  mv "$tmp_file" "$APP_DIR/.env"
}

prompt_secret() {
  local key="$1"
  local prompt="$2"
  local default_value="$3"
  local current_value=""

  current_value="$(get_env_value "$key" || true)"
  case "$key:$current_value" in
    DB_PASSWORD:postgres|JWT_SECRET:fb_comment_secret|JWT_SECRET:change-this-secret-in-production|ADMIN_PASSWORD:123456789|APP_IMAGE:fb_comment:local)
      current_value=""
      ;;
  esac    

  if [[ -n "$current_value" ]]; then
    return
  fi

  local user_value=""
  if [[ -t 0 ]]; then
    read -r -s -p "$prompt" user_value || true
    printf '\n'
  fi

  if [[ -z "$user_value" ]]; then
    user_value="$default_value"
  fi

  set_env_value "$key" "$user_value"
}

generate_secret() {
  local length="$1"
  openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | head -c "$length"
}

prepare_env_file() {
  log "Preparing .env"
  if [[ ! -f "$APP_DIR/.env" ]]; then
    cp "$APP_DIR/.env.example" "$APP_DIR/.env"
  fi

  set_env_value "APP_IMAGE" "$APP_IMAGE_DEFAULT"

  prompt_secret "DB_PASSWORD" "DB_PASSWORD (leave blank to auto-generate): " "$(generate_secret 24)"
  prompt_secret "JWT_SECRET" "JWT_SECRET (leave blank to auto-generate): " "$(generate_secret 48)"
  prompt_secret "ADMIN_PASSWORD" "ADMIN_PASSWORD [123456789]: " "123456789"

  if [[ -z "$(get_env_value DB_HOST || true)" ]]; then
    set_env_value "DB_HOST" "127.0.0.1"
  fi
  if [[ -z "$(get_env_value DB_PORT || true)" ]]; then
    set_env_value "DB_PORT" "5435"
  fi
  if [[ -z "$(get_env_value DB_USER || true)" ]]; then
    set_env_value "DB_USER" "postgres"
  fi
  if [[ -z "$(get_env_value DB_NAME || true)" ]]; then
    set_env_value "DB_NAME" "fb_comment"
  fi
  if [[ -z "$(get_env_value DB_SSLMODE || true)" ]]; then
    set_env_value "DB_SSLMODE" "disable"
  fi
  if [[ -z "$(get_env_value DB_TIMEZONE || true)" ]]; then
    set_env_value "DB_TIMEZONE" "Asia/Ho_Chi_Minh"
  fi
  if [[ -z "$(get_env_value APP_PORT || true)" ]]; then
    set_env_value "APP_PORT" "8080"
  fi
  if [[ -z "$(get_env_value SCRAPER_HEADLESS || true)" ]]; then
    set_env_value "SCRAPER_HEADLESS" "true"
  fi
}

start_stack() {
  log "Stopping any existing stack"
  (cd "$APP_DIR" && docker compose down --remove-orphans) || true

  if [[ -n "${GHCR_USERNAME:-}" && -n "${GHCR_TOKEN:-}" ]]; then
    log "Logging in to GHCR"
    printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin
  fi

  log "Pulling the app image"
  (cd "$APP_DIR" && docker compose pull app)

  log "Starting the stack"
  (cd "$APP_DIR" && APP_IMAGE="$APP_IMAGE_DEFAULT" docker compose up -d --no-build --remove-orphans)

  log "Current container status"
  (cd "$APP_DIR" && docker compose ps)

  log "Waiting for /healthz"
  for _ in $(seq 1 60); do
    if curl -fsS http://127.0.0.1:8080/healthz >/dev/null; then
      log "Application is healthy"
      return 0
    fi
    sleep 2
  done

  warn "Health check did not become ready; showing app logs"
  (cd "$APP_DIR" && docker compose logs --tail=200 app) || true
  die "Bootstrap completed but the app did not become healthy"
}

main() {
  require_root
  install_dependencies
  clone_or_update_repo
  prepare_env_file
  start_stack

  log "Bootstrap finished"
  log "Repo: ${APP_DIR}"
  log "App:  http://127.0.0.1:8080"
  log "Health: curl -fsS http://127.0.0.1:8080/healthz"
  log "Next deploys will use GHCR image tag: ${APP_IMAGE_DEFAULT}"
}

main "$@"
