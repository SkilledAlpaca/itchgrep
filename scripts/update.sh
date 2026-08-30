#!/bin/sh
# Production update runbook, as a script. Run ON THE UNRAID HOST, from the
# repo checkout directory (e.g. /mnt/user/appdata/itchgrep/src) that
# scripts/bootstrap-host.sh created.
#
# This just encodes the manual steps an operator would otherwise run by
# hand, so the sequence (pull, build, verify, or roll back) is the same
# every time instead of remembered ad hoc.
set -eu

CONTAINER_NAME="${CONTAINER_NAME:-itchgrep-webserver}"
HEALTH_TIMEOUT_SECONDS=60

echo "update: checking out directory is $(pwd)"

# --- (a) pull latest main -------------------------------------------------
# The Unraid host has no `git` binary, so this uses the `alpine/git` image
# instead of expecting one to be installed. `-c safe.directory=/git` is
# needed because git refuses to operate on a repo it doesn't own by default,
# and the repo's on-disk owner (whatever uid created it) rarely matches the
# uid git runs as inside the throwaway container.
echo "update: pulling latest main..."
docker run --rm -v "$(pwd)":/git alpine/git -c safe.directory=/git pull https://github.com/SkilledAlpaca/itchgrep.git main

# --- (b) rebuild and restart ----------------------------------------------
# --build is mandatory here: plain `up -d` will happily leave a stale image
# running if the tag hasn't changed, silently undoing the pull above.
echo "update: rebuilding and restarting (docker compose --profile public up -d --build)..."
docker compose --profile public up -d --build

# --- (c) verify ------------------------------------------------------------
echo "update: waiting up to ${HEALTH_TIMEOUT_SECONDS}s for ${CONTAINER_NAME} to report healthy..."
elapsed=0
status=""
while [ "${elapsed}" -lt "${HEALTH_TIMEOUT_SECONDS}" ]; do
	status=$(docker inspect -f '{{.State.Health.Status}}' "${CONTAINER_NAME}" 2>/dev/null || echo "unknown")
	if [ "${status}" = "healthy" ]; then
		break
	fi
	sleep 2
	elapsed=$((elapsed + 2))
done

if [ "${status}" != "healthy" ]; then
	echo "update: FAILED - ${CONTAINER_NAME} did not report healthy within ${HEALTH_TIMEOUT_SECONDS}s (last status: ${status})" >&2
	echo "update: rollback guidance:" >&2
	echo "  1. Find the previous commit:" >&2
	echo "     docker run --rm -v \"\$(pwd)\":/git alpine/git -c safe.directory=/git log --oneline -5" >&2
	echo "  2. Check it out:" >&2
	echo "     docker run --rm -v \"\$(pwd)\":/git alpine/git -c safe.directory=/git checkout <previous-commit>" >&2
	echo "  3. Rebuild on the previous commit:" >&2
	echo "     docker compose --profile public up -d --build" >&2
	exit 1
fi

echo "update: ${CONTAINER_NAME} is healthy"

# Print every running container's image creation timestamp (not just the
# webserver's - dataservice rebuilds too) so the operator can confirm the
# stack is actually running freshly built images, not cached ones left over
# from a build that silently no-opped.
echo "update: running image creation timestamps:"
for cid in $(docker compose --profile public ps -q); do
	name=$(docker inspect -f '{{.Name}}' "${cid}" | sed 's#^/##')
	image_id=$(docker inspect -f '{{.Image}}' "${cid}")
	image_created=$(docker inspect -f '{{.Created}}' "${image_id}")
	echo "  ${name}: image created ${image_created}"
done

echo "update: done"
