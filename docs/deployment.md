# Deployment

This document is the path from a bare Unraid host to a running, publicly
reachable itchgrep, and the update runbook for every deploy after the first.
It assumes the reader has completed [docs/getting-started.md](getting-started.md)
and understands what the two services do; it does not repeat that here.

Production facts referenced throughout this document:

- Host: Unraid.
- Checkout: `/mnt/user/appdata/itchgrep/src`
- Data: `/mnt/user/appdata/itchgrep/data`
- Exported logs: `/mnt/user/appdata/itchgrep/logs`

For the system design behind these choices, see
[docs/architecture.md](architecture.md). For the Cloudflare-side setup (DNS,
the tunnel itself, WAF rules), see [docs/infrastructure.md](infrastructure.md) —
this document only covers what runs the containers.

## Prerequisites

### The host has no `git` binary

Every Git operation on the host runs through a throwaway `alpine/git`
container instead of an installed `git` command. `-c safe.directory=/git` is
required because Git refuses to operate on a repository it doesn't own by
default, and the repository's on-disk owner (whatever user created it) will
rarely match the user Git runs as inside that container.

### Bootstrap the three host directories

COMMAND: `BASE_PATH=/mnt/user/appdata/itchgrep scripts/bootstrap-host.sh`
(or, from an existing checkout on the host, `scripts/bootstrap-host.sh` alone
— it defaults `BASE_PATH` to `/mnt/user/appdata/itchgrep` already)
Purpose: Creates `src/`, `logs/`, and `data/` under `BASE_PATH`, the
directories every other step in this document assumes already exist.
When to use: Once, before the very first deploy on a new host. Safe to
re-run any time after that (each step is a plain `mkdir -p`) — for example,
after restoring an Unraid appdata backup, to double-check a manual setup.
Prerequisites: Shell access to the Unraid host (its own **Terminal** icon
in the web UI, or SSH).
Expected result:
```
bootstrap-host: created: /mnt/user/appdata/itchgrep/src
bootstrap-host: created: /mnt/user/appdata/itchgrep/logs
bootstrap-host: created: /mnt/user/appdata/itchgrep/data
bootstrap-host: done
```
On a re-run, each line instead reads `already exists: ...`.
Common failures: `Permission denied` means the shell you're running this
from doesn't have write access to `/mnt/user/appdata` — check you're on the
Unraid host itself, not a remote machine.
How to undo or recover: `rm -r` any of the three directories to remove
them; nothing else in this script is destructive.

This script deliberately does **not** set ownership on `data/` — see the
next step for why that's separate.

### Clone the repository into `src/`

COMMAND: `docker run --rm -v /mnt/user/appdata/itchgrep/src:/git alpine/git -c safe.directory=/git clone https://github.com/SkilledAlpaca/itchgrep.git .`
Purpose: Populates the empty `src/` directory with the repository, using a
throwaway container instead of a host-installed `git`.
When to use: Once, on first deploy only — every deploy after this one uses
`scripts/update.sh`'s pull instead (see below).
Prerequisites: `src/` exists and is empty (from the bootstrap step above);
Docker installed on the host (Unraid ships it).
Expected result: A `Cloning into '.'...` progress message, then the prompt
returns with no error; `ls /mnt/user/appdata/itchgrep/src` shows the
repository's files, including `docker-compose.yml`.
Common failures: `fatal: destination path '.' already exists and is not an
empty directory` means `src/` already has something in it — either a
previous partial clone (delete its contents and retry) or you're re-running
this on an already-deployed host (use the update flow instead).
How to undo or recover: Delete everything under `src/` and retry.

### Write the production `.env` file

Create `/mnt/user/appdata/itchgrep/src/.env` (copy `.env.example` as a
starting point — `cp .env.example .env` from inside `src/`). At minimum, a
public production deployment needs:

