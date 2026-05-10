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

  msg_info "Updating ${APP} (git pull + rebuild)"
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

msg_info "Installing Docker"
curl -fsSL https://get.docker.com | sh
systemctl enable -q --now docker
msg_ok "Installed Docker"

msg_info "Cloning ${APP} repository"
git clone -q https://github.com/linuxnoodle/webfictionpoller.git /opt/webfictionpoller/src
msg_ok "Cloned repository"

msg_info "Setting up ${APP}"
mkdir -p /opt/webfictionpoller/data
cat <<'EOF' > /opt/webfictionpoller/docker-compose.yml
services:
  app:
    build: /opt/webfictionpoller/src
    ports:
      - "8080:8080"
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
EOF
msg_ok "Created docker-compose.yml"

msg_info "Building ${APP} container (this takes a minute)"
cd /opt/webfictionpoller
docker compose build
msg_ok "Built container"

msg_info "Pulling FlareSolverr image"
docker compose pull flaresolverr
msg_ok "Pulled FlareSolverr"

msg_info "Starting ${APP}"
docker compose up -d
msg_ok "Started ${APP}"

msg_ok "Completed Successfully!\n"
echo -e "${CREATING}${GN}${APP} setup has been successfully initialized!${CL}"
echo -e "${INFO}${YW} Access it using the following URL:${CL}"
echo -e "${TAB}${GATEWAY}${BGN}http://${IP}:8080${CL}"
