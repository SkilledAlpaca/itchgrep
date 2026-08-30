#!/bin/sh
# Host-side follower for both containers' stdout logs.
#
# Why a host script instead of a sidecar container: a sidecar that reads a
# container's own log would need the Docker socket (or the log-driver's file)
# mounted into the compose stack, which is a much bigger blast radius than a
# cron-launched shell script — a socket mount can control every container on
# the host, not just read one log. This script needs neither: it shells out to
# the `docker` CLI already on the Unraid host and appends what it reads to
# plain files. No new service to build, upgrade or keep patched.
#
# What it does: follows `docker logs` for BOTH the webserver and the
# dataservice containers forever, each in its own reconnecting loop, with a
# short backoff across restarts or a daemon reload:
#   webserver lines are split three ways by the `"kind"` discriminator
#   emitted by internal/web/logmiddleware.go and internal/web/probes.go:
#     "kind":"access" -> access.jsonl    (one line per served request)
#     "kind":"probe"  -> security.jsonl  (one line per blocked scanner probe)
#     anything else   -> app.log         (human INFO:/WARN: lines)
#   dataservice lines carry no such discriminator - the crawler emits only
#   plain INFO:/WARN:/ERRO: text - so every line goes to one file:
#     (any line)      -> crawler.log
#
# Before this followed only the webserver: every crawl-side failure (coverage
# floor refusals, publish failures, buildIndex errors, checkpoint write
# failures, self-heal messages) lived only in the dataservice container's own
# Docker-capped log buffer, with nothing durable to show for it once that
# buffer rolled over.
#
# It also reinstalls the logrotate config on every run (see "logrotate
# reinstall" below) and gives up loudly if EITHER container can never be
# reached (see "fail loud" below), instead of looping forever with nothing to
# show for it in the Unraid User Scripts UI.
#
# Install as an Unraid User Scripts entry set to "At Startup of Array" — see
# docs/deployment.md's "Installing the log-export follower" section.
set -eu

LOG_EXPORT_DIR="${LOG_EXPORT_DIR:-/mnt/user/appdata/itchgrep/logs}"
# docker-compose.yml pins both containers' names via `container_name:`, so
# these defaults should track those values. Overriding either is only needed
# for a renamed/second stack (e.g. a staging copy) on the same host.
CONTAINER_NAME="${CONTAINER_NAME:-itchgrep-webserver}"
DATASERVICE_CONTAINER_NAME="${DATASERVICE_CONTAINER_NAME:-itchgrep-dataservice}"
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
CRAWLER_FILE="${LOG_EXPORT_DIR}/crawler.log"

# --- logrotate reinstall -----------------------------------------------
# Unraid's /etc is a tmpfs ramdisk rebuilt from scratch on every boot, so any
# /etc/logrotate.d/itchgrep dropped in by hand silently disappears at the next
# array start and the 30-day privacy retention quietly stops. This script
# already runs at every array start (it's the User Scripts "At Startup of
# Array" entry), so reinstalling the conf here means one User Scripts entry
# keeps both the followers and the retention guarantee alive — no second entry
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
# The followers below are still worth running even if the above failed —
# losing rotation is a retention problem, not a reason to stop capturing logs.

# --- follow loops ---------------------------------------------------------

# RFC3339 UTC timestamp, matching the format the Go binaries themselves emit.
now_ts() {
	date -u +%Y-%m-%dT%H:%M:%SZ
}

# mark_gap writes a visible boundary into $1 around every stream connection.
# --tail 0 below deliberately drops anything logged while disconnected;
# without this marker that gap is invisible in the data. With it, a reader
# can tell "no activity happened" apart from "the follower was down".
mark_gap() {
	printf '{"ts":"%s","kind":"follower","event":"%s"}\n' "$(now_ts)" "$2" >>"$1"
}

