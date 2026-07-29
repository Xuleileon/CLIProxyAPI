#!/bin/sh
set -eu

APP_DIR="/CLIProxyAPI"
CONFIG_FILE="${APP_DIR}/config.yaml"
AUTH_DIR="${APP_DIR}/auths"

mkdir -p "${AUTH_DIR}"

port="${CPA_PORT:-${PORT:-8080}}"
api_key="${CPA_API_KEY:-}"
mgmt_key="${CPA_MANAGEMENT_KEY:-}"

if [ ! -f "${CONFIG_FILE}" ]; then
	cat >"${CONFIG_FILE}" <<EOF
host: ''
port: ${port}
auth-dir: '${AUTH_DIR}'
debug: false
api-keys:
EOF
	if [ -n "${api_key}" ]; then
		printf "  - '%s'\n" "${api_key}" >>"${CONFIG_FILE}"
	else
		printf "  - 'change-me'\n" >>"${CONFIG_FILE}"
	fi

	cat >>"${CONFIG_FILE}" <<EOF
remote-management:
  allow-remote: true
  secret-key: '${mgmt_key}'
EOF
fi

exec "${APP_DIR}/CLIProxyAPIPlus" "$@"
