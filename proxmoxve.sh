#!/usr/bin/env bash
source <(curl -fsSL https://raw.githubusercontent.com/community-scripts/ProxmoxVE/main/misc/build.func)
# Copyright (c) 2021-2026 community-scripts ORG
# Author: linuxnoodle
# License: MIT | https://github.com/community-scripts/ProxmoxVE/raw/main/LICENSE
# Source: https://github.com/linuxnoodle/webfictionpoller

APP="WebFictionPoller"
var_tags="${var_tags:-webfiction;tracker}"
var_cpu="${var_cpu:-2}"
var_ram="${var_ram:-2048}"
var_disk="${var_disk:-8}"
var_os="${var_os:-debian}"
var_version="${var_version:-13}"
var_unprivileged="${var_unprivileged:-1}"

header_info "$APP"
variables
color
catch_errors

function update_script() {
  header_info
  check_container_storage
  check_container_resources
  if [[ ! -f /opt/webfictionpoller/docker-compose.yml ]]; then
    msg_error "No ${APP} Installation Found!"
    exit
  fi

  msg_info "Updating ${APP}"
  cd /opt/webfictionpoller/src
  git pull -q
  docker compose -f /opt/webfictionpoller/docker-compose.yml build --pull app
  docker compose -f /opt/webfictionpoller/docker-compose.yml up -d --remove-orphans
  msg_ok "Updated ${APP}"
  exit
}

start
build_container
description

INSTALL_DIR="/opt/webfictionpoller"

msg_info "Installing Docker in LXC"
if ! pct exec "$CTID" -- bash -c "command -v docker &>/dev/null"; then
  pct exec "$CTID" -- bash -c "curl -fsSL https://get.docker.com | sh"
fi
pct exec "$CTID" -- systemctl enable -q --now docker
msg_ok "Installed Docker"

msg_info "Cloning ${APP} into LXC"
pct exec "$CTID" -- bash -c "apt-get update -qq && apt-get install -y -qq git > /dev/null 2>&1"
pct exec "$CTID" -- git clone -q https://github.com/linuxnoodle/webfictionpoller.git "$INSTALL_DIR/src"
msg_ok "Cloned repository"

msg_info "Writing docker-compose.yml"
pct exec "$CTID" -- bash -c "mkdir -p $INSTALL_DIR/data && cat > $INSTALL_DIR/docker-compose.yml << 'DCEOF'
services:
  app:
    build: /opt/webfictionpoller/src
    ports:
      - \"8080:8080\"
    environment:
      - DB_PATH=/data/data.db
      - ADDR=:8080
      - POLL_INTERVAL=15m
      - FLARESOLVERR_URL=http://flaresolverr:8191
      - LOG_DIR=/data/logs
    volumes:
      - ./data:/data
    depends_on:
      - flaresolverr
    restart: unless-stopped

  flaresolverr:
    image: ghcr.io/flaresolverr/flaresolverr:latest
    environment:
      - LOG_LEVEL=info
    restart: unless-stopped
DCEOF"
msg_ok "Created docker-compose.yml"

msg_info "Building ${APP} container (this takes a minute)"
pct exec "$CTID" -- docker compose -f "$INSTALL_DIR/docker-compose.yml" build
msg_ok "Built container"

msg_info "Pulling FlareSolverr"
pct exec "$CTID" -- docker compose -f "$INSTALL_DIR/docker-compose.yml" pull flaresolverr
msg_ok "Pulled FlareSolverr"

msg_info "Starting ${APP}"
pct exec "$CTID" -- docker compose -f "$INSTALL_DIR/docker-compose.yml" up -d
msg_ok "Started ${APP}"

msg_ok "Completed Successfully!\n"
echo -e "${CREATING}${GN}${APP} setup has been successfully initialized!${CL}"
echo -e "${INFO}${YW} Access it using the following URL:${CL}"
echo -e "${TAB}${GATEWAY}${BGN}http://${IP}:8080${CL}"
