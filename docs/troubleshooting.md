# Troubleshooting

This document is organized by symptom — what you're actually seeing — rather
than by component. For each one: likely causes, a command to run to tell
them apart, what that command's output means, the fix, how to confirm it
worked, and when the right move is to stop and get outside help rather than
keep digging.

This is a solo-maintained project with no dedicated support channel.
"Escalate," in this document, honestly means: open an issue on the
[GitHub issue tracker](https://github.com/SkilledAlpaca/itchgrep/issues) for
a bug in this code, or the relevant dependency's own issue tracker (Cloudflare
support, `bleve`'s GitHub, and so on) for a problem that turns out to live
there instead.

## Site shows `503`

Two very different situations produce the same status code. Telling them
apart is the first thing to do, not an afterthought.

**Diagnostic:**

COMMAND: `docker logs itchgrep-webserver --tail 50`
Purpose: Shows the webserver's recent log lines, including the specific
error behind the `503`.
When to use: Any time the site shows `503` and you want to know why.
Prerequisites: Shell access to the host running the container.
Expected result: One of the two patterns below.
Common failures: `Error: No such container` means the container isn't
running at all — see "container exits immediately" below instead.
How to undo or recover: Not applicable — read-only.

**Pattern A — designed pre-data state.** No crawl has ever published on
this data directory yet (a brand-new deployment), or the webserver just
restarted and is still loading. In a container (`DATA_DIR=/data` — see
`docker-compose.yml`), the log reads:

```
ERRO: ... Initial cache load failed, will retry in background every 15s: storage.GetAssetsUpdateTime: os.Stat: stat /data/assets.json: no such file or directory
```

A bare `go run` (local dev, `DATA_DIR` defaulting to `./data`) shows the
same line with a relative path instead:
`stat data/assets.json: no such file or directory` — see
[docs/getting-started.md](getting-started.md#what-you-see-when-it-works).

This is working as designed — see
[docs/getting-started.md](getting-started.md#what-you-see-when-it-works)
for why the webserver starts immediately instead of waiting on the crawl.
**Fix:** wait. On a fresh production deploy, a full crawl takes roughly six
hours (see [docs/configuration.md](configuration.md#scrape_rps)); the
webserver picks up the result within about 5 seconds of it publishing, with
no restart needed. If it's been substantially longer than that with no
change, move to "crawl fails" below.

**Pattern B — a genuinely broken index.** Data has published before, but
the webserver can't open it. `docker logs itchgrep-dataservice` shows the
dataservice's own self-heal running at its next startup:

```
Published index is unusable (<reason>); rebuilding as scorch from assets.json - this takes minutes, the webserver keeps serving the old data meanwhile
Self-heal rebuild complete
```

This is also self-correcting, automatically, with no data loss — the full
mechanism, what triggers it, and how to force it manually if you suspect a
problem the automatic check isn't catching, is covered once, in
[docs/disaster-recovery.md](disaster-recovery.md#index-corruption). Don't
delete `/data` or restore from a backup for this — it resolves on its own
within minutes, without a recrawl.

**When to worry:** if neither pattern's log line ever appears, and `503`
persists for hours past when a crawl should have finished, check "crawl
fails or coverage below floor" below — the crawl itself may be failing
before it ever gets far enough to publish anything at all.

## Site unreachable publicly but fine on loopback

The origin (this stack) is healthy; the problem is between it and the
public internet — almost always the Cloudflare Tunnel.

**Diagnostic:**

COMMAND: `curl -o /dev/null -s -w "%{http_code}\n" http://127.0.0.1:62686/healthz`
(run on the Unraid host itself), then compare against
`curl -I https://itchgrep.com/` (run from anywhere).
Purpose: Isolates whether the origin itself is fine (first command) while
the public path is broken (second command).
When to use: Whenever the public site doesn't load but you suspect the
stack is otherwise running.
Prerequisites: Shell access to the host for the first command.
Expected result: The first returns `200`; if the second times out, errors,
or returns something other than a normal page, the problem is the tunnel,
not the origin.
Common failures: If the *first* command also fails, this isn't a tunnel
problem — see "webserver container unhealthy" below instead.
How to undo or recover: Not applicable — read-only.

If the loopback check passes but the public one doesn't:

COMMAND: `docker logs itchgrep-cloudflared-1 --tail 50` (confirm the exact
container name with `docker compose ps` — it isn't pinned)
Purpose: Shows whether `cloudflared` is connected, retrying, or has exited.
When to use: Immediately after confirming the origin itself is healthy.
Prerequisites: The stack was started with `--profile public` — if it
wasn't, there is no `cloudflared` container at all, and the public site
being unreachable is expected (see
[docs/deployment.md](deployment.md#the---profile-public-distinction)).
Expected result: A connected tunnel logs steady "Registered tunnel
connection" style lines. An unhealthy one shows repeated connection
retries, an authentication failure, or an immediate exit.
Common failures: An immediate exit with a message about a missing token
means `CLOUDFLARE_TUNNEL_TOKEN` is unset or blank in `.env` — see
[docs/configuration.md](configuration.md#cloudflare_tunnel_token). A
connection or authentication failure with a token present may mean the
token was rotated or revoked — see
[docs/disaster-recovery.md](disaster-recovery.md#rotating-the-cloudflare-tunnel-token).
How to undo or recover: Not applicable — read-only; the fix depends on
what the logs show.

Cloudflare-side causes (DNS, the tunnel's dashboard configuration, an
account-level issue) are covered in
[docs/infrastructure.md](infrastructure.md). **Escalate to Cloudflare
support** if `cloudflared`'s own logs show a healthy, connected tunnel but
the public hostname still doesn't route — at that point the problem is on
Cloudflare's side of the connection, not this stack's.

## Webserver container unhealthy or restart-looping

**First, a correction worth knowing:** `/healthz` (what Docker's
healthcheck actually polls) is deliberately independent of whether real
data has loaded — see
[docs/observability.md](observability.md#health-checks) for why. This
means a `/data` permissions problem, on its own, does **not** make Docker
report the container unhealthy or restart it — it shows up as the site
being permanently stuck on `503` (Pattern A above) while the container
itself reports perfectly healthy. If you're chasing a permissions problem,
look at "site shows 503" above, not this section.

**What genuinely does trip the healthcheck or cause a restart loop:**

**Diagnostic:**

COMMAND: `docker inspect -f '{{.State.Status}} exited={{.State.ExitCode}} oom={{.State.OOMKilled}}' itchgrep-webserver`
Purpose: Shows the container's actual run state, its last exit code, and
whether the kernel OOM-killed it — the three things that distinguish the
causes below.
When to use: Whenever `docker inspect -f '{{.State.Health.Status}}'
itchgrep-webserver` (see [docs/deployment.md](deployment.md#verification))
reports anything other than `healthy`.
Prerequisites: Shell access to the host.
Expected result: `running exited=0 oom=false` for a healthy container.
Common failures: See below.
How to undo or recover: Not applicable — read-only.

- **`oom=true`.** The kernel killed the process for using too much memory.
  The webserver holds the entire dataset in memory, and during an index
  swap both the old and new copies are briefly open at once — this can
  roughly double steady-state memory usage against the ~740 MB index size
  quoted in "Disk filling up" below. Give the host, or an explicit container
  memory limit if you set one, real headroom above that peak — this is a
  host resource problem, not a code bug to fix.
- **`exited=1` (or similar) with no `oom`.** Check the log for a `Server
  failed: ...` line at startup — the most common cause is `PORT` set to
  something that can't be bound (non-numeric, or already in use by another
  process). See [docs/configuration.md](configuration.md#port).
- **Restarting repeatedly with `restart: unless-stopped` visibly cycling
  it.** Any of the above, recurring — `restart: unless-stopped` (see
  `docker-compose.yml`) brings the container back after any exit that isn't
  an explicit `docker compose stop`, so a persistent underlying problem
  (a `PORT` misconfiguration in `.env` that survives every restart, or
  sustained memory pressure) looks like an endless loop rather than a
  one-time failure. Fix the underlying cause; the loop is a symptom, not
  the problem itself.

## `/data` permission denied (root-owned named volume from an older image)

Both images now ship an empty `/data` directory owned by `65532:65532`
(the uid/gid the containers run as), so a **fresh** named Docker volume
(`DATA_PATH` unset) is seeded with the right ownership automatically and
works out of the box — see
[docs/configuration.md](configuration.md#data_path). This only applies to a
volume Docker creates for the first time *from this image*. A named volume
that was already created by an **older** image build (before that ownership
seed existed) is still owned by root underneath, and pulling this image on
top of it does not retroactively fix it.

**Symptom:** `docker logs itchgrep-dataservice` (or the webserver) shows a
permission error opening or writing under `/data` — `permission denied`
against `assets.json`, `index.bleve/`, or similar — even though nothing
about `DATA_PATH` or the compose file changed.

**Fix, pick one:**

- If the data in that volume is disposable (a local dev setup, a test
  stack): `docker compose down -v` to delete the volume, then bring the
  stack back up — the next `docker compose up` recreates it fresh, seeded
  with the correct ownership from the image.
- If the data must be kept, `chown` the existing volume's contents in place
  with a one-shot throwaway container:
  ```
  docker run --rm -v itchgrep-data:/data alpine chown -R 65532:65532 /data
  ```
  (substitute the actual volume name if `COMPOSE_PROJECT_NAME` isn't
  `itchgrep` — `docker volume ls` shows the exact name).

A **bind-mounted** `DATA_PATH` is a different case entirely — Docker never
chowns a bind mount automatically regardless of image version. See "The
one-time ownership fix for `data/`" in
[docs/deployment.md](deployment.md#the-one-time-ownership-fix-for-data).

## Crawl fails or coverage below floor

**Diagnostic:**

COMMAND: `docker logs itchgrep-dataservice --tail 100`
Purpose: Shows the crawl's own progress and outcome.
When to use: The site is stuck on `503` past when a crawl should have
finished, or `/stats` shows a coverage figure that hasn't updated.
Prerequisites: Shell access to the host.
Expected result: Either a successful publish (`Successfully stored
assets`) or one of the failure patterns below.
Common failures: N/A — this command itself doesn't fail.
How to undo or recover: Not applicable — read-only.

**Coverage below floor** — the crawl completed, but covered less of the
catalog than `COVERAGE_FLOOR` requires, so the previous (working) index
was deliberately left in place rather than replaced with something worse:

```
Coverage 71.3% is below COVERAGE_FLOOR 75.0%; refusing to publish. The previous index is left in place.
```

The site keeps serving the last good index while this happens — this is
not an outage, but a crawl run's specific run failed and needs
investigating. Likely causes: itch.io changed its markup (the crawler's
HTML parsing no longer matches what pages actually contain — this needs a
code fix), a `COVERAGE_FLOOR` value set too close to (or above) what a
healthy crawl actually reaches (measured at roughly 89% in the codebase's
own testing — see [docs/configuration.md](configuration.md#coverage_floor)),
or a transient itch.io-side problem during that specific run.

**Backoff behavior:** a failed *scheduled* crawl (not a manually triggered
one via `/trigger-fetch`) doesn't retry immediately — it backs off:
15 minutes after the first consecutive failure, 1 hour after the second, 6
hours after the third, and the normal `CRAWL_INTERVAL` after that
(`backoffDelay` in `cmd/dataservice/main.go`). **When to worry:** once
you've seen three or more consecutive failures (the backoff has reached its
maximum step), this stopped being a transient itch.io hiccup — it's
something persistent, most likely a markup change, and needs a human to
look at the crawler code. Fewer than that, and retrying on its own is the
correct behavior — don't intervene.

## Crawl seems stuck

A crawl checkpoints its own progress periodically (every 10 minutes at
most, `checkpointInterval` in `cmd/dataservice/main.go`), and a killed or
restarted crawl resumes from the last checkpoint instead of starting over —
watch for one of these lines at the next startup:

```
Resuming crawl from checkpoint <age> old: <N> assets, <N> slices already done
No crawl checkpoint to resume (<reason>), starting fresh
Ignoring crawl checkpoint: <age> old, older than <CHECKPOINT_MAX_AGE>
```

**Before assuming it's actually stuck:** `SCRAPE_RPS` (default `2`
requests/second) deliberately paces every outbound request, so a full crawl
genuinely takes hours. During the slice walk the dataservice logs a progress
line every 5 seconds (`assets: N/M (P%), slices: a/b, pages: n, 429s: r,
<ETA>`); check whether `pages:` keeps rising rather than waiting for
completion:

COMMAND: `docker logs itchgrep-dataservice --tail 20 --follow`
Purpose: Watches for new log lines in real time, to distinguish "slow but
progressing" from "actually stopped."
When to use: A crawl has been running much longer than expected with no
recent completion or checkpoint line.
Prerequisites: Shell access to the host.
Expected result: A progress line every 5 seconds with a rising `pages:`
count, plus occasional `DEBG: ... Checkpointed <N> assets, <N> slices done`
lines and, at the end, `Successfully fetched ... assets`. Tag discovery on a
cold start (about 17 minutes) and the index build print less often.
Common failures: If genuinely nothing new appears for well over 30
minutes, check for a network-level problem reaching itch.io first (the
process could be blocked in a request that itch.io itself never answers)
before assuming a code bug.
How to undo or recover: Ctrl-C locally, or
`docker compose restart dataservice` in production, is safe — the crawl
resumes from its last checkpoint rather than restarting from zero, per the
resume behavior above.

**Note:** a crawl checkpoints itself at most every **10 minutes**
(`checkpointInterval` in `cmd/dataservice/main.go`) — verified directly
against that constant, not assumed.

## Disk filling up

**On disk, under `DATA_PATH`:** `index.bleve/` (the published search
index, roughly 740 MB at full catalog size) is by far the largest thing
present. During a crawl, a second full copy briefly exists at
`index.bleve.staging` while the new index is built, and the previous
published index is kept briefly (suffix `.previous`) during the publish
swap — so peak disk usage during a crawl can run to roughly triple a single
index's size, briefly, before the old copies are cleaned up. This is
expected during every crawl, not a leak — if the extra directories persist
long after a crawl has finished (`Successfully stored assets` logged), that
is worth investigating as a genuine problem.

**Off disk, on the host:** exported logs under `LOG_EXPORT_DIR` are the
other major consumer if `scripts/export-logs.sh`'s logrotate configuration
isn't actually taking effect — see
[docs/observability.md](observability.md) for the log pipeline and
[docs/disaster-recovery.md](disaster-recovery.md#what-can-fail-and-how-you-recognize-it)
for the specific diagnostic (`df -h`, confirming the logrotate reinstall is
actually running).

## Compose says port already in use, or the network pool overlaps

The duplicate-stack failure described in this section surfaces as one of two
errors, depending on which step fails first. Because `docker-compose.yml`
pins a fixed subnet (`172.30.99.0/24` — see
[docs/infrastructure.md](infrastructure.md)), the second stack usually fails
at network creation, before it ever tries to bind a port:

```
Error response from daemon: invalid pool request: Pool overlaps with other one on this address space
failed to create network src_default
```

If the network step is somehow passed, the port bind fails instead:

```
Bind for 127.0.0.1:62685 failed: port is already allocated
```

(or the equivalent for `62686`). Both errors have the same cause and the
same fix. **The most common cause on this project's production host
specifically:** `docker compose` was run from a directory
without `COMPOSE_PROJECT_NAME` set (or set inconsistently), so Compose
derived the project name from the checkout directory instead — `src`, for
the production checkout at `/mnt/user/appdata/itchgrep/src`. That creates a
**second, independent stack** (its own containers, its own network) trying
to bind the same host ports the first, correctly named `itchgrep` stack
already holds. This happened in production; it's the reason
`COMPOSE_PROJECT_NAME=itchgrep` is pinned in the production `.env` — see
[docs/configuration.md](configuration.md#compose_project_name).

**Diagnostic:**

COMMAND: `docker compose ls`
Purpose: Lists every Compose project Docker currently knows about, by
name — this is how you see a stray second project if one exists.
When to use: Any time you hit a port-already-in-use error you didn't
expect.
Prerequisites: Docker installed.
Expected result: One `itchgrep` project. Seeing a second one (`src`, or
anything else) confirms the cause above.
Common failures: None specific to this command.
How to undo or recover: Not applicable — read-only; see below for the fix.

**Fix:** tear down the stray project explicitly by name, then bring the
correctly named one up:

```
docker compose -p src down
docker compose --profile public up -d --build
```

(from inside the checkout, with `COMPOSE_PROJECT_NAME=itchgrep` now
actually set in `.env`). Confirm with `docker compose ls` that only
`itchgrep` remains.

## Tests fail locally

Almost always the same root cause as a build failure — see
[docs/getting-started.md](getting-started.md#2-generate-the-templates) for
the full explanation and the exact error text (`undefined: …` in
`internal/web/templates/`). Run `task templ` (or `templ generate`), then
`task test` (or `go test ./...`) again.

## Build fails on the host

The Unraid host has no Go toolchain installed at all — "build" there always
means `docker compose --build`, which runs the Go compiler *inside* a
throwaway build container (see `Dockerfile.webserver` and
`Dockerfile.dataservice`). A failure here is a host or network problem far
more often than a code problem, since the same source builds successfully
in CI (see [docs/ci-cd.md](ci-cd.md)) before it ever reaches this host.

**Diagnostic:**

COMMAND: `docker compose --profile public build 2>&1 | tail -40`
Purpose: Runs just the build step in isolation, without also trying to
start anything, and shows the actual failure point.
When to use: `docker compose ... up -d --build` failed and you need to see
why, without the noise of the rest of the up sequence.
Prerequisites: Shell access to the host.
Expected result: The specific stage and command that failed, at the
bottom of the output.
Common failures: `no space left on device` — check `df -h` and `docker
system df` on the host; a build produces intermediate layers that
accumulate over time, and `docker system prune` (after confirming nothing
important would be removed) reclaims that space. A network timeout
fetching Go modules or pulling the pinned base images means the host lost
outbound internet access — check general host connectivity first, not this
project. `exec: "go": executable file not found` inside the build log
would indicate a corrupted base image layer — re-pull it explicitly with
`docker pull golang:1.27@sha256:...` (the exact pinned digest is in
`Dockerfile.webserver` / `Dockerfile.dataservice`).
How to undo or recover: Not applicable — read-only; the fix depends on
which failure above matches.

## Container exits immediately

Neither `webserver` nor `dataservice` refuses to start over a missing
configuration value — every environment variable documented in
[docs/configuration.md](configuration.md) has a working default, verified
against `internal/config/config.go` and each call site. The verified
startup-failure cases are narrower than that:

- **`PORT` set to something unusable** (non-numeric, or a port already
  bound by something else) — both binaries fail to bind and log `Server
  failed: ...` (webserver) or `Server failed to start: ...` (dataservice),
  then exit. See [docs/configuration.md](configuration.md#port).
- **`cloudflared` given no `CLOUDFLARE_TUNNEL_TOKEN`** — this is the
  `cloudflared` binary's own behavior, not this project's code: it starts
  and exits immediately, stating so in its own log. See
  [docs/configuration.md](configuration.md#cloudflare_tunnel_token).

If a container exits immediately for a reason other than these two, that's
a genuine bug worth filing on the issue tracker — include the full log
output from `docker logs <container-name>` for the failed run.

## Heavy bot traffic

In August 2026, Googlebot and OpenAI's `OAI-SearchBot` were observed
exhaustively crawling every filter-permutation query URL the site's
filtering UI can generate — an effectively unbounded URL space, since every
combination of tags, price filter, and sort order produces its own URL, all
describing the same underlying catalog.

**Diagnosing whether you're seeing the same pattern:** in `access.jsonl`
under `LOG_EXPORT_DIR`, count records with `"query": true` per user agent
(the log records only that a query string was present, never its contents):

```
jq -s -c 'map(select(.query)) | group_by(.ua) | map({ua: .[0].ua, count: length}) | sort_by(-.count)' access.jsonl
```

A single crawler user agent dominating that list is the pattern.

**The fix already in place:** `robots.txt` (`internal/web/robots.go`)
currently disallows any URL carrying a query string:

```
Disallow: /*?*
```

verified present in the checked-out source as of this writing. Both bots
involved in the original incident respect `robots.txt`, so this alone
resolves compliant crawlers. For bots or scanners that don't respect
`robots.txt`, Cloudflare WAF rules provide a backstop — what they match
and why they're a backstop rather than the primary fix is documented once,
in [docs/infrastructure.md](infrastructure.md#waf-rules).
If you're seeing the same exhaustive-crawl pattern from a bot that ignores
`robots.txt` and the existing WAF rules don't cover it, that's a new
Cloudflare-side rule to add, not a code change here.

## Escalation summary

| If... | ...then talk to |
| --- | --- |
| A crawl keeps failing past three consecutive backoff steps, or coverage is persistently below floor | This project's own [GitHub issues](https://github.com/SkilledAlpaca/itchgrep/issues) — likely an itch.io markup change needing a parser fix |
| `cloudflared` logs show a healthy, connected tunnel but the public hostname still won't route | Cloudflare support |
| A container exits immediately for a reason not covered above | This project's own GitHub issues, with full logs attached |
| `go test ./...` fails on code you didn't write, after `templ generate` | This project's own GitHub issues |
| A dependency (`bleve`, `chi`, `templ`, `htmx`) appears to have a bug independent of this project's own code | That dependency's own issue tracker — see [docs/dependencies.md](dependencies.md) |
