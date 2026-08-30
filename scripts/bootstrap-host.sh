#!/bin/sh
# First-time Unraid host setup for itchgrep. Idempotent — safe to re-run at
# any point (e.g. after an appdata backup restore, or to double-check a
# manual setup) since every step below is a plain `mkdir -p`.
#
# Nothing else in this repo creates these directories: docker-compose.yml
# only mounts them (it assumes they already exist), and export-logs.sh only
# creates LOG_EXPORT_DIR, not the other two. Run this once before the first
# `docker compose up` on a new host.
#
# The three directories and their roles:
#   src/  - the git checkout of this repo itself (what scripts/update.sh
#           pulls into and what `docker compose` is run from).
#   logs/ - LOG_EXPORT_DIR: where scripts/export-logs.sh writes
#           access.jsonl / security.jsonl / app.log.
#   data/ - DATA_PATH: the crawl database volume mounted into the webserver
#           container.
#
# Ownership is intentionally NOT set here. data/ is written from inside the
# container, and the right uid/gid to chown it to depends on which container
# user the image runs as — a decision landing in a separate batch. Ownership
# requirements are documented in the deployment docs once that lands.
set -eu

BASE_PATH="${BASE_PATH:-/mnt/user/appdata/itchgrep}"

SRC_DIR="${BASE_PATH}/src"
LOGS_DIR="${BASE_PATH}/logs"
DATA_DIR="${BASE_PATH}/data"

create_if_missing() {
	dir="$1"
	if [ -d "${dir}" ]; then
		echo "bootstrap-host: already exists: ${dir}"
	else
		mkdir -p "${dir}"
		echo "bootstrap-host: created: ${dir}"
	fi
}

create_if_missing "${SRC_DIR}"
create_if_missing "${LOGS_DIR}"
create_if_missing "${DATA_DIR}"

echo "bootstrap-host: done"