| Variable | Example value | Why it's required here |
| --- | --- | --- |
| `COMPOSE_PROJECT_NAME` | `itchgrep` | Pins the Compose project name. Without it, running `docker compose` from a directory not literally named `itchgrep` (this checkout lives in `src/`) creates a differently named, independent stack instead of managing the existing one — see [docs/troubleshooting.md](troubleshooting.md) for exactly what that failure looks like. |
| `DATA_PATH` | `/mnt/user/appdata/itchgrep/data` | Points the shared data volume at the host directory `bootstrap-host.sh` created, instead of an anonymous Docker-managed volume. |
| `CLOUDFLARE_TUNNEL_TOKEN` | *(secret — from the Cloudflare Zero Trust dashboard)* | Required only if deploying with `--profile public` (see below); without it `cloudflared` starts and immediately exits. |
| `TRUST_PROXY_HEADERS` | `true` | Required alongside the tunnel token — tells the webserver to read the visitor's real address from Cloudflare's headers instead of treating every request as coming from the tunnel container. |

Every variable here — its exact format, default, and what happens if it's
missing or wrong — is documented once, in
[docs/configuration.md](configuration.md); this table only says which ones a
production deploy actually needs to set. Tuning variables
(`CRAWL_INTERVAL`, `SCRAPE_RPS`, `COVERAGE_FLOOR`, and the rest) have working
defaults and don't need to be in this file unless you're deliberately
changing them.

**`.env` is never committed** — it's excluded by `.gitignore`, and pulling
the latest source (the update flow below) never touches it.

### The one-time ownership fix for `data/`

Both container images run their process as uid `65532` (a non-root user
baked into the `distroless:nonroot` base image — see `Dockerfile.webserver`
and `Dockerfile.dataservice`). Docker only sets ownership automatically for
a *named* Docker volume; a bind-mounted host path like `DATA_PATH` above is
never `chown`'d for you.

COMMAND: `chown -R 65532:65532 /mnt/user/appdata/itchgrep/data`
Purpose: Gives the container process permission to create and open
`index.bleve/`, `assets.json`, and everything else it writes to `data/`.
When to use: Once, before the very first `docker compose up` against this
`DATA_PATH`. Also required again after upgrading an older deployment that
predates the switch to this non-root image — check
[docs/troubleshooting.md](troubleshooting.md) if a previously working
deployment suddenly can't write to `/data` after an update.
Prerequisites: The `data/` directory exists (from the bootstrap step).
Expected result: No output; `ls -ln /mnt/user/appdata/itchgrep/data` shows
`65532 65532` as the owner and group of its contents (once the containers
have written something).
Common failures: `Permission denied` running `chown` itself means you're
not running as a user with rights over `/mnt/user/appdata` — use the
Unraid host's own root shell.
How to undo or recover: Not destructive — re-running it is always safe.
Skipping it is not a degraded mode: both services fail to write anything at
all, and the webserver can never take the search index's lock file, so it
never serves real data.

## First deploy vs. update

**First deploy** is everything above, plus bringing the stack up for the
first time:

COMMAND: `cd /mnt/user/appdata/itchgrep/src && docker compose --profile public up -d --build`
Purpose: Builds both service images from source and starts the full
production stack — `dataservice`, `webserver`, and `cloudflared` — detached
(`-d`, so it keeps running after the terminal closes).
When to use: Once, after every prerequisite above is in place.
Prerequisites: All of the "Prerequisites" steps above completed, in order.
Expected result: Build output for both images, then
`Container itchgrep-webserver  Started` (and similarly for the other two
services). The dataservice begins a full crawl immediately, since no data
has ever been published on this volume — expect roughly six hours before
the site shows real results; see "Verification" below for what to check in
the meantime.
Common failures: `Bind for 127.0.0.1:62685 failed: port is already
allocated` or similar — see
[docs/troubleshooting.md](troubleshooting.md)'s port-already-in-use
section, most often caused by a missing `COMPOSE_PROJECT_NAME`.
How to undo or recover: `docker compose --profile public down` stops
everything without deleting data.

**Every deploy after the first** uses `scripts/update.sh`, which encodes the
same pull-rebuild-verify sequence every time instead of relying on an
operator to remember the right order by hand. Read the script itself
(`scripts/update.sh`) for the exact commands it runs — this section
describes what it does and what to expect, not a copy of its logic.

