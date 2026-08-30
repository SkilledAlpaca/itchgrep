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
# It also reinstalls the logrotate config on every run (see "logrotate
# reinstall" below) and gives up loudly if the container can never be reached
# (see "fail loud" below), instead of looping forever with nothing to show
# for it in the Unraid User Scripts UI.
#
# Install as an Unraid User Scripts entry set to "At Startup of Array" — see
# the "Operations" section of README.md.
set -eu

LOG_EXPORT_DIR="${LOG_EXPORT_DIR:-/mnt/user/appdata/itchgrep/logs}"
# docker-compose.yml pins the webserver's name via `container_name:`, so this
# default should track that value. Overriding it is only needed for a
# renamed/second stack (e.g. a staging copy) on the same host.
CONTAINER_NAME="${CONTAINER_NAME:-itchgrep-webserver}"
RECONNECT_DELAY_SECONDS="${RECONNECT_DELAY_SECONDS:-5}"
# ~5 minutes at the default RECONNECT_DELAY_SECONDS before giving up and
# exiting non-zero, so a permanently-missing container shows as a failed
# User Scripts run instead of an entry that looks fine but never wrote a line.
MAX_CONSECUTIVE_FAILURES=60

SCRIPT_DIR=$(dirname "$0")

mkdir -p "${LOG_EXPORT_DIR}"

ACCESS_FILE="${LOG_EXPORT_DIR}/access.jsonl"
SECURITY_FILE="${LOG_EXPORT_DIR}/security.jsonl"
APP_FILE="${LOG_EXPORT_DIR}/app.log"

# --- logrotate reinstall -----------------------------------------------
# Unraid's /etc is a tmpfs ramdisk rebuilt from scratch on every boot, so any
# /etc/logrotate.d/itchgrep dropped in by hand silently disappears at the next
# array start and the 30-day privacy retention quietly stops. This script
# already runs at every array start (it's the User Scripts "At Startup of
# Array" entry), so reinstalling the conf here means one User Scripts entry
# keeps both the follower and the retention guarantee alive — no second entry
# to remember or forget.
#
# The template keeps LOG_EXPORT_DIR's default path literal (so the checked-in
# file is byte-identical to what gets installed in the common case) and this
# step sed-replaces that literal with the live LOG_EXPORT_DIR when it differs,
# rather than inventing a placeholder syntax that would make the template odd
# to read on its own.
LOGROTATE_SRC="${SCRIPT_DIR}/itchgrep-logrotate.conf"
LOGROTATE_DEFAULT_DIR="/mnt/user/appdata/itchgrep/logs"
LOGROTATE_DEST_DIR="/etc/logrotate.d"
LOGROTATE_DEST="${LOGROTATE_DEST_DIR}/itchgrep"

if [ -d "${LOGROTATE_DEST_DIR}" ]; then
	if [ -r "${LOGROTATE_SRC}" ]; then
		if [ "${LOG_EXPORT_DIR}" = "${LOGROTATE_DEFAULT_DIR}" ]; then
			LOGROTATE_RENDERED="${LOGROTATE_SRC}"
			LOGROTATE_TMP=""
		else
			LOGROTATE_TMP=$(mktemp)
			sed "s#${LOGROTATE_DEFAULT_DIR}#${LOG_EXPORT_DIR}#g" "${LOGROTATE_SRC}" >"${LOGROTATE_TMP}"
			LOGROTATE_RENDERED="${LOGROTATE_TMP}"
		fi

		if cmp -s "${LOGROTATE_RENDERED}" "${LOGROTATE_DEST}" 2>/dev/null; then
			: # already up to date, nothing to copy
		else
			cp "${LOGROTATE_RENDERED}" "${LOGROTATE_DEST}"
			echo "export-logs: (re)installed ${LOGROTATE_DEST}" >&2
		fi

		[ -n "${LOGROTATE_TMP}" ] && rm -f "${LOGROTATE_TMP}"
	else
		echo "export-logs: WARNING: ${LOGROTATE_SRC} not found, log rotation NOT installed" >&2
	fi
else
	echo "export-logs: WARNING: ${LOGROTATE_DEST_DIR} does not exist, log rotation NOT installed" >&2
fi
# The follower below is still worth running even if the above failed — losing
# rotation is a retention problem, not a reason to stop capturing logs.

# --- follow loop ---------------------------------------------------------

# RFC3339 UTC timestamp, matching the format the Go webserver itself emits.
now_ts() {
	date -u +%Y-%m-%dT%H:%M:%SZ
}

mark_gap() {
	# Marks a visible boundary in access.jsonl around every stream connection.
	# --tail 0 below deliberately drops anything logged while disconnected;
	# without this marker that gap is invisible in the data. With it, a reader
	# can tell "no requests happened" apart from "the follower was down".
	printf '{"ts":"%s","kind":"follower","event":"%s"}\n' "$(now_ts)" "$1" >>"${ACCESS_FILE}"
}

failures=0
first_run=1
while true; do
	if ! docker inspect "${CONTAINER_NAME}" >/dev/null 2>&1; then
		failures=$((failures + 1))
		echo "export-logs: cannot inspect ${CONTAINER_NAME} (attempt ${failures}/${MAX_CONSECUTIVE_FAILURES}), retrying in ${RECONNECT_DELAY_SECONDS}s" >&2
		if [ "${failures}" -ge "${MAX_CONSECUTIVE_FAILURES}" ]; then
			echo "export-logs: ERROR: ${CONTAINER_NAME} unreachable after ${MAX_CONSECUTIVE_FAILURES} consecutive attempts, giving up" >&2
			exit 1
		fi
		sleep "${RECONNECT_DELAY_SECONDS}"
		continue
	fi
	failures=0

	if [ "${first_run}" -eq 1 ]; then
		echo "export-logs: following ${CONTAINER_NAME} -> ${LOG_EXPORT_DIR}" >&2
		mark_gap "start"
		first_run=0
	else
		echo "export-logs: reconnecting to ${CONTAINER_NAME}" >&2
		mark_gap "reconnect"
	fi

	# --tail 0: only new lines. Anything written while the follower was down
	# (or before its first start) is accepted as lost rather than replayed,
	# since replay would need tracking a cursor into a stream Docker itself
	# does not expose one for. The start/reconnect markers above make that
	# gap visible instead of silent.
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
