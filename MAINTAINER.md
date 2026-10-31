# Maintaining itchgrep

This is the handbook for whoever owns itchgrep in production, alone, day to
day. It assumes you've already read [docs/getting-started.md](docs/getting-started.md)
and can run the project locally. If you're looking for feature or
architecture documentation instead, start at [docs/README.md](docs/README.md).

## A normal week

Most of a week runs with no input from you:

- **The weekly crawl.** `dataservice` re-crawls itch.io and republishes the
  index automatically once the published one is older than `CRAWL_INTERVAL`
  (a week, by default) — see
  [docs/architecture.md](docs/architecture.md#how-a-crawl-flows). Nothing
  needs to trigger this by hand.
- **CI on every push to `main`, every pull request, and daily.** GitHub
  Actions builds, vets, formats, tests, and vulnerability-scans every commit
  pushed to `main` and every pull request, and re-runs on `main` every day at
  06:23 UTC so a newly published advisory surfaces there first — see
  [docs/ci-cd.md](docs/ci-cd.md).

What's worth a glance, on whatever cadence suits you (weekly is reasonable):

- **The site itself.** Load [itchgrep.com](https://itchgrep.com/) and run a
  search. The masthead states the index's own age and coverage — a
  suspiciously old date is the first sign a crawl has been failing silently.
- **The coverage line in the masthead** (and the per-crawl Coverage figure in
  `/stats`' Index history). A healthy full crawl reaches about 89%
  of the catalog. A number sagging well below that, or repeating across
  several crawls, means investigate — see
  [docs/observability.md](docs/observability.md#what-normal-looks-like-vs-what-warrants-attention)
  and [docs/dependencies.md](docs/dependencies.md#itchio).
- **GitHub Actions.** A red run on `main` means a change that looked fine
  locally broke something CI catches — `gofmt`, `go vet`, a test, or
  `govulncheck` finding a new reachable vulnerability in a dependency you
  haven't touched.

## Day-to-day development workflow

1. Edit the code.
2. Run `task templ` (or `templ generate`) if you touched any `.templ` file —
   the build fails without this step; see
   [docs/getting-started.md](docs/getting-started.md#2-generate-the-templates).
3. `go build ./...`, `go vet ./...`, and `go test ./... -count=1` — all three,
   every time; see [docs/testing.md](docs/testing.md).
4. `gofmt -l .` (excluding generated `*_templ.go` files) — CI fails on
   unformatted code.
5. Commit and push. See [CONTRIBUTING.md](CONTRIBUTING.md) for this repository's
   commit conventions.
6. Wait for CI on the pull request (a pushed branch with no open pull request
   runs nothing) or on the merged commit on `main` to go green — see
   [docs/ci-cd.md](docs/ci-cd.md).
7. On the Unraid host, run `scripts/update.sh` from the repository checkout
   directory. It pulls `main`, rebuilds with `docker compose --profile public
   up -d --build`, and waits for the webserver to report healthy — see
   [docs/deployment.md](docs/deployment.md#first-deploy-vs-update). Plain
   `docker compose up -d`, without `--build`, silently redeploys the previous
   image and skips your change entirely; never run it that way in production.

For the full recipe for a specific kind of change (add a filter, add a
route, change a config value, add a dependency), see
[docs/patterns.md](docs/patterns.md).

## Where each kind of task is documented

Rather than duplicate any of this here, go straight to the page that owns it:

| Task | Page |
| --- | --- |
| Deploy or update production | [docs/deployment.md](docs/deployment.md) |
| Troubleshoot a live problem | [docs/troubleshooting.md](docs/troubleshooting.md) |
| Recover from lost data, a lost credential, or a lost host | [docs/disaster-recovery.md](docs/disaster-recovery.md) |
| Rotate the Cloudflare Tunnel token | [docs/disaster-recovery.md](docs/disaster-recovery.md#rotating-the-cloudflare-tunnel-token) |
| Upgrade a Go dependency or a base container image | [docs/dependencies.md](docs/dependencies.md) |
| Look up or change an environment variable | [docs/configuration.md](docs/configuration.md) |
| Read logs or interpret a health check | [docs/observability.md](docs/observability.md) |

## Security responsibilities

You are the only person watching for security issues on this project. Read
[SECURITY.md](SECURITY.md) for the trust boundaries, what's logged and what
deliberately isn't, and what to do if a report comes in or something looks
compromised.

## Backups

**Backups are handled at the host level, outside this repository.** See
[docs/disaster-recovery.md](docs/disaster-recovery.md#backups-are-handled-at-the-host-level-outside-this-repository)
for what needs backing up (and what doesn't), the manual snapshot procedure
for ad hoc use before a risky change, and the restore runbook.

## Keeping docs current

**The rule: a change that alters behavior, configuration, or deployment
updates the matching document in the same commit.** A config default
changed in code and not in [docs/configuration.md](docs/configuration.md),
or a deploy step changed in `scripts/update.sh` and not in
[docs/deployment.md](docs/deployment.md), is exactly the kind of drift this
documentation set exists to prevent. If you're not sure a change is covered
anywhere, check [docs/README.md](docs/README.md) for the page it belongs on
before assuming there isn't one.

## Log analysis recipes

To answer "who is hitting the site", "what is 404ing", or "what is slow", run the `jq` recipes in [docs/observability.md](docs/observability.md#log-analysis-recipes) against the exported log files under `LOG_EXPORT_DIR`.

## The emergency top three

1. **Site down.** [docs/troubleshooting.md](docs/troubleshooting.md).
2. **Data lost.** [docs/disaster-recovery.md](docs/disaster-recovery.md).
3. **Compromised** (a credential leaked, unexpected access, a vulnerability
   actively being exploited). [SECURITY.md](SECURITY.md#incident-response).
