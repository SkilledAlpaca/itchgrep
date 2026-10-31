#!/bin/sh
# First-time Unraid host setup for itchgrep. Idempotent — safe to re-run at
# any point (e.g. after an appdata backup restore, or to double-check a
# manual setup): directory creation is a plain `mkdir -p`, and the chown
# below (see "Ownership on data/") is always safe to repeat.
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
#           access.jsonl / security.jsonl / app.log / crawler.log.
#   data/ - DATA_PATH: the crawl database volume mounted into both the
#           webserver and dataservice containers.
#
# Ownership on data/: both container images now run as uid 65532 (the
# distroless :nonroot base image — see Dockerfile.webserver and
# Dockerfile.dataservice), so this script chowns data/ to 65532:65532 after
# creating it. That step needs root, so it only runs when this script itself
# is running as root (the normal case on Unraid, which runs User Scripts
# entries as root); otherwise it prints a warning naming the command to run
# by hand instead of failing the whole script. Re-running the chown is always
# safe — see docs/deployment.md for the full explanation of why this step
# exists.
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

if [ "$(id -u)" -eq 0 ]; then
	chown -R 65532:65532 "${DATA_DIR}"
	echo "bootstrap-host: chowned ${DATA_DIR} to 65532:65532"
else
	echo "bootstrap-host: WARNING: not running as root, skipped chown on ${DATA_DIR}"
	echo "bootstrap-host: run this once, as root, before the first docker compose up:"
	echo "bootstrap-host:   chown -R 65532:65532 ${DATA_DIR}"
fi

echo "bootstrap-host: done"
