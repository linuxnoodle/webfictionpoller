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

  msg_info "Updating docker-compose.yml"
  cat > /opt/webfictionpoller/docker-compose.yml << 'DCEOF'
services:
  app:
    image: ghcr.io/linuxnoodle/webfictionpoller:latest
    ports:
      - "8080:8080"
    environment:
      - DB_PATH=/data/data.db
      - ADDR=:8080
      - POLL_INTERVAL=15m
      - FLARESOLVERR_URL=http://flaresolverr:8191
      - LOG_DIR=/data/logs
      - WATCHTOWER_URL=http://watchtower:8080
      - WATCHTOWER_TOKEN=webfictionpoller
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

  watchtower:
    image: containrrr/watchtower
    environment:
      - WATCHTOWER_HTTP_API=true
      - WATCHTOWER_HTTP_API_TOKEN=webfictionpoller
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    command: --interval 3600 --cleanup app
    restart: unless-stopped
DCEOF

  msg_info "Updating ${APP} (pulling latest image)"
  docker compose -f /opt/webfictionpoller/docker-compose.yml pull
  docker compose -f /opt/webfictionpoller/docker-compose.yml up -d --remove-orphans
  docker image prune -f >/dev/null 2>&1
  msg_ok "Updated ${APP} successfully"
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

msg_info "Writing docker-compose.yml"
pct exec "$CTID" -- bash -c "mkdir -p $INSTALL_DIR/data && cat > $INSTALL_DIR/docker-compose.yml << 'DCEOF'
services:
  app:
    image: ghcr.io/linuxnoodle/webfictionpoller:latest
    ports:
      - \"8080:8080\"
    environment:
      - DB_PATH=/data/data.db
      - ADDR=:8080
      - POLL_INTERVAL=15m
      - FLARESOLVERR_URL=http://flaresolverr:8191
      - LOG_DIR=/data/logs
      - WATCHTOWER_URL=http://watchtower:8080
      - WATCHTOWER_TOKEN=webfictionpoller
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

  watchtower:
    image: containrrr/watchtower
    environment:
      - WATCHTOWER_HTTP_API=true
      - WATCHTOWER_HTTP_API_TOKEN=webfictionpoller
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
    command: --interval 3600 --cleanup app
    restart: unless-stopped
DCEOF"
msg_ok "Created docker-compose.yml"

msg_info "Pulling ${APP} image"
pct exec "$CTID" -- docker compose -f "$INSTALL_DIR/docker-compose.yml" pull
msg_ok "Pulled images"

msg_info "Starting ${APP}"
pct exec "$CTID" -- docker compose -f "$INSTALL_DIR/docker-compose.yml" up -d
msg_ok "Started ${APP}"

msg_ok "Completed Successfully!\n"
echo -e "${INFO}${YW} Updates:${CL}"
echo -e "${TAB}${GATEWAY}${BGN}Automatic: Watchtower checks every hour${CL}"
echo -e "${TAB}${GATEWAY}${BGN}Manual:    pct exec $CTID -- docker compose -f $INSTALL_DIR/docker-compose.yml pull \&\& pct exec $CTID -- docker compose -f $INSTALL_DIR/docker-compose.yml up -d${CL}"
echo -e "${TAB}${GATEWAY}${BGN}In-app:    Settings -> Version & Updates -> Update Now${CL}"
echo -e "${INFO}${YW} Access it using the following URL:${CL}"
echo -e "${TAB}${GATEWAY}${BGN}http://${IP}:8080${CL}"
