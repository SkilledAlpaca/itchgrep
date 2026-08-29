#!/bin/sh
# Host-side follower for the webserver's stdout log.
#
# Why a host script instead of a sidecar container: a sidecar that reads the
# webserver's own log would need the Docker socket (or the log-driver's file)
# mounted into the compose stack, which is a much bigger blast radius than a
# cron-launched shell script — a socket mount can control every container on
# the host, not just read one log. This script needs neither: it shells out to
# the `docker` CLI already on the Unraid host and appends what it reads to
# plain files. No new service to build, upgrade or keep patched.
#
# What it does: follows `docker logs` for the webserver container forever,
# reconnecting with a short backoff across restarts or a daemon reload, and
# splits each line three ways by the `"kind"` discriminator emitted by
# internal/web/logmiddleware.go and internal/web/probes.go:
#   "kind":"access" -> access.jsonl    (one line per served request)
#   "kind":"probe"  -> security.jsonl  (one line per blocked scanner probe)
#   anything else   -> app.log         (human INFO:/WARN: lines)
#
# Install as an Unraid User Scripts entry set to "At Startup of Array" — see
# the "Operations" section of README.md.
set -eu

LOG_EXPORT_DIR="${LOG_EXPORT_DIR:-/mnt/user/appdata/itchgrep/logs}"
CONTAINER_NAME="${CONTAINER_NAME:-itchgrep-webserver-1}"
RECONNECT_DELAY_SECONDS="${RECONNECT_DELAY_SECONDS:-5}"

mkdir -p "${LOG_EXPORT_DIR}"

ACCESS_FILE="${LOG_EXPORT_DIR}/access.jsonl"
SECURITY_FILE="${LOG_EXPORT_DIR}/security.jsonl"
APP_FILE="${LOG_EXPORT_DIR}/app.log"

while true; do
	echo "export-logs: following ${CONTAINER_NAME} -> ${LOG_EXPORT_DIR}" >&2
	# --tail 0: only new lines. Anything written while the follower was down
	# (or before its first start) is accepted as lost rather than replayed,
	# since replay would need tracking a cursor into a stream Docker itself
	# does not expose one for.
	docker logs -f --tail 0 "${CONTAINER_NAME}" 2>&1 | while IFS= read -r line; do
		case "${line}" in
		*'"kind":"access"'*)
			printf '%s\n' "${line}" >>"${ACCESS_FILE}"
			;;
		*'"kind":"probe"'*)
			printf '%s\n' "${line}" >>"${SECURITY_FILE}"
			;;
		*)
			printf '%s\n' "${line}" >>"${APP_FILE}"
			;;
		esac
	done
	echo "export-logs: disconnected from ${CONTAINER_NAME}, reconnecting in ${RECONNECT_DELAY_SECONDS}s" >&2
	sleep "${RECONNECT_DELAY_SECONDS}"
done