COMMAND: `scripts/update.sh`
Purpose: Pulls the latest `main` branch, rebuilds both images, restarts the
stack, and waits for the webserver to report healthy — or prints rollback
guidance if it doesn't.
When to use: For every deploy after the first, whenever there's a new
commit on `main` you want running in production.
Prerequisites: Run from the checkout directory
(`/mnt/user/appdata/itchgrep/src`), which the script itself confirms in its
first line of output. The production `.env` from the previous section
already in place — this script doesn't touch it.
Expected result:
```
update: checking out directory is /mnt/user/appdata/itchgrep/src
update: pulling latest main...
...
update: rebuilding and restarting (docker compose --profile public up -d --build)...
...
update: waiting up to 60s for itchgrep-webserver to report healthy...
update: itchgrep-webserver is healthy
update: running image creation timestamps:
  /itchgrep-webserver: image created 2026-...
  /itchgrep-dataservice-1: image created 2026-...
update: done
```
The trailing image-creation timestamps are worth actually reading, not just
glancing past — they're how you confirm the stack is running images that
were *just built*, not stale ones left over from a build that silently
no-opped.
Common failures: If the webserver doesn't report healthy within 60 seconds,
the script exits non-zero and prints rollback guidance directly to your
terminal (find the previous commit, check it out via the same `alpine/git`
container pattern, rebuild). See "Rollback" below.
How to undo or recover: See "Rollback" below if it fails; if it succeeds
but something is still wrong, see
[docs/troubleshooting.md](troubleshooting.md) or
[docs/disaster-recovery.md](disaster-recovery.md) for data-loss scenarios.

**`--build` is mandatory in both commands above.** Plain `docker compose up
-d`, without it, happily leaves a stale image running if the image tag
hasn't changed — silently undoing whatever the `git pull` just fetched.

## The `--profile public` distinction

`docker-compose.yml` defines the `cloudflared` container under an opt-in
Compose **profile** named `public` — a Compose feature that excludes a
service from the stack unless its profile is explicitly requested. This
means:

- **`docker compose up -d --build`** (no `--profile public`) starts only
  `dataservice` and `webserver`. Nothing is reachable from outside the
  host; only `http://127.0.0.1:62685` (dataservice, loopback-only — see the
  warning below) and `http://<host>:62686` (webserver) work, and only from
  the host or its local network.
- **`docker compose --profile public up -d --build`** (what both the
  first-deploy and update commands above use) additionally starts
  `cloudflared`, which dials out to Cloudflare and makes the site reachable
  at the public hostname configured in the Cloudflare dashboard (see
  [docs/infrastructure.md](infrastructure.md)).

Every command in this document uses `--profile public`, because production
is always meant to be publicly reachable. If you ever need to take the
public site down while leaving the origin running locally (for host
maintenance, for instance), `docker compose stop cloudflared` does that
without touching the other two services.

**Never route a public hostname at the dataservice.** Its
`/trigger-fetch` endpoint takes no authentication by design — it's protected
by network position (bound to `127.0.0.1:62685`, reachable only from the
host itself), not a credential. A public route to it, on any port, hands
anyone a button that starts a multi-hour crawl.

## Verification

After either a first deploy or an update, confirm the stack is actually
healthy before considering the deploy done:

