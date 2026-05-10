#!/usr/bin/env bash
source <(curl -fsSL https://raw.githubusercontent.com/community-scripts/ProxmoxVE/main/misc/build.func)
# Copyright (c) 2021-2026 community-scripts ORG
# Author: linuxnoodle
# License: MIT | https://github.com/community-scripts/ProxmoxVE/raw/main/LICENSE
# Source: https://github.com/linuxnoodle/webfictionpoller

APP="WebFictionPoller"
var_tags="${var_tags:-webfiction;tracker}"
var_cpu="${var_cpu:-2}"
var_ram="${var_ram:-1024}"
var_disk="${var_disk:-4}"
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
  if [[ ! -f /opt/webfictionpoller/webfictionpoller ]]; then
    msg_error "No ${APP} Installation Found!"
    exit
  fi

  if check_for_gh_release "webfictionpoller" "linuxnoodle/webfictionpoller"; then
    msg_info "Stopping ${APP}"
    systemctl stop webfictionpoller
    msg_ok "Stopped ${APP}"

    msg_info "Updating ${APP}"
    fetch_and_deploy_gh_release "webfictionpoller" "linuxnoodle/webfictionpoller" "tarball"
    chmod +x /opt/webfictionpoller/webfictionpoller
    msg_ok "Updated ${APP}"

    msg_info "Starting ${APP}"
    systemctl start webfictionpoller
    msg_ok "Started ${APP}"

    msg_ok "Updated Successfully!"
  fi
  exit
}

start
build_container
description

msg_ok "Completed Successfully!\n"
echo -e "${CREATING}${GN}${APP} setup has been successfully initialized!${CL}"
echo -e "${INFO}${YW} Access it using the following URL:${CL}"
echo -e "${TAB}${GATEWAY}${BGN}http://${IP}:8080${CL}"
