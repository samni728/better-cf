#!/usr/bin/env bash
set -Eeuo pipefail

PROJECT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
WEB_BINARY="${WEB_BINARY:-${PROJECT_DIR}/bin/cf-betterip-web}"
LISTEN_ADDR="${LISTEN_ADDR:-0.0.0.0:18080}"
DATA_DIR="${DATA_DIR:-${PROJECT_DIR}/data}"
LOG_FILE="${LOG_FILE:-${PROJECT_DIR}/web.log}"
PID_FILE="${PID_FILE:-${PROJECT_DIR}/web.pid}"
LISTEN_PORT="${LISTEN_ADDR##*:}"
HEALTH_URL="${HEALTH_URL:-http://127.0.0.1:${LISTEN_PORT}/healthz}"

color_green='\033[0;32m'
color_yellow='\033[0;33m'
color_red='\033[0;31m'
color_reset='\033[0m'

info() {
  printf "%b%s%b\n" "${color_green}" "$*" "${color_reset}"
}

warn() {
  printf "%b%s%b\n" "${color_yellow}" "$*" "${color_reset}"
}

fail() {
  printf "%b%s%b\n" "${color_red}" "$*" "${color_reset}" >&2
}

process_matches() {
  local pid="$1"
  [[ "${pid}" =~ ^[0-9]+$ ]] || return 1
  kill -0 "${pid}" 2>/dev/null || return 1

  if [[ -r "/proc/${pid}/cmdline" ]]; then
    local cmdline executable expected_executable
    cmdline="$(tr '\0' ' ' < "/proc/${pid}/cmdline")"
    [[ "${cmdline}" == *"cf-betterip-web"* ]] || return 1
    [[ "${cmdline}" == *"--listen ${LISTEN_ADDR}"* ]] || return 1
    executable="$(readlink -f "/proc/${pid}/exe" 2>/dev/null || true)"
    expected_executable="$(readlink -f "${WEB_BINARY}" 2>/dev/null || true)"
    [[ -n "${executable}" && "${executable}" == "${expected_executable}" ]] || return 1
  fi
  return 0
}

running_pid() {
  local pid candidate
  if [[ -r "${PID_FILE}" ]]; then
    pid="$(tr -dc '0-9' < "${PID_FILE}")"
    if [[ -n "${pid}" ]] && process_matches "${pid}"; then
      printf '%s\n' "${pid}"
      return 0
    fi
  fi

  while IFS= read -r candidate; do
    if process_matches "${candidate}"; then
      printf '%s\n' "${candidate}"
      return 0
    fi
  done < <(pgrep -x cf-betterip-web 2>/dev/null || true)
  return 1
}

health_ok() {
  command -v curl >/dev/null 2>&1 && curl -fsS --max-time 2 "${HEALTH_URL}" >/dev/null 2>&1
}

show_status() {
  local pid
  if pid="$(running_pid)"; then
    if health_ok; then
      info "运行中：PID ${pid}，健康检查正常，监听 ${LISTEN_ADDR}"
    else
      warn "进程存在：PID ${pid}，但健康检查暂未通过（${HEALTH_URL}）"
    fi
    printf '日志：%s\n' "${LOG_FILE}"
    return 0
  fi
  warn "项目当前未运行。"
  return 1
}

start_service() {
  local pid
  if pid="$(running_pid)"; then
    warn "项目已经运行，PID ${pid}。"
    show_status || true
    return 0
  fi
  if [[ ! -x "${WEB_BINARY}" ]]; then
    fail "找不到可执行文件：${WEB_BINARY}"
    fail "请先编译或部署 bin/cf-betterip-web。"
    return 1
  fi

  mkdir -p "${DATA_DIR}" "$(dirname -- "${LOG_FILE}")" "$(dirname -- "${PID_FILE}")"
  : >> "${LOG_FILE}"
  cd "${PROJECT_DIR}"
  if command -v setsid >/dev/null 2>&1; then
    setsid "${WEB_BINARY}" --listen "${LISTEN_ADDR}" --data-dir "${DATA_DIR}" >> "${LOG_FILE}" 2>&1 < /dev/null &
  else
    nohup "${WEB_BINARY}" --listen "${LISTEN_ADDR}" --data-dir "${DATA_DIR}" >> "${LOG_FILE}" 2>&1 < /dev/null &
  fi
  pid=$!
  printf '%s\n' "${pid}" > "${PID_FILE}"

  for _ in $(seq 1 40); do
    if health_ok; then
      info "启动成功：PID ${pid}，监听 ${LISTEN_ADDR}"
      return 0
    fi
    if ! kill -0 "${pid}" 2>/dev/null; then
      break
    fi
    sleep 0.25
  done

  fail "启动失败或健康检查超时，请查看日志：${LOG_FILE}"
  tail -n 30 "${LOG_FILE}" 2>/dev/null || true
  return 1
}

stop_service() {
  local pid
  if ! pid="$(running_pid)"; then
    warn "项目当前未运行。"
    rm -f "${PID_FILE}"
    return 0
  fi

  info "正在停止 PID ${pid}..."
  kill -TERM "${pid}"
  for _ in $(seq 1 40); do
    if ! kill -0 "${pid}" 2>/dev/null; then
      rm -f "${PID_FILE}"
      info "项目已停止。"
      return 0
    fi
    sleep 0.25
  done

  warn "进程未在 10 秒内退出，执行强制停止。"
  kill -KILL "${pid}" 2>/dev/null || true
  rm -f "${PID_FILE}"
  info "项目已停止。"
}

restart_service() {
  stop_service
  start_service
}

follow_log() {
  if [[ ! -e "${LOG_FILE}" ]]; then
    warn "日志文件尚不存在：${LOG_FILE}"
    return 0
  fi
  info "正在查看实时日志（按 Ctrl+C 退出）：${LOG_FILE}"
  tail -n 100 -F "${LOG_FILE}"
}

show_menu() {
  while true; do
    printf '\n========== Better CF 管理菜单 ==========\n'
    printf '1) 启动项目\n'
    printf '2) 停止项目\n'
    printf '3) 重启项目\n'
    printf '4) 查看实时日志\n'
    printf '5) 查看运行状态\n'
    printf '0) 退出\n'
    printf '请选择 [0-5]：'
    read -r choice
    case "${choice}" in
      1) start_service ;;
      2) stop_service ;;
      3) restart_service ;;
      4) follow_log ;;
      5) show_status || true ;;
      0) exit 0 ;;
      *) warn "无效选项，请重新选择。" ;;
    esac
  done
}

case "${1:-menu}" in
  menu) show_menu ;;
  start) start_service ;;
  stop) stop_service ;;
  restart) restart_service ;;
  log|logs) follow_log ;;
  status) show_status ;;
  *)
    printf '用法：%s [menu|start|stop|restart|log|status]\n' "$0" >&2
    exit 2
    ;;
esac