# follow_container runs one container's reconnect-forever follow loop.
#   $1 = the container name to follow
#   $2 = "split" (route webserver lines by their "kind" field) or "plain"
#        (append every line, unmodified, to one file)
#   $3 = the file to write into for "plain" mode, and where mark_gap's
#        boundary lines land for either mode (webserver's own "split" mode
#        still marks gaps in ACCESS_FILE, since that is the file a reader
#        watches for continuity)
#   $4 = a short label for this loop's own log lines, so the two followers'
#        stderr output is distinguishable in the User Scripts console
#
# Runs forever unless the container can never be reached
# (MAX_CONSECUTIVE_FAILURES in a row), in which case it exits 1 - the caller
# (main, below) treats either follower exiting as a reason to stop both and
# fail the whole script loudly, rather than silently running on with only
# half the logs captured.
follow_container() {
	container_name="$1"
	mode="$2"
	target_file="$3"
	label="$4"

	failures=0
	first_run=1
	while true; do
		if ! docker inspect "${container_name}" >/dev/null 2>&1; then
			failures=$((failures + 1))
			echo "export-logs[${label}]: cannot inspect ${container_name} (attempt ${failures}/${MAX_CONSECUTIVE_FAILURES}), retrying in ${RECONNECT_DELAY_SECONDS}s" >&2
			if [ "${failures}" -ge "${MAX_CONSECUTIVE_FAILURES}" ]; then
				echo "export-logs[${label}]: ERROR: ${container_name} unreachable after ${MAX_CONSECUTIVE_FAILURES} consecutive attempts, giving up" >&2
				return 1
			fi
			sleep "${RECONNECT_DELAY_SECONDS}"
			continue
		fi
		failures=0

		if [ "${first_run}" -eq 1 ]; then
			echo "export-logs[${label}]: following ${container_name} -> ${LOG_EXPORT_DIR}" >&2
			mark_gap "${target_file}" "start"
			first_run=0
		else
			echo "export-logs[${label}]: reconnecting to ${container_name}" >&2
			mark_gap "${target_file}" "reconnect"
		fi

		# --tail 0: only new lines. Anything written while the follower was
		# down (or before its first start) is accepted as lost rather than
		# replayed, since replay would need tracking a cursor into a stream
		# Docker itself does not expose one for. The start/reconnect markers
		# above make that gap visible instead of silent.
		if [ "${mode}" = "split" ]; then
			docker logs -f --tail 0 "${container_name}" 2>&1 | while IFS= read -r line; do
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
		else
			docker logs -f --tail 0 "${container_name}" 2>&1 | while IFS= read -r line; do
				printf '%s\n' "${line}" >>"${target_file}"
			done
		fi
		echo "export-logs[${label}]: disconnected from ${container_name}, reconnecting in ${RECONNECT_DELAY_SECONDS}s" >&2
		sleep "${RECONNECT_DELAY_SECONDS}"
	done
}

follow_container "${CONTAINER_NAME}" split "${ACCESS_FILE}" webserver &
webserver_pid=$!
follow_container "${DATASERVICE_CONTAINER_NAME}" plain "${CRAWLER_FILE}" dataservice &
dataservice_pid=$!

# Stop both followers together on a signal, rather than leaving an orphan
# running after User Scripts kills this parent.
trap 'kill "${webserver_pid}" "${dataservice_pid}" 2>/dev/null' INT TERM

# Wait for whichever follower exits first. Neither exits in normal operation
# - each one loops on its own reconnect-forever cycle - so this only fires
# when one of them has already given up after MAX_CONSECUTIVE_FAILURES, and
# at that point running the other alone, with nothing watching for the first
# one to come back, would silently halve the coverage this script exists to
# provide. `wait -n` is a bash/ksh extension no POSIX-only /bin/sh (dash,
# BusyBox ash on Unraid) implements, hence the portable kill -0 poll instead.
while kill -0 "${webserver_pid}" 2>/dev/null && kill -0 "${dataservice_pid}" 2>/dev/null; do
	sleep 2
done

if ! kill -0 "${webserver_pid}" 2>/dev/null; then
	wait "${webserver_pid}"
	exit_code=$?
	dead_label="webserver (${CONTAINER_NAME})"
	kill "${dataservice_pid}" 2>/dev/null
else
	wait "${dataservice_pid}"
	exit_code=$?
	dead_label="dataservice (${DATASERVICE_CONTAINER_NAME})"
	kill "${webserver_pid}" 2>/dev/null
fi

echo "export-logs: ${dead_label} follower exited (code ${exit_code}); stopping the other follower and exiting" >&2
exit "${exit_code}"