COMMAND: `docker inspect -f '{{.State.Health.Status}}' itchgrep-webserver`
Purpose: Reads the webserver container's Docker healthcheck state directly
— the same state `scripts/update.sh` polls automatically.
When to use: Any time you want to check the webserver's health outside of
running a full update.
Prerequisites: The stack is running (`docker compose ps` shows
`itchgrep-webserver`).
Expected result: `healthy`. Immediately after a restart it briefly reads
`starting` (the healthcheck's 10-second `start_period`); anything other
than `healthy` after that grace period needs investigating — see
[docs/troubleshooting.md](troubleshooting.md).
Common failures: `Error: No such object: itchgrep-webserver` means the
container isn't running at all — check `docker compose ps` and the
container's logs.
How to undo or recover: Not applicable — this is a read-only check.

COMMAND: `curl -o /dev/null -s -w "%{http_code}\n" http://127.0.0.1:62686/healthz`
Purpose: Confirms the webserver actually answers HTTP requests on its
published port, from the host itself — independent of Docker's own
healthcheck bookkeeping.
When to use: As a second, independent check alongside the healthcheck
state above, or when troubleshooting a container Docker reports healthy
but that still seems unreachable from outside.
Prerequisites: The stack is running.
Expected result: `200`.
Common failures: Connection refused means the container isn't up or isn't
bound to that port — check `docker compose ps`. A `503` means the
webserver is up but has no data loaded yet — expected right after a first
deploy, before the initial crawl publishes; see
[docs/troubleshooting.md](troubleshooting.md) to tell that apart from a
genuinely broken index.
How to undo or recover: Not applicable — this is a read-only check.

Finally, load the public hostname in a browser (or `curl -I
https://itchgrep.com/`) to confirm the tunnel itself is routing traffic —
this exercises the full path (Cloudflare → `cloudflared` → `webserver`)
that the two checks above don't cover on their own. If the loopback checks
pass but the public site doesn't load, the problem is in the tunnel, not
the origin — see
[docs/troubleshooting.md](troubleshooting.md)'s "site unreachable publicly
but fine on loopback" section.

## Rollback

`scripts/update.sh` prints this guidance itself when the post-update health
check fails, so you don't need to remember it — but the sequence is:

1. Find the previous working commit:
   ```
   docker run --rm -v "$(pwd)":/git alpine/git -c safe.directory=/git log --oneline -5
   ```
2. Check it out:
   ```
   docker run --rm -v "$(pwd)":/git alpine/git -c safe.directory=/git checkout <previous-commit>
   ```
3. Rebuild and restart on that commit:
   ```
   docker compose --profile public up -d --build
   ```

This rolls back the **code**, not the data — the crawl database on
`DATA_PATH` is untouched by any of this, for better or worse. If the
problem you're rolling back from is data corruption rather than a bad code
change, rolling back the code alone won't fix it; see
[docs/disaster-recovery.md](disaster-recovery.md).

## Logs

The webserver's only log sink is its own stdout, captured by Docker with a
10 MB × 3 rotation cap per service (see the `x-logging` block in
`docker-compose.yml`) — a safety valve against unbounded growth, not a
retention policy. `scripts/export-logs.sh` is what actually retains it:
a host-side script that follows the container's log stream continuously and
appends it to durable, rotated files. See
[docs/observability.md](observability.md) for the record formats and how to
query them; this section only covers installing the follower itself.

### Installing the log-export follower

The follower needs to run continuously, starting every time the Unraid
array starts (containers can restart or rebuild; the follower needs to be
there to catch the gap). Unraid's **User Scripts** plugin is what runs it:

1. In the Unraid web UI, go to **Plugins → User Scripts**.
2. Add a new script named `itchgrep-log-export`, pointing at
   `/mnt/user/appdata/itchgrep/src/scripts/export-logs.sh`.
3. Set its schedule to **At Startup of Array**, and enable the **Run in
   Background** option — the script runs an infinite follow loop and never
   exits on its own, so without this the array's startup sequence would
   wait on it forever.
4. Start it once by hand, for the current session (User Scripts entries set
   to "At Startup of Array" don't retroactively run for an array that's
   already up).

The script also (re)installs `scripts/itchgrep-logrotate.conf` to
`/etc/logrotate.d/itchgrep` on every run, since Unraid's `/etc` is a
ramdisk wiped on every boot — this is why it needs to run at every array
start, not just once.

### After changing `scripts/export-logs.sh`

The User Scripts plugin runs its own **copy** of the script, taken at the
time the entry was created or last edited — pulling new source into
`src/` (via `scripts/update.sh` or a manual `git pull`) does **not**
automatically update what User Scripts is running. After a change to
`scripts/export-logs.sh` specifically:

1. Go to **Plugins → User Scripts** in the Unraid web UI.
2. Open the `itchgrep-log-export` entry's editor.
3. Replace its contents with the updated
   `/mnt/user/appdata/itchgrep/src/scripts/export-logs.sh`, or point the
   entry at that path directly instead of pasting the script inline, so
   future updates need no manual copy step at all.
4. Save, then stop and restart the script for the current session — the
   next array start picks up the change automatically either way.

## Recovery from a failed deploy

A deploy that fails health verification is a code problem — use "Rollback"
above. A deploy that succeeds but the *data* turns out to be missing,
corrupted, or wildly wrong is a different situation with its own recovery
path: see [docs/disaster-recovery.md](disaster-recovery.md).
